package presentation

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// TraefikError serves RFC 9457 documents for statuses the gateway's `errors`
// middleware delegates back to us (429 rate-limit, 503 circuit-breaker), so
// fleet responses match the monolith's payloads instead of Traefik's default
// plain-text bodies.
func (h *Handler) TraefikError(c *gin.Context) {
	instance := c.Request.URL.Path
	statusCode, err := strconv.Atoi(strings.TrimPrefix(c.Param("status"), "/"))
	if err != nil {
		// Nothing to map: malformed status is not part of the delegated contract.
		writeProblemSlug(c, h.cfg.ErrBaseURL, "not-found", "Not Found", "", http.StatusNotFound, instance)
		return
	}

	var slug, title, detail string
	switch statusCode {
	case http.StatusTooManyRequests:
		slug = "too-many-requests"
		title = "Too Many Requests"
		detail = "Se ha excedido el límite de peticiones permitidas. Intente nuevamente más tarde."
	case http.StatusServiceUnavailable:
		slug = "service-unavailable"
		title = "Service Unavailable"
		detail = "El servicio no se encuentra disponible en este momento. Intente nuevamente más tarde."
	default:
		writeProblemSlug(c, h.cfg.ErrBaseURL, "not-found", "Not Found", "", http.StatusNotFound, instance)
		return
	}

	writeProblemSlug(c, h.cfg.ErrBaseURL, slug, title, detail, statusCode, instance)
}
