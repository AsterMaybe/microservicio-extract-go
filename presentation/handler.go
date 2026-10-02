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

// Pinger reports dependency health; satisfied by the mongo repository.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Handler glues the Gin HTTP layer to the application layer. It parses
// HTTP/multipart input and applies the two admission filters in order, before
// any work reaches the use case: first the space bound (the bounded queue),
// then the time bound (the admission budget). It also formats RFC 9457
// responses.
type Handler struct {
	extractor application.Extractor
	pinger    Pinger
	cfg       Config
	// inflight bounds how many upload bodies are held in RAM at once.
	inflight chan struct{}
	// queue is the bounded request queue (cap QueueSize). Taken BEFORE the body
	// is read, so a queued request holds no PDF bytes; that is what makes a deep
	// queue affordable and the only reason QUEUE_SIZE can exceed MAX_IN_FLIGHT.
	queue chan struct{}
}

// Defaults for injected Config values left unset.
const (
	defaultMaxInFlight = 32
	defaultQueueSize   = 200
	// defaultAdmissionTimeout bounds queue wait + upload read + parse-slot wait.
	// Kept short: deeper than this only converts load into latency.
	defaultAdmissionTimeout = 2 * time.Second
)

// retryAfterSeconds is the Retry-After hint sent with shed requests (429/503).
const retryAfterSeconds = "1"

func NewHandler(extractor application.Extractor, pinger Pinger, cfg Config) *Handler {
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = defaultMaxInFlight
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultQueueSize
	}
	if cfg.AdmissionTimeout <= 0 {
		cfg.AdmissionTimeout = defaultAdmissionTimeout
	}
	return &Handler{
		extractor: extractor,
		pinger:    pinger,
		cfg:       cfg,
		queue:     make(chan struct{}, cfg.QueueSize),
		inflight:  make(chan struct{}, cfg.MaxInFlight),
	}
}

// Extract handles POST /extract.
func (h *Handler) Extract(c *gin.Context) {
	instance := c.Request.URL.Path

	// FILTER 1 — space. The bounded queue. Rejection is immediate and never
	// reads the body, so shedding costs nothing.
	select {
	case h.queue <- struct{}{}:
		defer func() { <-h.queue }()
	default:
		c.Header("Retry-After", retryAfterSeconds)
		writeProblem(c, h.cfg.ErrBaseURL, domain.ErrorTypeOverloaded, instance)
		return
	}

	// The admission budget bounds how long an admitted request may wait for RAM
	// to become available to buffer its body.
	admitCtx, cancelAdmission := context.WithTimeout(c.Request.Context(), h.cfg.AdmissionTimeout)
	defer cancelAdmission()

	// RAM guard. Now that the request holds a queue place it waits instead of
	// failing fast: dropping it would waste the admission it already paid for.
	// This budget bounds that wait only; the parse is budgeted separately below.
	select {
	case h.inflight <- struct{}{}:
		defer func() { <-h.inflight }()
	case <-admitCtx.Done():
		c.Header("Retry-After", retryAfterSeconds)
		writeProblem(c, h.cfg.ErrBaseURL, domain.ErrorTypeOverloaded, instance)
		return
	}

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
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(file)
	if err != nil {
		if isTooLarge(err) {
			writeProblem(c, h.cfg.ErrBaseURL, problemTypeTooLarge, instance)
			return
		}
		writeProblem(c, h.cfg.ErrBaseURL, domain.ErrorTypeInternal, instance)
		return
	}

	// FILTER 2 — time. The parse gets its own deadline, independent of the
	// admission budget: waiting for a slot must not eat into the time the
	// caller's document is allowed to take, or a healthy but slow PDF would be
	// misreported as overloaded.
	extractCtx, cancelExtract := context.WithTimeout(c.Request.Context(), h.cfg.ExtractionTimeout)
	defer cancelExtract()

	out, err := h.extractor.Extract(extractCtx, application.ExtractInput{Filename: header.Filename, Data: data})
	if err != nil {
		h.writeExtractionError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"content":    out.Content,
		"page_count": out.PageCount,
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
	if slug == domain.ErrorTypeOverloaded {
		// Tell the caller when it is worth retrying so load generators and
		// real clients can back off instead of hot-looping on a 429/503.
		c.Header("Retry-After", retryAfterSeconds)
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
