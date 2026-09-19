package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the validated runtime configuration for the service.
type Config struct {
	Port            string
	MongoURI        string
	MongoDB         string
	MongoCollection string

	MaxUploadBytes    int64
	ExtractionTimeout time.Duration
	// Concurrency bounds simultaneous extractions; 0 falls back to NumCPU.
	Concurrency int
	ErrBaseURL  string
}

const (
	defaultPort              = "8080"
	defaultDB                = "pdf_extraction"
	defaultCollection        = "extractions"
	defaultMaxUploadBytes    = 25 * 1024 * 1024
	defaultExtractionTimeout = 30 * time.Second
	defaultErrBaseURL        = "https://errors.example.com"
)

// Load reads and validates the environment. It fails with an actionable
// message naming the offending variable.
func Load() (*Config, error) {
	cfg := &Config{
		Port:              getEnv("PORT", defaultPort),
		MongoURI:          os.Getenv("MONGODB_URI"),
		MongoDB:           getEnv("MONGODB_DB", defaultDB),
		MongoCollection:   getEnv("MONGODB_COLLECTION", defaultCollection),
		MaxUploadBytes:    defaultMaxUploadBytes,
		ExtractionTimeout: defaultExtractionTimeout,
		ErrBaseURL:        getEnv("ERR_BASE_URL", defaultErrBaseURL),
	}

	if cfg.MongoURI == "" {
		return nil, fmt.Errorf("MONGODB_URI is required")
	}

	if raw := os.Getenv("MAX_UPLOAD_BYTES"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("MAX_UPLOAD_BYTES must be a positive integer, got %q", raw)
		}
		cfg.MaxUploadBytes = v
	}

	if raw := os.Getenv("EXTRACTION_TIMEOUT"); raw != "" {
		v, err := time.ParseDuration(raw)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("EXTRACTION_TIMEOUT must be a positive duration (e.g. 30s), got %q", raw)
		}
		cfg.ExtractionTimeout = v
	}

	if raw := os.Getenv("CONCURRENCY"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("CONCURRENCY must be a positive integer (or unset for CPU count), got %q", raw)
		}
		cfg.Concurrency = v
	}

	if raw := os.Getenv("ERR_BASE_URL"); raw != "" {
		if !strings.HasPrefix(raw, "https://") && !strings.HasPrefix(raw, "http://") {
			return nil, fmt.Errorf("ERR_BASE_URL must be an http(s) URI, got %q", raw)
		}
		cfg.ErrBaseURL = raw
	}

	if _, err := strconv.Atoi(cfg.Port); err != nil {
		return nil, fmt.Errorf("PORT must be numeric, got %q", cfg.Port)
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
