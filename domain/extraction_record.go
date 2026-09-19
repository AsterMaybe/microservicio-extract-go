package domain

import "time"

const (
	StatusSuccess = "success"
	StatusError   = "error"
)

// ExtractionRecord is the metadata persisted for every extraction attempt,
// successful or failed. It is stored in MongoDB without any Mongo-specific
// concerns leaking into the core.
type ExtractionRecord struct {
	ID            string        `json:"id,omitempty" bson:"_id,omitempty"`
	Filename      string        `json:"filename" bson:"filename"`
	MimeType      string        `json:"mime_type" bson:"mime_type"`
	FileSizeBytes int64         `json:"file_size_bytes" bson:"file_size_bytes"`
	PageCount     int           `json:"page_count,omitempty" bson:"page_count,omitempty"`
	TextLength    int           `json:"text_length" bson:"text_length"`
	DurationMS    int64         `json:"duration_ms" bson:"duration_ms"`
	SHA256        string        `json:"sha256" bson:"sha256"`
	Status        string        `json:"status" bson:"status"`
	Error         *ErrorDetails `json:"error,omitempty" bson:"error,omitempty"`
	CreatedAt     time.Time     `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at" bson:"updated_at"`
}
