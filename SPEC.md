# Spec: PDF Extraction Microservice (Go)

## Objective

Build a high-throughput, highly concurrent PDF text extraction microservice (in Go). The service accepts binary PDF uploads via `multipart/form-data`, extracts the text with a native C-library wrapper (`go-fitz`, CGo bindings for MuPDF) for maximum performance, logs extraction metadata to MongoDB, and returns the extracted text.

**User:** Internal consumers (the monolith and other internal services) that need the text content of PDF files without a heavyweight extraction stack.

**Success looks like:** A containerized service that reliably extracts text from many concurrent PDF uploads without memory leaks or crashes, records every extraction (success and failure) in the shared MongoDB, and communicates all failures via RFC 9457 Problem Details.

## Tech Stack

| Component | Choice | Version |
|---|---|---|
| Language | Go | 1.27.1 |
| Web framework | `github.com/gin-gonic/gin` | latest stable, pinned in `go.mod` |
| PDF engine | `github.com/gen2brain/go-fitz` (CGo/MuPDF) | v1.24.15 (bundled MuPDF 1.24.x) |
| Driver | `go.mongodb.org/mongo-driver/mongo` | latest stable v2/v1 line, pinned at implementation |
| CI/lint | `golangci-lint` | latest stable |
| Container | Docker multi-stage | `golang:1.27.1-bookworm` → `debian:bookworm-slim` |

**Concurrency note (validated):** go-fitz supports concurrent extraction **only when each goroutine owns a distinct `Document`** (its own MuPDF context). Concurrency on a single `Document`, including racing with `Close()`, is unsupported and can SIGSEGV. The worker pool must therefore open/close a document per job and never share one across goroutines.

## Commands

```sh
# Build (CGo must be enabled for go-fitz)
CGO_ENABLED=1 go build ./...
go build -o bin/microservicio-go ./cmd/api

# Test (TDD: table-driven unit tests)
go test ./...
go test -race ./...
go test -cover ./...

# Lint + format
golangci-lint run ./...
gofmt -l .
go vet ./...

# Dev (requires MONGODB_URI)
MONGODB_URI=mongodb://localhost:27017 go run ./cmd/api

# Docker
docker build -t microservicio-go .
```

## Project Structure

```
cmd/api/           → Entrypoint: config loading, wiring, HTTP server, graceful shutdown
domain/            → Core entities (ExtractionRecord, ErrorDetails) and interfaces
                     (DocumentProcessor, ExtractionRepository). ZERO external imports.
application/       → Use cases (ExtractTextUseCase). Orchestrates processor + repository.
infrastructure/    → Concrete implementations of domain interfaces:
                     mongo/ (ExtractionRepository), fitz/ (DocumentProcessor adapter)
presentation/      → Gin handlers, router, middleware (multipart parsing, RFC 9457 errors)
config/            → Environment-based configuration (env parsing, validation)
docs/              → Documentation (this spec, ADRs)
```

## Code Style

Constructor injection, `context.Context` propagated through every layer, small strictly-typed functions, exported interfaces in domain, unexported implementations.

```go
// domain/document_processor.go — zero external dependencies
package domain

import "context"

type DocumentProcessor interface {
    // ExtractText returns the full text of the document plus page count.
    ExtractText(ctx context.Context, data []byte) (string, int, error)
}
```

```go
// application/extract_text.go — orchestrates, depends only on domain interfaces
package application

import (
    "context"
    "time"

    "microservicio-go/domain"
)

type ExtractTextUseCase struct {
    processor  domain.DocumentProcessor
    repository domain.ExtractionRepository
}

func NewExtractTextUseCase(p domain.DocumentProcessor, r domain.ExtractionRepository) *ExtractTextUseCase {
    return &ExtractTextUseCase{processor: p, repository: r}
}
```

```go
// infrastructure/fitz/adapter.go — owns its C-allocated memory, always defers Close
func (a *Adapter) ExtractText(ctx context.Context, data []byte) (string, int, error) {
    doc, err := fitz.NewFromMemory(data)
    if err != nil {
        return "", 0, err
    }
    defer doc.Close()
    // ... iterate pages, accumulate text with a strings.Builder
}
```

Conventions: package names lowercase (no underscores), interfaces declared at the consumer boundary, `defer` immediately after resource acquisition, errors wrapped with context (`fmt.Errorf("open document: %w", err)`), RFC 9457 problem type as a typed constant in `presentation/`.

## Design Principles & Code Quality

Strict adherence to **SOLID principles** and **Clean Code** is required:
- **Single Responsibility (SRP):** Handlers only parse HTTP/multipart inputs and format RFC 9457 responses. Use cases only orchestrate domain logic. Infrastructure adapters only talk to external systems (MuPDF CGo, Mongo driver).
- **Open/Closed & Dependency Inversion (OCP / DIP):** Higher layers (`application`) depend strictly on domain interfaces (`DocumentProcessor`, `ExtractionRepository`). Inject all dependencies via `New...` constructors. No concrete implementations instantiated inside use cases.
- **Interface Segregation (ISP):** Keep domain interfaces lean and minimal (e.g., `DocumentProcessor` defines only the exact extraction method needed by the application).
- **Clean Code Practices:**
  - Zero package-level global variables (state must live on injected structs).
  - Explicit error propagation wrapping with contextual strings (`fmt.Errorf("step description: %w", err)`).
  - Explicit resource deallocation via `defer` immediately following acquisition.
  - Context propagation: every blocking, network, or computation-heavy call must accept and respect `ctx context.Context`.

## Testing Strategy

- **Framework:** standard library `testing` + `net/http/httptest`. Mocks for `DocumentProcessor` and `ExtractionRepository` are hand-rolled stubs in the test files (no mocking dependency). Table-driven tests required for application and presentation layers.
- **TDD per SDD:** test files written before implementation files (red → green → refactor).
- **Coverage:** `go test -cover ./...` — application and presentation layers ≥ 80%; overall goal ≥ 70%. Infrastructure (fitz, mongo) covered by integration tests against real services, not faked.
- **Levels:**
  - *Unit (table-driven):* use case orchestration (success/error paths, record persistence), handlers (multipart parsing, size limit, error mapping, response shape).
  - *Integration:* MongoDB repository against `mongoDB://localhost:27017` (or the shared instance); real-PDF extraction against small fixtures.
  - *Concurrency/race:* `go test -race ./...` must pass; a soak loop (e.g., 100 sequential extractions) must show flat RSS (no C-heap leak).

## Boundaries

**Always do:**
- Run `gofmt`, `go vet`, `golangci-lint`, `go test ./...` before committing.
- Keep `domain/` free of any external import (zero-dependency core).
- `defer doc.Close()` / free every C-allocated resource in the fitz adapter.
- Propagate `context.Context` through every layer; honor cancellation and timeouts.
- Return errors strictly as RFC 9457 Problem Details; never leak internal stack traces.
- Persist an `ExtractionRecord` for every request, success or failure.

**Ask first:**
- MongoDB schema changes or new collections/databases.
- Adding any dependency (test frameworks, mocks, routers) beyond the pinned stack.
- Changing the Docker base images or the auth/network boundary of the service.
- Changing public endpoint paths or the accepted response/envelope shape.

**Never do:**
- Commit secrets or `.env` files.
- Remove or skip failing tests without approval.
- Share a fitz `Document` across goroutines.
- Return internal error details / stack traces to clients.

## Success Criteria

- [ ] `POST /api/v1/extract` accepts a `multipart/form-data` upload (field `file`), extracts text, and returns `200` with the JSON envelope `{"filename", "extension", "mime_type", "text"}`.
- [ ] Uploads exceeding the 25 MB limit are rejected with `413` and a valid RFC 9457 Problem Details body (all 5 fields present).
- [ ] Non-PDF or malformed uploads are rejected with `400`/`422` Problem Details (no panic, no internal details leaked).
- [ ] `GET /api/v1/health` returns `200 {"status":"ok"}` when MongoDB is reachable, `503` otherwise.
- [ ] Every request (success or failure) produces an `ExtractionRecord` in MongoDB with the agreed schema, including `status` and per-status fields.
- [ ] Concurrent uploads are bounded by a worker pool sized to CPU count; extraction saturation queues safely with no OOM, no SIGSEGV, no data race (`go test -race ./...` clean).
- [ ] A 100-iteration soak loop shows flat process RSS (within noise) — no C-heap leak.
- [ ] Application and presentation layers ≥ 80% test coverage.
- [ ] `CGO_ENABLED=1 go build ./...` succeeds; the multi-stage Docker image builds and runs on Debian bookworm.

## Open Questions

1. **Go module path** — repo has no remote yet; using local module `microservicio-go`. Set the real path on first push.
2. **Error `type` URI base** — RFC 9457 `type` must be a URI. Using a configurable base `ERR_BASE_URL` (default example.com/errors). Confirm or set.
3. **Extraction timeout default** — proposal: 30s per document (configurable via `EXTRACTION_TIMEOUT`). Large PDFs may need more; confirm.
4. **Mongo defaults** — proposal: DB `pdf_extraction`, collection `extractions` (override via `MONGODB_DB` / `MONGODB_COLLECTION`). Confirm.