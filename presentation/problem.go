package presentation

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"microservicio-go/domain"
)

// Presentation-only problem types (detected before the use case, so no
// ExtractionRecord is persisted for them).
const (
	problemTypeTooLarge           = "too-large"
	problemTypeServiceUnavailable = "service-unavailable"
	// problemTypeBusy is returned when the in-flight upload admission limit is
	// exhausted: the service fails fast before buffering more uploads in RAM.
	problemTypeBusy = "busy"
)

type problemDefinition struct {
	status int
	title  string
	detail string
}

// problemRegistry maps stable slugs to their RFC 9457 presentation. Detail
// strings are fixed and never derived from internal error values.
var problemRegistry = map[string]problemDefinition{
	domain.ErrorTypeInvalidFile: {
		status: http.StatusBadRequest,
		title:  "Invalid request",
		detail: "The request does not include a valid PDF file to extract.",
	},
	domain.ErrorTypeMalformedPDF: {
		status: http.StatusUnprocessableEntity,
		title:  "Malformed document",
		detail: "The uploaded file is not a readable PDF document, or it is corrupted.",
	},
	domain.ErrorTypeTimeout: {
		status: http.StatusGatewayTimeout,
		title:  "Extraction timed out",
		detail: "Text extraction took longer than the configured timeout and was aborted.",
	},
	domain.ErrorTypeInternal: {
		status: http.StatusInternalServerError,
		title:  "Internal Server Error",
		detail: "An unexpected error occurred while processing the request.",
	},
	problemTypeTooLarge: {
		status: http.StatusRequestEntityTooLarge,
		title:  "File too large",
		detail: "The uploaded file exceeds the maximum allowed size.",
	},
	problemTypeServiceUnavailable: {
		status: http.StatusServiceUnavailable,
		title:  "Service Unavailable",
		detail: "A required dependency (e.g. the database) is currently unreachable.",
	},
	problemTypeBusy: {
		status: http.StatusServiceUnavailable,
		title:  "Service Busy",
		detail: "The service is at capacity; retry the request later.",
	},
}

type problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Instance string `json:"instance"`
}

// writeProblem emits an RFC 9457 Problem Details document for the given slug.
func writeProblem(c *gin.Context, baseURL, slug, instance string) {
	def, ok := problemRegistry[slug]
	if !ok {
		def = problemRegistry[domain.ErrorTypeInternal]
	}
	writeProblemDoc(c, baseURL, slug, def.title, def.detail, def.status, instance)
}

// writeProblemSlug emits an RFC 9457 document with an explicit definition, for
// statuses outside the domain registry (e.g. Traefik-delegated errors).
func writeProblemSlug(c *gin.Context, baseURL, slug, title, detail string, status int, instance string) {
	writeProblemDoc(c, baseURL, slug, title, detail, status, instance)
}

func writeProblemDoc(c *gin.Context, baseURL, slug, title, detail string, status int, instance string) {
	c.Header("Content-Type", "application/problem+json")
	c.JSON(status, problem{
		Type:     strings.TrimRight(baseURL, "/") + "/" + slug,
		Title:    title,
		Status:   status,
		Detail:   detail,
		Instance: instance,
	})
}
