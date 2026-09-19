package presentation

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"microservicio-go/application"
	"microservicio-go/domain"
)

// Extractor is the seam over the use case; satisfied by
// *application.ExtractTextUseCase.
type Extractor interface {
	Extract(ctx context.Context, in application.ExtractInput) (application.ExtractOutput, error)
}

// Pinger reports dependency health; satisfied by the mongo repository.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Handler glues the Gin HTTP layer to the application layer. It only parses
// HTTP/multipart input, enforces the upload limit, applies the per-request
// timeout, and formats RFC 9457 responses.
type Handler struct {
	extractor Extractor
	pinger    Pinger
	cfg       Config
}

func NewHandler(extractor Extractor, pinger Pinger, cfg Config) *Handler {
	return &Handler{extractor: extractor, pinger: pinger, cfg: cfg}
}

// Extract handles POST /api/v1/extract.
func (h *Handler) Extract(c *gin.Context) {
	instance := c.Request.URL.Path

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.cfg.MaxUploadBytes)

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		if isTooLarge(err) {
			writeProblem(c, h.cfg.ErrBaseURL, problemTypeTooLarge, instance)
			return
		}
		writeProblem(c, h.cfg.ErrBaseURL, domain.ErrorTypeInvalidFile, instance)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		if isTooLarge(err) {
			writeProblem(c, h.cfg.ErrBaseURL, problemTypeTooLarge, instance)
			return
		}
		writeProblem(c, h.cfg.ErrBaseURL, domain.ErrorTypeInternal, instance)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), h.cfg.ExtractionTimeout)
	defer cancel()

	out, err := h.extractor.Extract(ctx, application.ExtractInput{Filename: header.Filename, Data: data})
	if err != nil {
		h.writeExtractionError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"filename":  out.Filename,
		"extension": out.Extension,
		"mime_type": out.MimeType,
		"text":      out.Text,
	})
}

// Health handles GET /api/v1/health.
func (h *Handler) Health(c *gin.Context) {
	instance := c.Request.URL.Path
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	if err := h.pinger.Ping(ctx); err != nil {
		writeProblem(c, h.cfg.ErrBaseURL, problemTypeServiceUnavailable, instance)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) writeExtractionError(c *gin.Context, err error) {
	slug := domain.SlugFor(err)
	if errors.Is(err, context.DeadlineExceeded) {
		slug = domain.ErrorTypeTimeout
	}
	writeProblem(c, h.cfg.ErrBaseURL, slug, c.Request.URL.Path)
}

// isTooLarge detects the http.MaxBytesReader cut-off, including the
// multipart.ErrMessageTooLarge wrapping some drivers otherwise miss.
func isTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return true
	}
	return err != nil && strings.Contains(err.Error(), "message too large")
}
