package presentation_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"microservicio-go/application"
	"microservicio-go/domain"
	"microservicio-go/presentation"
)

const errBase = "https://errors.example.com"

type stubExtractor struct {
	out application.ExtractOutput
	err error
}

func (s *stubExtractor) Extract(_ context.Context, _ application.ExtractInput) (application.ExtractOutput, error) {
	if s.err != nil {
		return application.ExtractOutput{}, s.err
	}
	return s.out, nil
}

type stubPinger struct {
	err error
}

func (s *stubPinger) Ping(_ context.Context) error {
	return s.err
}

// blockingExtractor holds the handler in-flight slot open for as long as
// release is not closed, so admission saturation can be tested deterministically.
type blockingExtractor struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingExtractor) Extract(ctx context.Context, _ application.ExtractInput) (application.ExtractOutput, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return application.ExtractOutput{
			Filename:   "a.pdf",
			Extension:  "pdf",
			MimeType:   "application/pdf",
			Text:       "ok",
			PageCount:  1,
			TextLength: 2,
		}, nil
	case <-ctx.Done():
		return application.ExtractOutput{}, ctx.Err()
	}
}

func testConfig() presentation.Config {
	return presentation.Config{
		ErrBaseURL:        errBase,
		MaxUploadBytes:    10 * 1024,
		ExtractionTimeout: 5 * time.Second,
	}
}

func newTestRouter(t *testing.T, ex presentation.Extractor, p presentation.Pinger, cfg presentation.Config) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return presentation.NewRouter(ex, p, cfg)
}

func uploadRequest(t *testing.T, path, filename string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func perform(t *testing.T, router http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	if rec.Body.Len() == 0 {
		t.Fatalf("expected a problem document body, got empty")
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode problem json: %v", err)
	}
	for _, k := range []string{"type", "title", "status", "detail", "instance"} {
		if _, ok := m[k]; !ok {
			t.Errorf("problem missing field %q: %v", k, m)
		}
	}
	return m
}

func TestExtract_ValidUpload_ReturnsEnvelope(t *testing.T) {
	ex := &stubExtractor{out: application.ExtractOutput{
		Filename:   "invoice.pdf",
		Extension:  "pdf",
		MimeType:   "application/pdf",
		Text:       "hello from the pdf",
		PageCount:  1,
		TextLength: 18,
		DurationMS: 3,
	}}
	router := newTestRouter(t, ex, &stubPinger{}, testConfig())

	rec := perform(t, router, uploadRequest(t, "/api/v1/extract", "invoice.pdf", []byte("%PDF-1.7\n...")))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	want := map[string]any{"filename": "invoice.pdf", "extension": "pdf", "mime_type": "application/pdf", "text": "hello from the pdf"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("envelope[%q] = %v, want %v", k, m[k], v)
		}
	}
	for _, k := range []string{"filename", "extension", "mime_type", "text"} {
		if _, ok := m[k]; !ok {
			t.Errorf("envelope missing key %q: %v", k, m)
		}
	}
}

func TestExtract_FileTooLarge_Returns413Problem(t *testing.T) {
	cfg := testConfig()
	cfg.MaxUploadBytes = 32
	router := newTestRouter(t, &stubExtractor{}, &stubPinger{}, cfg)

	rec := perform(t, router, uploadRequest(t, "/api/v1/extract", "big.pdf", bytes.Repeat([]byte("x"), 128)))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (%s)", rec.Code, rec.Body.String())
	}
	m := decodeProblem(t, rec)
	if !strings.HasSuffix(m["type"].(string), "/too-large") {
		t.Errorf("type = %v, want to end in /too-large", m["type"])
	}
	if m["status"].(float64) != 413 {
		t.Errorf("status field = %v, want 413", m["status"])
	}
	if m["instance"] != "/api/v1/extract" {
		t.Errorf("instance = %v, want path /api/v1/extract", m["instance"])
	}
}

func TestExtract_Saturated_Returns503Problem(t *testing.T) {
	ex := &blockingExtractor{entered: make(chan struct{}), release: make(chan struct{})}
	cfg := testConfig()
	cfg.MaxInFlight = 1
	router := newTestRouter(t, ex, &stubPinger{}, cfg)

	firstReq := uploadRequest(t, "/api/v1/extract", "a.pdf", []byte("%PDF-1.7\n"))
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, firstReq)
		firstDone <- rec
	}()
	<-ex.entered // the first request is buffering and holds the only slot

	rec := perform(t, router, uploadRequest(t, "/api/v1/extract", "b.pdf", []byte("%PDF-1.7\n")))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
	m := decodeProblem(t, rec)
	if !strings.HasSuffix(m["type"].(string), "/busy") {
		t.Errorf("type = %v, want to end in /busy", m["type"])
	}

	close(ex.release)
	if first := <-firstDone; first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200 (%s)", first.Code, first.Body.String())
	}

	// Slot freed: a fresh request is admitted again.
	rec2 := perform(t, router, uploadRequest(t, "/api/v1/extract", "c.pdf", []byte("%PDF-1.7\n")))
	if rec2.Code != http.StatusOK {
		t.Fatalf("status after release = %d, want 200 (%s)", rec2.Code, rec2.Body.String())
	}
}

func TestExtract_MissingFileField_Returns400Problem(t *testing.T) {
	router := newTestRouter(t, &stubExtractor{}, &stubPinger{}, testConfig())

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("note", "no file here")
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/extract", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	rec := perform(t, router, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	m := decodeProblem(t, rec)
	if !strings.HasSuffix(m["type"].(string), "/invalid-file") {
		t.Errorf("type = %v, want to end in /invalid-file", m["type"])
	}
}

func TestExtract_MalformedPDF_Returns422Problem(t *testing.T) {
	router := newTestRouter(t, &stubExtractor{err: domain.ErrMalformedDocument}, &stubPinger{}, testConfig())

	rec := perform(t, router, uploadRequest(t, "/api/v1/extract", "broken.pdf", []byte("%PDF-1.7\nnonsense")))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (%s)", rec.Code, rec.Body.String())
	}
	m := decodeProblem(t, rec)
	if !strings.HasSuffix(m["type"].(string), "/malformed-pdf") {
		t.Errorf("type = %v, want to end in /malformed-pdf", m["type"])
	}
}

func TestExtract_Timeout_Returns504Problem(t *testing.T) {
	router := newTestRouter(t, &stubExtractor{err: context.DeadlineExceeded}, &stubPinger{}, testConfig())

	rec := perform(t, router, uploadRequest(t, "/api/v1/extract", "slow.pdf", []byte("%PDF-1.7\n")))

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 (%s)", rec.Code, rec.Body.String())
	}
	m := decodeProblem(t, rec)
	if !strings.HasSuffix(m["type"].(string), "/timeout") {
		t.Errorf("type = %v, want to end in /timeout", m["type"])
	}
}

func TestExtract_InternalError_DoesNotLeakInternals(t *testing.T) {
	secret := "secret db password for production"
	router := newTestRouter(t, &stubExtractor{err: errors.New(secret)}, &stubPinger{}, testConfig())

	rec := perform(t, router, uploadRequest(t, "/api/v1/extract", "x.pdf", []byte("%PDF-1.7\n")))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
	m := decodeProblem(t, rec)
	if !strings.HasSuffix(m["type"].(string), "/server-error") {
		t.Errorf("type = %v, want to end in /server-error", m["type"])
	}
	body := rec.Body.String()
	if strings.Contains(body, secret) {
		t.Errorf("response leaked internal details: %s", body)
	}
	for _, frag := range []string{"goroutine", "panic", "runtime.", "main.go", "failed"} {
		if strings.Contains(body, frag) {
			t.Errorf("response leaks internal fragment %q: %s", frag, body)
		}
	}
}

func TestHealth_Ok(t *testing.T) {
	router := newTestRouter(t, &stubExtractor{}, &stubPinger{}, testConfig())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := perform(t, router, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if m["status"] != "ok" {
		t.Errorf("health status = %v, want ok", m["status"])
	}
}

func TestHealth_Unavailable_Returns503Problem(t *testing.T) {
	router := newTestRouter(t, &stubExtractor{}, &stubPinger{err: errors.New("mongo unreachable")}, testConfig())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := perform(t, router, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
	m := decodeProblem(t, rec)
	if !strings.HasSuffix(m["type"].(string), "/service-unavailable") {
		t.Errorf("type = %v, want to end in /service-unavailable", m["type"])
	}
}
