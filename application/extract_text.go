package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"microservicio-go/domain"
)

const pdfMimeType = "application/pdf"

// ExtractTextUseCase orchestrates extraction: it validates input, derives
// metadata, queues work behind a bounded worker semaphore, calls the
// processor, and always persists an ExtractionRecord. It depends only on
// domain interfaces.
type ExtractTextUseCase struct {
	processor  domain.DocumentProcessor
	repository domain.ExtractionRepository
	limiter    chan struct{}
}

// NewExtractTextUseCase builds the use case. A non-positive concurrency falls
// back to the CPU core count.
func NewExtractTextUseCase(p domain.DocumentProcessor, r domain.ExtractionRepository, concurrency int) *ExtractTextUseCase {
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}
	return &ExtractTextUseCase{
		processor:  p,
		repository: r,
		limiter:    make(chan struct{}, concurrency),
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

	select {
	case uc.limiter <- struct{}{}:
		defer func() { <-uc.limiter }()
	case <-ctx.Done():
		return uc.fail(ctx, in, started, classify(ctx.Err()), 0, 0)
	}

	text, pages, err := uc.processor.ExtractText(ctx, in.Data)
	if err != nil {
		return uc.fail(ctx, in, started, classify(err), pages, len(text))
	}

	now := time.Now().UTC()
	rec := &domain.ExtractionRecord{
		Filename:      in.Filename,
		MimeType:      pdfMimeType,
		FileSizeBytes: int64(len(in.Data)),
		PageCount:     pages,
		TextLength:    len(text),
		DurationMS:    time.Since(started).Milliseconds(),
		SHA256:        sha256Hex(in.Data),
		Status:        domain.StatusSuccess,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := uc.repository.Save(ctx, rec); err != nil {
		return ExtractOutput{}, fmt.Errorf("persist extraction record: %w", err)
	}

	return ExtractOutput{
		Filename:   in.Filename,
		Extension:  extensionOf(in.Filename),
		MimeType:   pdfMimeType,
		Text:       text,
		PageCount:  pages,
		TextLength: len(text),
		DurationMS: time.Since(started).Milliseconds(),
	}, nil
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

func extensionOf(filename string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
}

// classify normalizes a context deadline into the domain timeout sentinel so
// the persisted record and the RFC 9457 mapping agree.
func classify(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.ErrExtractionTimeout
	}
	return err
}
