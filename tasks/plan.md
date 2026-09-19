# Implementation Plan: PDF Extraction Microservice (Go)

## Overview

Build a containerized Go microservice that accepts PDF uploads via `multipart/form-data`, extracts text with `go-fitz` (CGo/MuPDF), logs every extraction (success or failure) to a shared MongoDB instance, and returns the text in a JSON envelope. Errors follow RFC 9457 Problem Details. Concurrency is bounded by a worker pool sized to CPU count; each job owns its own fitz `Document` (validated requirement: go-fitz supports concurrency only with a separate MuPDF context per goroutine).

Source of truth: `SPEC.md` (approved). Detailed tasks: `tasks/todo.md`.

## Architecture Decisions

- **Layered (N-tier) per SDD:** `domain/` (entities + ports, zero external imports) → `application/` (orchestration) → `infrastructure/{fitz,mongo}` (adapters) → `presentation/` (Gin handlers/router) → `cmd/api` (composition root).
- **Consumer-defined interfaces (ISP/DIP):** `application` depends on `domain.DocumentProcessor` + `domain.ExtractionRepository`. `presentation` defines its own minimal `Extractor` and `Pinger` interfaces satisfied by the use case and mongo repo (no concrete types leaked into handlers).
- **DTO ownership:** request/response data-transfer types live in `application`; `domain` holds only entities (`ExtractionRecord`, `ErrorDetails`) and ports. This keeps `domain` free of HTTP concerns.
- **Hash + timing in the use case:** SHA-256, duration, and text length computed in `application` (pure Go, mock-testable); `infrastructure` adapters never compute domain metadata.
- **Size limit enforcement:** `http.MaxBytesReader` wrapping the request body in a middleware; a typed `*http.MaxBytesError` maps to a `413` problem document. Prevents unbounded memory growth at the HTTP layer.
- **Concurrency:** buffered semaphore (default `CONCURRENCY`, falls back to `runtime.NumCPU()`). Use case acquires a slot per request. Handlers are thin; no per-request goroutine spawning.
- **Timeouts:** fixed `ReadHeaderTimeout` on the server (NOT `ReadTimeout`, which would break large uploads) + per-extraction `context.WithTimeout` using `EXTRACTION_TIMEOUT` (default 30s) created in the handler.
- **Mongo lifecycle:** client created once at startup (`MONGODB_URI`), fail-fast with clear error if unreachable, `Disconnect` on graceful shutdown. `Ping` feeds `/health`.
- **TDD per SDD:** tests authored before implementation (red → green). Table-driven for application/presentation with hand-rolled stubs; integration tests for fitz (real PDF fixture) and mongo (real instance, skipped when `MONGODB_URI` unset).

## Environment (verified)

- **Docker:** available (Docker Desktop 4.74.0, Engine 29.4.3, Compose v5.1.3). CGo builds/tests for go-fitz can run via a `golang:1.27.1-bookworm` image or locally once Go 1.27.1 + gcc are installed.
- **Go CLI:** not on PATH on the dev Windows machine → `go build` / `go test -race` run inside Docker (`golang:1.27.1-bookworm`) or on CI.
- **MongoDB:** live at `mongo:8.0`, container `mongo` on Docker network `test_network` (alias `mongo`), port 27017 published to host. Credentials verified (`mongodb://admin:password@mongo:27017` → `{"ok":1}`).
  - Local `go run`/integration tests on Windows → use `mongodb://admin:password@localhost:27017`.
  - Containerized service → join `test_network`, keep hostname `mongo`.

## Task List

### Phase 1: Foundation
- Task 1: Module init + domain layer → **S**
- Task 2: `ExtractTextUseCase` + record builder (TDD) → **M**
- Task 3: fitz `DocumentProcessor` adapter + integration/soak tests → **M**

**Checkpoint:** `go build ./...` and `go test -race ./...` green on Linux; soak shows flat RSS.

### Phase 2: End-to-end path
- Task 4: Mongo `ExtractionRepository` (integration-gated on real instance) → **M**
- Task 5: Presentation: handlers, router, RFC 9457, multipart + size limit (TDD) → **S/M**
- Task 6: `cmd/api` wiring, config, graceful shutdown, health endpoint → **M**

**Checkpoint:** end-to-end manual flow (upload → 200 envelope; oversize → 413; malformed → 422; health 200/503) against a real Mongo.

### Phase 3: Ship
- Task 7: Multi-stage Dockerfile + `.dockerignore` + build/run smoke → **S**

**Checkpoint:** `docker build` succeeds; container starts and `/health` responds.

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Go CLI not installed on dev Windows machine | Med — cannot run `go build`/`go test` natively | Run via `golang:1.27.1-bookworm` Docker image or CI; optionally install Go 1.27.1 locally |
| go-fitz CGo build on Windows (gcc required) | Med | Keep fitz build/test verification inside Docker/Linux (Task 7); fitz integration test skips gracefully if toolchain/setup missing |
| MuPDF bundle build in Docker needs system deps (gcc, make, pkg-config, wget, libjpeg/openssl/freetype/harfbuzz/zlib dev) | Med | Pin packages in builder stage; verify via `docker build` (Task 7) |
| go-fitz concurrency SIGSEGV history (#142) | High | Separate `Document` per job (validated requirement); pin go-fitz v1.24.15+; `-race` + concurrent extraction test + RSS soak |
| Mongo unreachable at startup | Med | Fail-fast connect with timeout and clear error; `Ping`-driven health |
| `http.MaxBytesReader` error → 413 mapping | Low | Typed `*http.MaxBytesError` detection; table-tested in Task 5 |
| Gin + context cancellation leaking goroutines | Med | No raw goroutines in handlers; semaphore released via `defer`; `defer doc.Close()` always after acquisition |

## Open Questions

Resolved-by-default from the approved spec (changeable anytime):
- Go module path: `microservicio-go` (rename on first push).
- RFC 9457 `type` base URI: configurable `ERR_BASE_URL` (default `https://example.com/errors`).
- Extraction timeout default: 30s (`EXTRACTION_TIMEOUT`).
- Mongo defaults: DB `pdf_extraction`, collection `extractions` (`MONGODB_DB`, `MONGODB_COLLECTION`).