package application_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
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

func newUseCase(p domain.DocumentProcessor, r domain.ExtractionRepository, c int) *application.ExtractTextUseCase {
	return application.NewExtractTextUseCase(p, r, c)
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

	if out.Filename != "report.pdf" || out.Extension != "pdf" || out.MimeType != "application/pdf" {
		t.Errorf("output identity fields wrong: %+v", out)
	}
	if out.Text != "hello pdf text" {
		t.Errorf("Text = %q, want extracted text", out.Text)
	}
	if out.PageCount != 4 {
		t.Errorf("PageCount = %d, want 4", out.PageCount)
	}
	if out.TextLength != len("hello pdf text") {
		t.Errorf("TextLength = %d, want %d", out.TextLength, len("hello pdf text"))
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
	if out.MimeType != "application/pdf" {
		t.Errorf("MimeType = %q, want application/pdf (content-derived)", out.MimeType)
	}
}

func TestExtract_ExtensionLowercased(t *testing.T) {
	uc := newUseCase(&stubProcessor{text: "t", pages: 1}, &stubRepository{}, 2)
	out, err := uc.Extract(context.Background(), application.ExtractInput{Filename: "REPORT.PDF", Data: pdfMagic})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if out.Extension != "pdf" {
		t.Errorf("Extension = %q, want %q", out.Extension, "pdf")
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

func TestExtract_CancelledWhileWaiting_PersistedAsTimeout(t *testing.T) {
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

	ctx2, cancel2 := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel2()
	_, err := uc.Extract(ctx2, application.ExtractInput{Filename: "b.pdf", Data: pdfMagic})
	if !errors.Is(err, domain.ErrExtractionTimeout) {
		t.Fatalf("err = %v, want ErrExtractionTimeout from queue wait", err)
	}

	recs := repo.records()
	found := false
	for _, r := range recs {
		if r.Filename == "b.pdf" && r.Error != nil && r.Error.Type == domain.ErrorTypeTimeout {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a timeout error record for b.pdf, got: %+v", recs)
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
