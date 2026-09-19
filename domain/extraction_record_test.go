package domain_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"microservicio-go/domain"
)

func TestStatusConstants(t *testing.T) {
	if domain.StatusSuccess != "success" {
		t.Errorf("StatusSuccess = %q, want %q", domain.StatusSuccess, "success")
	}
	if domain.StatusError != "error" {
		t.Errorf("StatusError = %q, want %q", domain.StatusError, "error")
	}
}

func TestErrorTypeSlugs(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"empty input sentinel", domain.ErrEmptyInput, domain.ErrorTypeInvalidFile},
		{"malformed sentinel", domain.ErrMalformedDocument, domain.ErrorTypeMalformedPDF},
		{"timeout sentinel", domain.ErrExtractionTimeout, domain.ErrorTypeTimeout},
		{"wrapped malformed sentinel", fmt.Errorf("open document: %w", domain.ErrMalformedDocument), domain.ErrorTypeMalformedPDF},
		{"wrapped timeout sentinel", fmt.Errorf("extract: %w", domain.ErrExtractionTimeout), domain.ErrorTypeTimeout},
		{"unknown error maps to internal", errors.New("boom"), domain.ErrorTypeInternal},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.SlugFor(tc.err); got != tc.want {
				t.Errorf("SlugFor(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestNewErrorDetails(t *testing.T) {
	err := fmt.Errorf("open document: %w", domain.ErrMalformedDocument)
	details := domain.NewErrorDetails(err)

	if details.Type != domain.ErrorTypeMalformedPDF {
		t.Errorf("Type = %q, want %q", details.Type, domain.ErrorTypeMalformedPDF)
	}
	if details.Message != err.Error() {
		t.Errorf("Message = %q, want %q", details.Message, err.Error())
	}
	if details.Detail != "" {
		t.Errorf("Detail = %q, want empty", details.Detail)
	}
}

func TestExtractionRecordMarshalsApprovedKeys(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	rec := domain.ExtractionRecord{
		Filename:      "report.pdf",
		MimeType:      "application/pdf",
		FileSizeBytes: 1024,
		PageCount:     3,
		TextLength:    150,
		DurationMS:    12,
		SHA256:        "abc123",
		Status:        domain.StatusSuccess,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	wantKeys := []string{"filename", "mime_type", "file_size_bytes", "page_count", "text_length", "duration_ms", "sha256", "status", "created_at", "updated_at"}
	for _, k := range wantKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("record JSON missing key %q; got %v", k, m)
		}
	}

	if m["page_count"].(float64) != 3 || m["text_length"].(float64) != 150 || m["status"] != "success" {
		t.Errorf("record JSON values wrong: %v", m)
	}
}
