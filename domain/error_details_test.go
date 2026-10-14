package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"microservicio-go/domain"
)

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
