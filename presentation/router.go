package presentation

import (
	"github.com/gin-gonic/gin"

	"microservicio-go/domain"
)

// NewRouter wires the HTTP routes with recovery middleware that emits RFC 9457
// problem documents on panics instead of raw internal responses.
func NewRouter(extractor Extractor, pinger Pinger, cfg Config) *gin.Engine {
	r := gin.New()
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

	r.POST("/api/v1/extract", h.Extract)
	r.GET("/api/v1/health", h.Health)

	// Serves RFC 9457 bodies for statuses Traefik's errors middleware delegates.
	r.GET("/traefik/errors/:status", h.TraefikError)
	return r
}
