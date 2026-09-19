# Tasks: PDF Extraction Microservice (Go)

> Plan: `tasks/plan.md` • Spec: `SPEC.md`. Docker + Mongo (`mongodb://admin:password@mongo:27017` on `test_network`; `localhost:27017` from host) verified. Go CLI absent on Windows — CGo build/test runs via `golang:1.27.1-bookworm` or CI.

## Task 1: Module init + domain layer

**Description:** Initialize the Go module (Go 1.27.1), the `domain/` foundations and the layer directory skeleton. `domain` holds zero external imports: the `ExtractionRecord` entity, `ErrorDetails`, status constants, and the `DocumentProcessor` / `ExtractionRepository` ports.

**Acceptance criteria:**
- [ ] `go.mod` created with `go 1.27.1`, module `microservicio-go`; empty `application/`, `infrastructure/fitz`, `infrastructure/mongo`, `presentation/`, `config/`, `cmd/api` dirs exist
- [ ] `domain.ExtractionRecord` matches the approved schema (filename, mime_type, file_size_bytes, page_count, text_length, duration_ms, sha256, status, error{type,message,detail}, created_at, updated_at)
- [ ] `domain.DocumentProcessor` defines only `ExtractText(ctx, data) (text string, pages int, err error)`; `domain.ExtractionRepository` defines `Save(ctx, record) error`
- [ ] `grep` over `domain/` shows zero imports outside stdlib

**Verification:**
- [ ] `go vet ./domain/... && go test ./domain/...` green (struct/constant tests)
- [ ] `go list -deps ./domain/...` shows no third-party packages

**Dependencies:** None

**Files likely touched:**
- `go.mod`
- `domain/extraction_record.go`, `domain/error_details.go`, `domain/ports.go`
- `domain/extraction_record_test.go` (+ `_test.go` per file, written first per TDD)

**Estimated scope:** Small (1-3 files)

---

## Task 2: ExtractTextUseCase + record builder (TDD)

**Description:** Implement the orchestration layer. `application.ExtractTextUseCase` validates input, computes SHA-256 / duration / text length, acquires a worker-pool slot (semaphore), calls the processor, builds an `ExtractionRecord` (success or error), persists it via the repository, and returns an `ExtractionOutput`. Define `application` DTOs (`ExtractInput`, `ExtractOutput`) and the consumer interface `Extractor` the presentation layer will later rely on.

**Acceptance criteria:**
- [ ] On success: record saved with `status: success` and full metadata; output has extracted text + page count
- [ ] On processor error: record saved with `status: error` and populated `error{type,message,detail}`; error propagates to caller
- [ ] Repository failure still returns an error to the caller (extraction result not silently dropped)
- [ ] Semaphore (buffered chan, default `CONCURRENCY` or `runtime.NumCPU()`) bounds concurrent extractions; context cancellation during processing returns a ctx error and a `cancelled` record
- [ ] All unit tests are table-driven with hand-rolled stub `DocumentProcessor` + `ExtractionRepository` (no mocking dependency)

**Verification:**
- [ ] `go test -race ./application/...` green
- [ ] `go test -cover ./application/...` shows ≥ 80% coverage

**Dependencies:** Task 1

**Files likely touched:**
- `application/extract_text.go`, `application/dto.go`
- `application/extract_text_test.go` (written first), `application/dto_test.go`

**Estimated scope:** Medium (3-4 files)

---

## Task 3: fitz DocumentProcessor adapter + integration/soak tests

**Description:** Implement `infrastructure/fitz.Adapter` satisfying `domain.DocumentProcessor` using `go-fitz` (CGo) with `fitz.NewFromMemory` and `defer doc.Close()`. Integration test extracts text from a real (committed) minimal PDF fixture; concurrent test spawns N tasks each with their own `Document` and runs clean under `-race`; soak loop (100 runs) asserts flat RSS.

**Acceptance criteria:**
- [ ] `NewFromMemory` + page loop + `defer doc.Close()`; every C-allocated resource freed (no path skips Close)
- [ ] Concurrent extraction (each goroutine owns its `Document`) runs clean: no SIGSEGV, `-race` clean
- [ ] Soak test: 100 sequential extractions, RSS growth within noise (e.g. < 5% of baseline)
- [ ] Malformed bytes return a typed error (no panic); password-protected/empty PDFs return identifiable errors
- [ ] Test fixture (minimal valid PDF) committed under `infrastructure/fitz/testdata/`
- [ ] Integration/concurrency tests skip cleanly when the CGo build environment is unavailable (gated), so CI is not blocked on Windows

**Verification:**
- [ ] Linux/CI: `go test -race -count=1 ./infrastructure/fitz/... && go test -run TestSoak -v ./infrastructure/fitz/...`
- [ ] `go vet ./infrastructure/fitz/...` green

**Dependencies:** Task 1

**Files likely touched:**
- `infrastructure/fitz/adapter.go`
- `infrastructure/fitz/adapter_test.go`, `infrastructure/fitz/concurrency_test.go`, `infrastructure/fitz/soak_test.go` (written first)
- `infrastructure/fitz/testdata/minimal.pdf`

**Estimated scope:** Medium (3-4 files)

---

## Checkpoint: After Tasks 1-3

- [ ] `go build ./...` and `go test -race ./...` green on Linux/CI
- [ ] Soak shows flat RSS (no C-heap leak)
- [ ] `domain/` verified zero-dependency (`go list -deps`)
- [ ] Human review before proceeding

---

## Task 4: Mongo ExtractionRepository

**Description:** Implement `infrastructure/mongo.Repository` (client created once from `MONGODB_URI`, DB `MONGODB_DB`, collection `MONGODB_COLLECTION`), `Save(ctx, record)` via `InsertOne`, index on `created_at`, `Ping(ctx)` for health, and `Disconnect` for graceful shutdown. Integration tests run only when `MONGODB_URI` is set (skip otherwise).

**Acceptance criteria:**
- [ ] `NewRepository` fails fast with a clear, non-secret error when the instance is unreachable
- [ ] `Save` persists the full record; round-trip read matches the uploaded record
- [ ] `Ping` returns nil when reachable, non-nil otherwise
- [ ] Index on `created_at` ensured at startup (idempotent)
- [ ] No internal panic on driver errors; context timeouts honored
- [ ] Integration tests gated: `t.Skip` when `MONGODB_URI` unset

**Verification:**
- [ ] With `MONGODB_URI` set: `go test -race ./infrastructure/mongo/...` green
- [ ] Without it: tests skip cleanly

**Dependencies:** Task 1

**Files likely touched:**
- `infrastructure/mongo/repository.go`, `infrastructure/mongo/client.go`
- `infrastructure/mongo/repository_test.go` (written first)

**Estimated scope:** Medium (3 files)

---

## Task 5: Presentation — handlers, router, RFC 9457 (TDD)

**Description:** Gin layer. `POST /api/v1/extract`: multipart parse (field `file`), 25 MB `http.MaxBytesReader` limit on body, per-request `EXTRACTION_TIMEOUT` context, call the presentation-side `Extractor` interface, return the JSON envelope `{filename, extension, mime_type, text}`. `GET /api/v1/health`: `Pinger.Ping` → `200 {status:ok}` or `503 problem`. All errors as RFC 9457 problem documents (`type`, `title`, `status`, `detail`, `instance`) via a typed problem registry (`too-large` 413, `invalid-file` 400, `malformed-pdf` 422, `timeout` 504, `server-error` 500). `type` uses `ERR_BASE_URL`. Internal stack traces never leak.

**Acceptance criteria:**
- [ ] Valid PDF upload → `200` with all envelope fields; unknown file extension still extracts (mime from content, not client)
- [ ] Body/file exceeding 25 MB → `413` problem with all 5 fields, no partial state persisted
- [ ] Non-PDF / nonexistent file field → `400`/`422` problem, no panic
- [ ] Processor timeout / ctx deadline → `504` problem
- [ ] Unexpected processor error → `500` problem; `detail` contains no stack traces or internal paths
- [ ] `/health` returns `200`/`503` per `Ping` result
- [ ] Table-driven handler tests via `net/http/httptest` with stub `Extractor` + `Pinger`; ≥ 80% coverage

**Verification:**
- [ ] `go test -race -cover ./presentation/...` green, ≥ 80% coverage
- [ ] Manual: `curl -F file=@x.pdf localhost:8080/api/v1/extract` and oversized/malformed cases

**Dependencies:** Task 2

**Files likely touched:**
- `presentation/router.go`, `presentation/handler.go`, `presentation/problem.go`
- `presentation/handler_test.go`, `presentation/problem_test.go` (written first)

**Estimated scope:** Small/Medium (5 files)

---

## Task 6: cmd/api wiring, config, graceful shutdown

**Description:** Composition root. `config` package reads/validates env (`PORT`, `MONGODB_URI`, `MONGODB_DB`, `MONGODB_COLLECTION`, `MAX_UPLOAD_BYTES`, `EXTRACTION_TIMEOUT`, `CONCURRENCY`, `ERR_BASE_URL`). `main.go` wires fitz adapter → use case → router, starts the server (fixed `ReadHeaderTimeout`, no blocking `ReadTimeout`), and shuts down gracefully on SIGINT/SIGTERM (server `Shutdown` + mongo `Disconnect` with bounded ctx).

**Acceptance criteria:**
- [ ] Config validates required vars, applies documented defaults, and errors with a clear message on bad values
- [ ] `MONGODB_URI` absent → startup aborts with actionable message
- [ ] Graceful shutdown drains in-flight requests and disconnects Mongo before exit
- [ ] `config` parsing unit-tested (table-driven)

**Verification:**
- [ ] `go vet ./... && go test -race ./config/...` green
- [ ] Manual: start with real Mongo; stop mid-request; observe clean exit

**Dependencies:** Tasks 3, 4, 5

**Files likely touched:**
- `cmd/api/main.go`
- `config/config.go`, `config/config_test.go` (written first)

**Estimated scope:** Medium (3 files)

---

## Checkpoint: After Tasks 4-6

- [ ] End-to-end flow verified against a real Mongo: upload → 200 + envelope
- [ ] Oversize → 413, malformed → 422, health → 200/503 — all as RFC 9457 problem docs
- [ ] Graceful shutdown exits cleanly
- [ ] Human review before proceeding

---

## Task 7: Multi-stage Dockerfile + .dockerignore + run smoke

**Description:** Builder `golang:1.27.1-bookworm` with CGo prerequisites (build-essential, make, pkg-config, wget, and MuPDF build deps: libjpeg-dev, libfreetype6-dev, libharfbuzz-dev, zlib1g-dev, libssl-dev), `CGO_ENABLED=1 go build -o /app/microservicio-go ./cmd/api`. Runner `debian:bookworm-slim` with CA certs and a non-root user, copying only the binary. `.dockerignore` excludes `.git`, `bin/`, logs, `.env`. Smoke: container starts and `/health` responds.

**Acceptance criteria:**
- [ ] `docker build .` produces a working image (no Alpine — musl conflicts with MuPDF)
- [ ] Runs as non-root; only essential runtime packages present
- [ ] Container starts and `GET /health` responds (gated on a reachable Mongo for `200`, but `503` with problem body still proves HTTP up)
- [ ] No `.env`/secrets baked into any layer (`docker history` spot-check)

**Verification:**
- [ ] `docker build -t microservicio-go . && docker run -p 8080:8080 microservicio-go` → `curl localhost:8080/api/v1/health`
- [ ] Spot-check layers: `docker history --no-trunc microservicio-go | Select-String -Pattern 'env|KEY|secret'` empty

**Dependencies:** Tasks 3, 6

**Files likely touched:**
- `Dockerfile`
- `.dockerignore`

**Estimated scope:** Small (2 files)

---

## Checkpoint: Complete

- [ ] All [`SPEC.md` Success Criteria](../SPEC.md) satisfied
- [ ] `go build ./...`, `go test -race ./...`, `go vet ./...`, `golangci-lint run ./...` green on CI
- [ ] Docker image builds, runs non-root, health responds
- [ ] Final human review + commit