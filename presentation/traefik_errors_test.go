package presentation_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func expectProblem(t *testing.T, rr *httptest.ResponseRecorder, wantStatus int, wantType, wantTitle, wantDetail, wantInstance string) {
	t.Helper()
	if rr.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body %s)", rr.Code, wantStatus, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	assertField(t, body, "type", wantType)
	assertField(t, body, "title", wantTitle)
	assertField(t, body, "detail", wantDetail)
	assertField(t, body, "instance", wantInstance)
	if status, ok := body["status"].(float64); !ok || int(status) != wantStatus {
		t.Errorf("status field = %v, want %d", body["status"], wantStatus)
	}
}

func assertField(t *testing.T, body map[string]any, key, want string) {
	t.Helper()
	if got, _ := body[key].(string); got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

func TestTraefikError_RateLimited_ReturnsProblemDetails(t *testing.T) {
	r := newTestRouter(t, &stubExtractor{}, &stubPinger{}, testConfig())

	req := httptest.NewRequest(http.MethodGet, "/traefik/errors/429", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	expectProblem(t, rr, http.StatusTooManyRequests,
		errBase+"/too-many-requests",
		"Too Many Requests",
		"Se ha excedido el límite de peticiones permitidas. Intente nuevamente más tarde.",
		"/traefik/errors/429")
}

func TestTraefikError_ServiceUnavailable_ReturnsProblemDetails(t *testing.T) {
	r := newTestRouter(t, &stubExtractor{}, &stubPinger{}, testConfig())

	req := httptest.NewRequest(http.MethodGet, "/traefik/errors/503", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	expectProblem(t, rr, http.StatusServiceUnavailable,
		errBase+"/service-unavailable",
		"Service Unavailable",
		"El servicio no se encuentra disponible en este momento. Intente nuevamente más tarde.",
		"/traefik/errors/503")
}

func TestTraefikError_UnknownStatus_ReturnsNotFoundProblem(t *testing.T) {
	r := newTestRouter(t, &stubExtractor{}, &stubPinger{}, testConfig())

	req := httptest.NewRequest(http.MethodGet, "/traefik/errors/400", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	expectProblem(t, rr, http.StatusNotFound, errBase+"/not-found", "Not Found", "", "/traefik/errors/400")
}
