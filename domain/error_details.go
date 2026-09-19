package domain

import "errors"

// Sentinel errors classified by the RFC 9457 problem type they map to.
// Infrastructure adapters wrap these when returning failures to the
// application layer.
var (
	ErrEmptyInput        = errors.New("empty input")
	ErrMalformedDocument = errors.New("malformed document")
	ErrExtractionTimeout = errors.New("extraction timeout")
)

// Stable problem-type slugs, shared between persisted records and the
// presentation-layer RFC 9457 problem registry.
const (
	ErrorTypeInvalidFile  = "invalid-file"
	ErrorTypeMalformedPDF = "malformed-pdf"
	ErrorTypeTimeout      = "timeout"
	ErrorTypeInternal     = "server-error"
)

// ErrorDetails is the error facet of an ExtractionRecord. It mirrors the
// problem-type contract without coupling the core to HTTP.
type ErrorDetails struct {
	Type    string `json:"type" bson:"type"`
	Message string `json:"message" bson:"message"`
	Detail  string `json:"detail,omitempty" bson:"detail,omitempty"`
}

// SlugFor maps an error to its stable problem-type slug. Unrecognized errors
// fall back to the generic internal slug rather than leaking to callers.
func SlugFor(err error) string {
	switch {
	case errors.Is(err, ErrEmptyInput):
		return ErrorTypeInvalidFile
	case errors.Is(err, ErrMalformedDocument):
		return ErrorTypeMalformedPDF
	case errors.Is(err, ErrExtractionTimeout):
		return ErrorTypeTimeout
	default:
		return ErrorTypeInternal
	}
}

// NewErrorDetails builds an ErrorDetails from an error, classifying its type
// from the wrapped sentinel.
func NewErrorDetails(err error) *ErrorDetails {
	return &ErrorDetails{
		Type:    SlugFor(err),
		Message: err.Error(),
	}
}
