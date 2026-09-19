package application

import "context"

// ExtractInput is the raw upload handed to the use case. The MIME type is
// derived from content inside the use case, never trusted from the client.
type ExtractInput struct {
	Filename string
	Data     []byte
}

// ExtractOutput is the successful extraction result returned to the caller.
type ExtractOutput struct {
	Filename   string
	Extension  string
	MimeType   string
	Text       string
	PageCount  int
	TextLength int
	DurationMS int64
}

// Extractor is the seam the presentation layer depends on. It is satisfied by
// *ExtractTextUseCase and keeps HTTP handlers decoupled from its internals.
type Extractor interface {
	Extract(ctx context.Context, in ExtractInput) (ExtractOutput, error)
}
