package domain

import "context"

// DocumentProcessor extracts text from raw document bytes. Implementations
// must respect ctx (timeout/cancellation) and own their native resources.
type DocumentProcessor interface {
	ExtractText(ctx context.Context, data []byte) (text string, pageCount int, err error)
}

// ExtractionRepository persists extraction records.
type ExtractionRepository interface {
	Save(ctx context.Context, record *ExtractionRecord) error
}
