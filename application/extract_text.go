package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"microservicio-go/domain"
)

const pdfMimeType = "application/pdf"

const (
	// defaultCacheTimeout bounds a single Redis round trip.
	defaultCacheTimeout = 2 * time.Second
	// defaultAdmissionTimeout bounds how long an admitted request waits for a
	// parse slot. Deliberately short: queueing deeper only converts load into
	// latency. It bounds the *wait*, never the parse itself.
	defaultAdmissionTimeout = 2 * time.Second
)

// Limits configures the use case's parse concurrency. Zero or negative values
// fall back to the documented defaults.
type Limits struct {
	// Workers is how many extractions may run at once. Zero means one per CPU.
	Workers int
	// Admission bounds how long an already-admitted request may wait for a
	// parse slot. It is deliberately a separate budget from the extraction
	// deadline: waiting behind other work says nothing about how slow the
	// caller's own document is.
	Admission time.Duration
	// CacheTimeout bounds one Redis round trip.
	CacheTimeout time.Duration
}

// errAdmissionExpired means the request's admission budget elapsed while it was
// waiting for a parse slot. It is deliberately distinct from a parse failure:
// nothing was attempted, so the client is looking at queueing latency and is
// told to retry (429), not that its document was too slow (504).
var errAdmissionExpired = errors.New("admission window elapsed waiting for a worker slot")

// gate bounds simultaneous parses. It does not own the request queue: that lives
// at the admission boundary (see presentation.Handler), where a waiting request
// still holds no PDF bytes. A queue nested here could never fill, because
// requests only reach the use case after the RAM-admitting handler has already
// let them in.
//
// A buffered channel of workers is the whole mechanism: a send acquires, a
// receive releases, and waiting for room inside it *is* the queue wait.
type gate struct {
	workers chan struct{} // cap == workers: parse slots
	admit   time.Duration // budget for waiting for a slot
}

// acquire reserves a parse slot. It returns immediately when one is free, and
// otherwise waits up to the admission budget for one to free, reporting
// errAdmissionExpired if the budget runs out. The budget is applied here rather
// than inherited from ctx so it cannot shorten the parse itself.
func (g *gate) acquire(ctx context.Context) (func(), error) {
	select {
	case g.workers <- struct{}{}:
		return g.release, nil
	default:
	}
	admitCtx, cancel := context.WithTimeout(ctx, g.admit)
	defer cancel()
	select {
	case g.workers <- struct{}{}:
		return g.release, nil
	case <-admitCtx.Done():
		return nil, errAdmissionExpired
	}
}

func (g *gate) release() { <-g.workers }

// ExtractTextUseCase orchestrates extraction: it validates input, consults the
// cache by content hash, coalesces concurrent misses for the same document,
// queues work behind a bounded gate, calls the processor, and always persists
// an ExtractionRecord. It depends only on domain interfaces.
type ExtractTextUseCase struct {
	processor  domain.DocumentProcessor
	repository domain.ExtractionRepository
	cache      domain.Cache
	gate       gate
	inflight   singleflight.Group
	// cacheTimeout bounds one Redis round trip.
	cacheTimeout time.Duration
}

// NewExtractTextUseCase builds the use case. A nil cache disables caching
// (every call re-parses), which is only intended for tests and cache-less
// deployments.
func NewExtractTextUseCase(p domain.DocumentProcessor, r domain.ExtractionRepository, c domain.Cache, limits Limits) *ExtractTextUseCase {
	if limits.Workers <= 0 {
		limits.Workers = runtime.NumCPU()
	}
	if limits.Admission <= 0 {
		limits.Admission = defaultAdmissionTimeout
	}
	if limits.CacheTimeout <= 0 {
		limits.CacheTimeout = defaultCacheTimeout
	}
	return &ExtractTextUseCase{
		processor:    p,
		repository:   r,
		cache:        c,
		gate:         gate{workers: make(chan struct{}, limits.Workers), admit: limits.Admission},
		cacheTimeout: limits.CacheTimeout,
	}
}

func (uc *ExtractTextUseCase) Extract(ctx context.Context, in ExtractInput) (ExtractOutput, error) {
	started := time.Now()

	if len(in.Data) == 0 {
		return uc.fail(ctx, in, started, domain.ErrEmptyInput, 0, 0)
	}
	if strings.TrimSpace(in.Filename) == "" {
		return uc.fail(ctx, in, started, domain.ErrEmptyInput, 0, 0)
	}
	if !isPDF(in.Data) {
		return uc.fail(ctx, in, started, domain.ErrMalformedDocument, 0, 0)
	}

	hash := sha256Hex(in.Data)

	// Fast path: identical bytes already parsed. Deliberately ahead of the
	// gate: a cache hit needs no parser and no slot, so it must not queue
	// behind CPU-bound work or the cache stops paying for itself under load.
	if out, ok := uc.cachedExtract(ctx, hash, started); ok {
		return out, nil
	}

	// Slow path. Concurrent misses for the same document are coalesced so a
	// burst of the same upload parses once instead of N times.
	res, err, _ := uc.inflight.Do(hash, func() (any, error) {
		return uc.extractAndCache(ctx, in, hash, started)
	})
	if err != nil {
		return ExtractOutput{}, err
	}
	return res.(ExtractOutput), nil
}

// cachedExtract returns the cached result for hash, if any. The Redis round trip
// is bounded so a stalled cache costs a timeout instead of a request that hangs
// while holding an in-flight slot.
func (uc *ExtractTextUseCase) cachedExtract(ctx context.Context, hash string, started time.Time) (ExtractOutput, bool) {
	if uc.cache == nil {
		return ExtractOutput{}, false
	}
	readCtx, cancel := context.WithTimeout(ctx, uc.cacheTimeout)
	defer cancel()

	content, pageCount, ok := uc.cache.Get(readCtx, hash)
	if !ok {
		return ExtractOutput{}, false
	}
	return ExtractOutput{
		Content:    content,
		PageCount:  pageCount,
		DurationMS: time.Since(started).Milliseconds(),
	}, true
}

// extractAndCache queues for a worker slot, parses, persists and warms the
// cache. It must only run inside a singleflight key.
func (uc *ExtractTextUseCase) extractAndCache(ctx context.Context, in ExtractInput, hash string, started time.Time) (ExtractOutput, error) {
	// Re-check inside the coalesced call: a sibling request may have just
	// warmed the cache while this one waited for the singleflight key.
	if out, ok := uc.cachedExtract(ctx, hash, started); ok {
		return out, nil
	}

	// Wait for a parse slot. Exhausting the budget here is queueing latency, not a
	// slow document, so it sheds with 429 and is not persisted. This holds
	// whichever deadline fired: the admission budget or the extraction budget.
	// Both mean the same thing — we never got to parse, so we must not claim
	// the document took too long.
	release, err := uc.gate.acquire(ctx)
	if err != nil {
		return uc.reject(domain.ErrOverloaded)
	}
	defer release()

	text, pages, err := uc.processor.ExtractText(ctx, in.Data)
	if err != nil {
		return uc.fail(ctx, in, started, classify(err), pages, len(text))
	}

	uc.warmCache(ctx, hash, text, pages)

	now := time.Now().UTC()
	rec := &domain.ExtractionRecord{
		Filename:      in.Filename,
		MimeType:      pdfMimeType,
		FileSizeBytes: int64(len(in.Data)),
		PageCount:     pages,
		TextLength:    len(text),
		DurationMS:    time.Since(started).Milliseconds(),
		SHA256:        hash,
		Status:        domain.StatusSuccess,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := uc.repository.Save(ctx, rec); err != nil {
		return ExtractOutput{}, fmt.Errorf("persist extraction record: %w", err)
	}

	return ExtractOutput{
		Content:    text,
		PageCount:  pages,
		DurationMS: time.Since(started).Milliseconds(),
	}, nil
}

// warmCache stores the result best-effort. A cache failure must not fail the
// request: the extraction itself succeeded. The write uses a detached context
// so a client hang-up mid-response does not discard a completed parse.
func (uc *ExtractTextUseCase) warmCache(ctx context.Context, hash, text string, pages int) {
	if uc.cache == nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uc.cacheTimeout)
	defer cancel()
	_ = uc.cache.Set(writeCtx, hash, text, pages)
}

// fail records an error record (best-effort) and returns the classified error.
func (uc *ExtractTextUseCase) fail(ctx context.Context, in ExtractInput, started time.Time, err error, pages, textLen int) (ExtractOutput, error) {
	now := time.Now().UTC()
	rec := &domain.ExtractionRecord{
		Filename:      in.Filename,
		MimeType:      pdfMimeTypeIf(in.Data),
		FileSizeBytes: int64(len(in.Data)),
		PageCount:     pages,
		TextLength:    textLen,
		DurationMS:    now.Sub(started).Milliseconds(),
		SHA256:        sha256Hex(in.Data),
		Status:        domain.StatusError,
		Error:         domain.NewErrorDetails(classify(err)),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	_ = uc.repository.Save(ctx, rec)
	return ExtractOutput{}, classify(err)
}

// reject refuses a request that we never attempted, without persisting a
// record. Persistence here would be actively harmful: shedding is the response
// to load we are already failing to absorb, so writing one Mongo document per
// shed request would amplify exactly the overload it exists to relieve, and add
// latency to a rejection that promises to be immediate.
func (uc *ExtractTextUseCase) reject(err error) (ExtractOutput, error) {
	return ExtractOutput{}, classify(err)
}

// isPDF returns whether data begins with the PDF magic bytes.
func isPDF(data []byte) bool {
	return len(data) >= 5 && string(data[:5]) == "%PDF-"
}

func pdfMimeTypeIf(data []byte) string {
	if isPDF(data) {
		return pdfMimeType
	}
	return ""
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// classify normalizes a context deadline into the domain timeout sentinel so
// the persisted record and the RFC 9457 mapping agree.
func classify(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.ErrExtractionTimeout
	}
	return err
}
