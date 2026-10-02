package application_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"microservicio-go/application"
	"microservicio-go/domain"
)

var pdfMagic = []byte("%PDF-1.7\n%%EOF")

// stubProcessor returns canned results and optionally an error. It does not
// block, so it cannot model waiting on the worker semaphore.
type stubProcessor struct {
	text  string
	pages int
	err   error
}

func (s *stubProcessor) ExtractText(_ context.Context, _ []byte) (string, int, error) {
	return s.text, s.pages, s.err
}

// blockingProcessor blocks until released, exercising the semaphore wait path.
type blockingProcessor struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingProcessor) ExtractText(ctx context.Context, _ []byte) (string, int, error) {
	close(b.started)
	select {
	case <-b.release:
		return "text", 1, nil
	case <-ctx.Done():
		return "", 0, ctx.Err()
	}
}

type stubRepository struct {
	mu      sync.Mutex
	saved   []*domain.ExtractionRecord
	saveErr error
}

func (r *stubRepository) Save(_ context.Context, rec *domain.ExtractionRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return r.saveErr
	}
	cp := *rec
	r.saved = append(r.saved, &cp)
	return nil
}

func (r *stubRepository) records() []*domain.ExtractionRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.saved
}

func newUseCase(p domain.DocumentProcessor, r domain.ExtractionRepository, workers int) *application.ExtractTextUseCase {
	return application.NewExtractTextUseCase(p, r, nil, application.Limits{Workers: workers})
}

// stubCache is an in-memory domain.Cache that records lookups and writes.
type stubCache struct {
	mu      sync.Mutex
	entries map[string]stubCacheEntry
	gets    int
	sets    int
}

type stubCacheEntry struct {
	content   string
	pageCount int
}

func newStubCache() *stubCache {
	return &stubCache{entries: map[string]stubCacheEntry{}}
}

func (c *stubCache) Get(_ context.Context, key string) (string, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	e, ok := c.entries[key]
	if !ok {
		return "", 0, false
	}
	return e.content, e.pageCount, true
}

func (c *stubCache) Set(_ context.Context, key, content string, pageCount int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sets++
	c.entries[key] = stubCacheEntry{content: content, pageCount: pageCount}
	return nil
}

func (c *stubCache) stats() (gets, sets int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets, c.sets
}

// countingProcessor records how many times the parser actually ran.
type countingProcessor struct {
	mu    sync.Mutex
	calls int
	text  string
	pages int
}

func (p *countingProcessor) ExtractText(_ context.Context, _ []byte) (string, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.text, p.pages, nil
}

func (p *countingProcessor) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestExtract_CacheMiss_ParsesAndWarmsCache(t *testing.T) {
	processor := &countingProcessor{text: "cached body", pages: 3}
	cache := newStubCache()
	uc := application.NewExtractTextUseCase(processor, &stubRepository{}, cache, application.Limits{Workers: 2})

	data := []byte("%PDF-1.7\ncacheable document")
	out, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "a.pdf", Data: data})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if out.Content != "cached body" || out.PageCount != 3 {
		t.Fatalf("unexpected output: %+v", out)
	}
	if processor.callCount() != 1 {
		t.Errorf("parser calls = %d, want 1 on a miss", processor.callCount())
	}
	if _, sets := cache.stats(); sets != 1 {
		t.Errorf("cache writes = %d, want 1 after a miss", sets)
	}
}

func TestExtract_CacheHit_SkipsParser(t *testing.T) {
	processor := &countingProcessor{text: "cached body", pages: 3}
	cache := newStubCache()
	uc := application.NewExtractTextUseCase(processor, &stubRepository{}, cache, application.Limits{Workers: 2})

	data := []byte("%PDF-1.7\ncacheable document")
	first, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "a.pdf", Data: data})
	if err != nil {
		t.Fatalf("first Extract: %v", err)
	}

	second, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "different-name.pdf", Data: data})
	if err != nil {
		t.Fatalf("second Extract: %v", err)
	}

	if processor.callCount() != 1 {
		t.Errorf("parser calls = %d, want 1: the hit must not re-parse", processor.callCount())
	}
	if second != first {
		t.Errorf("cached response must match the original: %+v vs %+v", second, first)
	}
}

func TestExtract_CacheHit_SkipsPersistence(t *testing.T) {
	processor := &countingProcessor{text: "t", pages: 1}
	repo := &stubRepository{}
	cache := newStubCache()
	uc := application.NewExtractTextUseCase(processor, repo, cache, application.Limits{Workers: 2})

	data := []byte("%PDF-1.7\nrepeat")
	if _, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "a.pdf", Data: data}); err != nil {
		t.Fatalf("first Extract: %v", err)
	}
	if _, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "a.pdf", Data: data}); err != nil {
		t.Fatalf("second Extract: %v", err)
	}
	if recs := repo.records(); len(recs) != 1 {
		t.Errorf("records = %d, want 1: a cache hit must not persist again", len(recs))
	}
}

func TestExtract_ConcurrentSameDocument_ParsesOnce(t *testing.T) {
	processor := &countingProcessor{text: "coalesced", pages: 2}
	cache := newStubCache()
	uc := application.NewExtractTextUseCase(processor, &stubRepository{}, cache, application.Limits{Workers: 4})

	data := []byte("%PDF-1.7\nstampede candidate")
	const n = 12
	results := make([]application.ExtractOutput, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = uc.Extract(context.Background(), application.ExtractInput{Filename: "a.pdf", Data: data})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if results[i].Content != "coalesced" || results[i].PageCount != 2 {
			t.Errorf("goroutine %d got %+v, want the shared result", i, results[i])
		}
	}
	if got := processor.callCount(); got != 1 {
		t.Errorf("parser calls = %d, want 1: concurrent misses must coalesce", got)
	}
}

func TestExtract_AdmissionWindowElapsedShedsWithOverloaded(t *testing.T) {
	blocking := &blockingProcessor{started: make(chan struct{}), release: make(chan struct{})}
	repo := &stubRepository{}
	uc := application.NewExtractTextUseCase(blocking, repo, nil, application.Limits{Workers: 1})

	firstDone := make(chan error, 1)
	go func() {
		_, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "a.pdf", Data: pdfMagic})
		firstDone <- err
	}()
	<-blocking.started // the only worker slot is occupied

	// A different document waits for the busy slot. The admission budget comes
	// from the caller's context, which is what the HTTP layer sets; here we
	// simulate an already-admitted request whose budget runs out while queued.
	admitCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := uc.Extract(admitCtx, application.ExtractInput{
		Filename: "b.pdf",
		Data:     []byte("%PDF-1.7\nshed me"),
	})
	if !errors.Is(err, domain.ErrOverloaded) {
		t.Fatalf("err = %v, want ErrOverloaded", err)
	}
	if got := domain.SlugFor(err); got != domain.ErrorTypeOverloaded {
		t.Errorf("SlugFor = %q, want %q", got, domain.ErrorTypeOverloaded)
	}

	close(blocking.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first extraction failed: %v", err)
	}

	// A refusal is not an attempt, so only the completed request is recorded.
	if got := len(repo.records()); got != 1 {
		t.Errorf("persisted %d record(s), want 1 (only the completed request)", got)
	}
}

// blockedProcessor blocks every call until release is closed, so N requests can
// pile up against the same worker pool deterministically.
type blockedProcessor struct {
	release chan struct{}
	calls   atomic.Int32
}

func (b *blockedProcessor) ExtractText(ctx context.Context, _ []byte) (string, int, error) {
	b.calls.Add(1)
	select {
	case <-b.release:
		return "queued", 1, nil
	case <-ctx.Done():
		return "", 0, ctx.Err()
	}
}

// TestExtract_ConcurrentMissesWaitForSlotsThenAllSucceed covers the use-case
// half of the hybrid pattern: once admitted, a burst of distinct documents
// waits for parse slots instead of being dropped, and every one of them
// completes as slots free up. The bounded queue that sheds arrivals lives in
// the HTTP layer (see TestExtract_FullQueueShedsImmediately there).
func TestExtract_ConcurrentMissesWaitForSlotsThenAllSucceed(t *testing.T) {
	processor := &blockedProcessor{release: make(chan struct{})}
	repo := &stubRepository{}
	uc := application.NewExtractTextUseCase(processor, repo, nil, application.Limits{
		Workers: 1, // one parse at a time
	})

	const burst = 8
	var (
		wg        sync.WaitGroup
		completed atomic.Int32
	)
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct bytes: different hashes, so no singleflight coalescing
			// and every request really contends for the single worker slot.
			_, err := uc.Extract(context.Background(), application.ExtractInput{
				Filename: "q.pdf",
				Data:     []byte("%PDF-1.7\ndocument number " + strconv.Itoa(i)),
			})
			if err != nil {
				t.Errorf("request %d failed: %v", i, err)
			}
			completed.Add(1)
		}(i)
	}

	// Exactly one runs; the rest are waiting on the slot, not shed. None may have
	// completed, because every one of them is still blocked on the worker.
	time.Sleep(150 * time.Millisecond)
	if got := processor.calls.Load(); got != 1 {
		t.Fatalf("parser calls = %d, want exactly 1 running while the rest wait", got)
	}
	if got := completed.Load(); got != 0 {
		t.Fatalf("%d requests already completed; they should still be waiting", got)
	}

	close(processor.release)
	wg.Wait()
	if got := completed.Load(); got != burst {
		t.Errorf("completed = %d, want %d: every waiting request must eventually run", got, burst)
	}
	if got := processor.calls.Load(); got != burst {
		t.Errorf("parser calls = %d, want %d", got, burst)
	}
}

// TestExtract_RealFailureStillPersists guards the other side of the rule: only
// refusals skip persistence. A document that genuinely fails to extract is a
// useful record and must still be stored.
func TestExtract_RealFailureStillPersists(t *testing.T) {
	repo := &stubRepository{}
	uc := application.NewExtractTextUseCase(
		&stubProcessor{err: domain.ErrMalformedDocument}, repo, nil, application.Limits{Workers: 1})

	if _, err := uc.Extract(context.Background(), application.ExtractInput{
		Filename: "broken.pdf",
		Data:     pdfMagic,
	}); !errors.Is(err, domain.ErrMalformedDocument) {
		t.Fatalf("err = %v, want ErrMalformedDocument", err)
	}
	if got := len(repo.records()); got != 1 {
		t.Errorf("persisted %d record(s), want 1: a real extraction failure must be recorded", got)
	}
}

func TestExtract_Success(t *testing.T) {
	processor := &stubProcessor{text: "hello pdf text", pages: 4}
	repo := &stubRepository{}
	uc := newUseCase(processor, repo, 2)

	data := []byte("%PDF-1.7\nhello pdf text")
	out, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "report.pdf", Data: data})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}

	if out.Content != "hello pdf text" {
		t.Errorf("Content = %q, want extracted text", out.Content)
	}
	if out.PageCount != 4 {
		t.Errorf("PageCount = %d, want 4", out.PageCount)
	}
	if out.DurationMS < 0 {
		t.Errorf("DurationMS = %d, want >= 0", out.DurationMS)
	}

	recs := repo.records()
	if len(recs) != 1 {
		t.Fatalf("saved %d records, want 1", len(recs))
	}
	rec := recs[0]
	if rec.Status != domain.StatusSuccess {
		t.Errorf("Status = %q, want %q", rec.Status, domain.StatusSuccess)
	}
	if rec.Error != nil {
		t.Errorf("Error = %+v, want nil on success", rec.Error)
	}
	if rec.Filename != "report.pdf" || rec.PageCount != 4 || rec.TextLength != len("hello pdf text") {
		t.Errorf("record fields wrong: %+v", rec)
	}
	if rec.FileSizeBytes != int64(len(data)) {
		t.Errorf("FileSizeBytes = %d, want %d", rec.FileSizeBytes, len(data))
	}
	sum := sha256.Sum256(data)
	if rec.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("SHA256 = %q, want %q", rec.SHA256, hex.EncodeToString(sum[:]))
	}
	if rec.CreatedAt.IsZero() || rec.UpdatedAt.IsZero() {
		t.Errorf("timestamps must be set")
	}
}

func TestExtract_MimeDerivedFromContent(t *testing.T) {
	uc := newUseCase(&stubProcessor{text: "t", pages: 1}, &stubRepository{}, 2)
	out, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "x", Data: pdfMagic})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if out.Content != "t" {
		t.Errorf("Content = %q, want extracted text", out.Content)
	}
}

func TestExtract_ExtensionLowercased(t *testing.T) {
	uc := newUseCase(&stubProcessor{text: "t", pages: 1}, &stubRepository{}, 2)
	out, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "REPORT.PDF", Data: pdfMagic})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if out.Content != "t" {
		t.Errorf("Content = %q, want extracted text", out.Content)
	}
}

func assertOneErrorRecord(t *testing.T, repo *stubRepository, wantType string) {
	t.Helper()
	recs := repo.records()
	if len(recs) != 1 {
		t.Fatalf("saved %d records, want 1", len(recs))
	}
	rec := recs[0]
	if rec.Status != domain.StatusError || rec.Error == nil {
		t.Fatalf("expected error record, got: %+v", rec)
	}
	if rec.Error.Type != wantType {
		t.Errorf("Error.Type = %q, want %q", rec.Error.Type, wantType)
	}
	if rec.Error.Message == "" {
		t.Errorf("Error.Message must not be empty")
	}
}

func TestExtract_RejectsNonPDF(t *testing.T) {
	repo := &stubRepository{}
	uc := newUseCase(&stubProcessor{}, repo, 2)
	_, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "nope.txt", Data: []byte("not a pdf at all")})
	if !errors.Is(err, domain.ErrMalformedDocument) {
		t.Fatalf("err = %v, want ErrMalformedDocument", err)
	}
	assertOneErrorRecord(t, repo, domain.ErrorTypeMalformedPDF)
}

func TestExtract_EmptyInput(t *testing.T) {
	repo := &stubRepository{}
	uc := newUseCase(&stubProcessor{}, repo, 2)
	_, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "empty.pdf", Data: []byte{}})
	if !errors.Is(err, domain.ErrEmptyInput) {
		t.Fatalf("err = %v, want ErrEmptyInput", err)
	}
	assertOneErrorRecord(t, repo, domain.ErrorTypeInvalidFile)
}

func TestExtract_EmptyFilename(t *testing.T) {
	uc := newUseCase(&stubProcessor{}, &stubRepository{}, 2)
	_, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "", Data: pdfMagic})
	if !errors.Is(err, domain.ErrEmptyInput) {
		t.Fatalf("err = %v, want ErrEmptyInput", err)
	}
}

func TestExtract_ProcessorError_PersistedAsMalformed(t *testing.T) {
	repo := &stubRepository{}
	uc := newUseCase(&stubProcessor{err: domain.ErrMalformedDocument}, repo, 2)
	_, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "corrupt.pdf", Data: pdfMagic})
	if !errors.Is(err, domain.ErrMalformedDocument) {
		t.Fatalf("err = %v, want ErrMalformedDocument", err)
	}
	assertOneErrorRecord(t, repo, domain.ErrorTypeMalformedPDF)
}

func TestExtract_ProcessorUnknownError_PersistedAsInternal(t *testing.T) {
	repo := &stubRepository{}
	uc := newUseCase(&stubProcessor{err: errors.New("boom")}, repo, 2)
	_, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "x.pdf", Data: pdfMagic})
	if err == nil || errors.Is(err, domain.ErrMalformedDocument) || errors.Is(err, domain.ErrEmptyInput) {
		t.Fatalf("err = %v, want the raw unexpected error", err)
	}
	assertOneErrorRecord(t, repo, domain.ErrorTypeInternal)
}

func TestExtract_ProcessorDeadline_PersistedAsTimeout(t *testing.T) {
	repo := &stubRepository{}
	uc := newUseCase(&stubProcessor{err: context.DeadlineExceeded}, repo, 2)
	_, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "x.pdf", Data: pdfMagic})
	if !errors.Is(err, domain.ErrExtractionTimeout) {
		t.Fatalf("err = %v, want ErrExtractionTimeout", err)
	}
	assertOneErrorRecord(t, repo, domain.ErrorTypeTimeout)
}

// TestExtract_CancelledWhileWaiting_ShedsAsOverloadedNotTimeout pins the
// distinction that matters under load: a request that dies waiting for a slot
// never got parsed, so it must not be reported as an extraction timeout. A 504
// tells clients the document was too slow and not to retry, which is the wrong
// advice exactly when the service is under pressure.
func TestExtract_CancelledWhileWaiting_ShedsAsOverloadedNotTimeout(t *testing.T) {
	blocking := &blockingProcessor{started: make(chan struct{}), release: make(chan struct{})}
	repo := &stubRepository{}
	uc := newUseCase(blocking, repo, 1)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	firstDone := make(chan error, 1)
	go func() {
		_, err := uc.Extract(ctx1, application.ExtractInput{Filename: "a.pdf", Data: pdfMagic})
		firstDone <- err
	}()
	<-blocking.started // first call holds the only slot

	// Distinct bytes: a different content hash means a different coalescing
	// key, so this request must genuinely queue for the worker slot.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel2()
	other := []byte("%PDF-1.7\nsecond document")
	_, err := uc.Extract(ctx2, application.ExtractInput{Filename: "b.pdf", Data: other})
	if !errors.Is(err, domain.ErrOverloaded) {
		t.Fatalf("err = %v, want ErrOverloaded (never parsed), got ErrExtractionTimeout", err)
	}
	if errors.Is(err, domain.ErrExtractionTimeout) {
		t.Error("a queue wait must never be reported as an extraction timeout")
	}
	if got := domain.SlugFor(err); got != domain.ErrorTypeOverloaded {
		t.Errorf("SlugFor = %q, want %q", got, domain.ErrorTypeOverloaded)
	}

	close(blocking.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first extraction failed: %v", err)
	}
}

func TestExtract_RepositoryFailure_SurfacesToCaller(t *testing.T) {
	saveErr := errors.New("mongo down")
	repo := &stubRepository{saveErr: saveErr}
	uc := newUseCase(&stubProcessor{text: "t", pages: 1}, repo, 2)
	_, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "ok.pdf", Data: pdfMagic})
	if !errors.Is(err, saveErr) {
		t.Fatalf("err = %v, want the persistence error surfaced to the caller", err)
	}
	if recs := repo.records(); len(recs) != 0 {
		t.Errorf("failed save must not append a record, got %d", len(recs))
	}
}
