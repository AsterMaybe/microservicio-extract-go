package fitz_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"microservicio-go/domain"
	fitzadapter "microservicio-go/infrastructure/fitz"
)

// buildMinimalPDF generates a structurally valid single/multi-page PDF with
// its xref computed from real byte offsets, so MuPDF can parse it without the
// need to commit a binary fixture.
func buildMinimalPDF(t *testing.T, pages int, body string) []byte {
	t.Helper()
	if pages < 1 {
		t.Fatal("pages must be >= 1")
	}

	var buf bytes.Buffer
	var offsets []int
	buf.WriteString("%PDF-1.4\n")

	writeObj := func(num int, content string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, content)
	}

	stream := fmt.Sprintf("BT /F1 24 Tf 72 720 Td (%s) Tj ET", body)
	fontObjNum := 3 + pages
	contentsObjNum := 4 + pages

	kids := make([]string, pages)
	for p := 0; p < pages; p++ {
		kids[p] = fmt.Sprintf("%d 0 R", 3+p)
	}

	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pages))
	for p := 0; p < pages; p++ {
		writeObj(3+p, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 %d 0 R >> >> >>", contentsObjNum, fontObjNum))
	}
	writeObj(fontObjNum, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	writeObj(contentsObjNum, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))

	xrefOffset := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(offsets)+1)
	fmt.Fprintf(&buf, "0000000000 65535 f \n")
	for _, o := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xrefOffset)
	return buf.Bytes()
}

func TestExtractText_ExtractsTextFromSinglePage(t *testing.T) {
	data := buildMinimalPDF(t, 1, "Hello PDF World")
	text, pages, err := fitzadapter.NewAdapter().ExtractText(context.Background(), data)
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}
	if pages != 1 {
		t.Errorf("pages = %d, want 1", pages)
	}
	if !strings.Contains(text, "Hello PDF World") {
		t.Errorf("text = %q, want it to contain the extracted body", text)
	}
}

func TestExtractText_ExtractsAllPages(t *testing.T) {
	data := buildMinimalPDF(t, 3, "Page Body")
	text, pages, err := fitzadapter.NewAdapter().ExtractText(context.Background(), data)
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}
	if pages != 3 {
		t.Errorf("pages = %d, want 3", pages)
	}
	if got := strings.Count(text, "Page Body"); got != 3 {
		t.Errorf("body appears %d times, want 3 (once per page)", got)
	}
}

func TestExtractText_MalformedInput_ReturnsDomainError(t *testing.T) {
	cases := [][]byte{
		[]byte("this is definitely not a pdf"),
		[]byte("%PDF-1.4\nbroken"),
		{},
	}
	for i, data := range cases {
		_, _, err := fitzadapter.NewAdapter().ExtractText(context.Background(), data)
		if !errors.Is(err, domain.ErrMalformedDocument) {
			t.Errorf("case %d: err = %v, want ErrMalformedDocument", i, err)
		}
	}
}

func TestExtractText_HonorsCancellation(t *testing.T) {
	data := buildMinimalPDF(t, 1, "Cancelled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := fitzadapter.NewAdapter().ExtractText(ctx, data)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestConcurrentExtraction_SeparateDocumentsPerGoroutine(t *testing.T) {
	data := buildMinimalPDF(t, 2, "Concurrent body")
	adapter := fitzadapter.NewAdapter()

	const goroutines = 16
	const iterations = 20

	errCh := make(chan error, goroutines)
	var wg sync.WaitGroup
	for w := 0; w < goroutines; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				text, pages, err := adapter.ExtractText(context.Background(), data)
				if err != nil {
					errCh <- fmt.Errorf("extract: %w", err)
					return
				}
				if pages != 2 || !strings.Contains(text, "Concurrent body") {
					errCh <- fmt.Errorf("unexpected result: pages=%d text=%q", pages, text)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestSoakExtraction_NoCHeapLeak(t *testing.T) {
	if _, err := os.Stat("/proc/self/statm"); err != nil {
		t.Skip("RSS measurement requires Linux /proc (statm unavailable)")
	}
	data := buildMinimalPDF(t, 1, "soak body")
	adapter := fitzadapter.NewAdapter()

	rss := func() int64 {
		raw, err := os.ReadFile("/proc/self/statm")
		if err != nil {
			t.Fatalf("read statm: %v", err)
		}
		var size, resident int64
		if _, err := fmt.Sscanf(string(raw), "%d %d", &size, &resident); err != nil {
			t.Fatalf("parse statm: %v", err)
		}
		return resident * int64(os.Getpagesize())
	}

	runtime.GC()
	baseline := rss()
	for i := 0; i < 100; i++ {
		if _, _, err := adapter.ExtractText(context.Background(), data); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
	runtime.GC()
	growth := rss() - baseline
	// Genetic MuPDF allocators can grow RSS a little; a large sustained growth
	// indicates C-heap memory not freed by Close.
	if growth > 20<<20 {
		t.Fatalf("RSS grew by %d bytes after 100 extractions, possible C-heap leak", growth)
	}
}
