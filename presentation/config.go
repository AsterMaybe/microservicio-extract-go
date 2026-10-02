package presentation

import "time"

// Config holds presentation-layer settings injected by the composition root.
type Config struct {
	// ErrBaseURL is the base URI for RFC 9457 problem "type" values.
	ErrBaseURL string
	// MaxUploadBytes caps the accepted multipart upload size.
	MaxUploadBytes int64
	// ExtractionTimeout bounds a single extraction, once the request has been
	// admitted. It must exceed AdmissionTimeout.
	ExtractionTimeout time.Duration
	// AdmissionTimeout bounds the whole admission path: the wait for a place in
	// the queue, the upload read, and the wait for a parse slot. Exceeding it is
	// queueing latency, reported as 429 so clients retry instead of giving up.
	AdmissionTimeout time.Duration
	// MaxInFlight bounds how many upload bodies are buffered in RAM at once.
	// Zero falls back to a safe default (see Handler).
	MaxInFlight int
	// QueueSize bounds how many admitted requests may wait for a parse slot
	// before the service sheds with 429. It is taken before the body is read, so
	// a queued request holds no PDF bytes and a deep queue stays cheap. Zero
	// falls back to a safe default (see Handler).
	QueueSize int
}
