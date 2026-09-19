package fitz

import (
	"context"
	"fmt"
	"strings"

	"github.com/gen2brain/go-fitz"

	"microservicio-go/domain"
)

var _ domain.DocumentProcessor = (*Adapter)(nil)

// Adapter extracts text with go-fitz (CGo bindings for MuPDF). Each call
// opens its own Document (its own MuPDF context), which is the threading
// contract required for safe concurrent use; a Document must never be shared
// across goroutines.
type Adapter struct{}

func NewAdapter() *Adapter {
	return &Adapter{}
}

func (a *Adapter) ExtractText(ctx context.Context, data []byte) (string, int, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, fmt.Errorf("extract: %w", err)
	}

	doc, err := fitz.NewFromMemory(data)
	if err != nil {
		return "", 0, fmt.Errorf("open document: %w", domain.ErrMalformedDocument)
	}
	defer doc.Close()

	pages := doc.NumPage()
	var sb strings.Builder
	for n := 0; n < pages; n++ {
		if err := ctx.Err(); err != nil {
			return sb.String(), pages, fmt.Errorf("extract page %d: %w", n, err)
		}
		text, err := doc.Text(n)
		if err != nil {
			return sb.String(), pages, fmt.Errorf("extract page %d: %w", n, err)
		}
		sb.WriteString(text)
		if n < pages-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String(), pages, nil
}
