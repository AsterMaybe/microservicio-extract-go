package presentation

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync/atomic"

	"github.com/gin-gonic/gin"

	"microservicio-go/application"
	"microservicio-go/domain"
)

// requestIDPrefix is generated once per process so IDs stay unique across
// replicas; the counter makes them unique (and cheap) within a process. This
// replaces a per-request uuid.New(), whose crypto-RNG read is pure overhead on
// the hot path for a value that only ever feeds tracing headers.
var (
	requestIDPrefix string
	requestCounter  atomic.Uint64
)

func init() {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		requestIDPrefix = "req"
		return
	}
	requestIDPrefix = hex.EncodeToString(b[:])
}

func nextRequestID() string {
	return requestIDPrefix + "-" + strconv.FormatUint(requestCounter.Add(1), 36)
}

// NewRouter wires the HTTP routes with recovery middleware that emits RFC 9457
// problem documents on panics instead of raw internal responses.
func NewRouter(extractor application.Extractor, pinger Pinger, cfg Config) *gin.Engine {
	r := gin.New()
	r.Use(requestIDMiddleware())
	r.Use(func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				c.Abort()
				writeProblem(c, cfg.ErrBaseURL, domain.ErrorTypeInternal, c.Request.URL.Path)
			}
		}()
		c.Next()
	})

	h := NewHandler(extractor, pinger, cfg)

	r.POST("/extract", h.Extract)
	r.GET("/api/v1/health", h.Health)

	// Serves RFC 9457 bodies for statuses Traefik's errors middleware delegates.
	r.GET("/traefik/errors/:status", h.TraefikError)
	return r
}

// requestIDMiddleware adds a unique request ID to each request for tracing.
// The ID is stored in the context and can be retrieved via c.GetString("request_id").
func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = nextRequestID()
		}
		c.Set("request_id", requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}
