package presentation

import "time"

// Config holds presentation-layer settings injected by the composition root.
type Config struct {
	// ErrBaseURL is the base URI for RFC 9457 problem "type" values.
	ErrBaseURL string
	// MaxUploadBytes caps the accepted multipart upload size.
	MaxUploadBytes int64
	// ExtractionTimeout bounds a single extraction, including queue wait.
	ExtractionTimeout time.Duration
	// MaxInFlight bounds simultaneous uploads buffered by handlers. Zero falls
	// back to a safe default (see Handler).
	MaxInFlight int
}
