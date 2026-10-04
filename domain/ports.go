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

// Cache provides a key-value store for extraction results.
// Keys are SHA-256 hashes of PDF content.
type Cache interface {
	Get(ctx context.Context, key string) (string, int, bool)
	Set(ctx context.Context, key string, content string, pageCount int) error
}

// HealthChecker reports the health of a dependency.
type HealthChecker interface {
	Ping(ctx context.Context) error
}
