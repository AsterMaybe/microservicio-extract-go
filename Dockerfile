# syntax=docker/dockerfile:1

# Use Debian-based image for glibc compatibility with go-fitz's pre-built MuPDF libraries
FROM golang:1.27-trixie AS build

# Install build dependencies for CGO (gcc, libffi-dev for go-fitz)
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libffi-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Enable CGO for go-fitz (MuPDF) which requires libffi
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/bin/api ./cmd/api

# Use Distroless runtime (glibc compatible, no shell, no package manager)
# base-nossl-debian13 has glibc but no OpenSSL (not needed by our binary)
FROM gcr.io/distroless/base-nossl-debian13:nonroot

COPY --from=build /out/bin/api /microservicio

EXPOSE 8080
ENTRYPOINT ["/microservicio"]