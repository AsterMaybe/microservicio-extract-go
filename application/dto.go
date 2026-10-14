package application

import "context"

// ExtractInput is the raw upload handed to the use case. The MIME type is
// derived from content inside the use case, never trusted from the client.
type ExtractInput struct {
	Filename string
	Data     []byte
	// Hash is the optional hex SHA-256 of Data, computed by a caller that
	// already streamed the bytes. When empty the use case computes it. It is
	// only an optimisation: a wrong value reuses the wrong cache entry, so
	// callers that set it must hash exactly Data.
	Hash string
}

// ExtractOutput is the successful extraction result returned to the caller.
type ExtractOutput struct {
	Content    string
	PageCount  int
	DurationMS int64
}

// Extractor is the seam the presentation layer depends on. It is satisfied by
// *ExtractTextUseCase and keeps HTTP handlers decoupled from its internals.
type Extractor interface {
	Extract(ctx context.Context, in ExtractInput) (ExtractOutput, error)
}
