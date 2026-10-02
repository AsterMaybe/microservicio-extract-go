package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the validated runtime configuration for the PDF extraction service.
// All fields are populated from environment variables with sensible defaults.
// Required: MONGODB_URI, REDIS_URL.
type Config struct {
	Port            string
	MongoURI        string
	MongoDB         string
	MongoCollection string
	RedisURL        string

	// MaxUploadBytes caps the accepted multipart upload size, expressed in
	// bytes internally. The MAX_UPLOAD_MB env var configures it in megabytes.
	MaxUploadBytes    int64
	ExtractionTimeout time.Duration
	// Concurrency bounds simultaneous extractions; 0 falls back to NumCPU.
	Concurrency int
	// QueueSize is the waiting buffer: how many requests may hold a place
	// while they wait for a free extraction slot. Once it is full, further
	// requests are shed immediately with 429 instead of queueing.
	QueueSize int
	// MaxInFlight bounds simultaneous uploads being buffered by HTTP handlers
	// *before* extraction. Saturation returns an RFC 9457 503 instead of
	// unbounded in-memory buffering.
	MaxInFlight int
	// AdmissionTimeout bounds how long a queued request waits for its slot
	// before it is shed with 429. Shorter than ExtractionTimeout on purpose.
	AdmissionTimeout time.Duration
	// CacheTimeout bounds a single Redis round trip, so a stalled cache costs
	// a timeout rather than a request that hangs until the client gives up.
	CacheTimeout time.Duration
	// CacheTTL bounds the lifetime of a cached extraction result.
	CacheTTL   time.Duration
	ErrBaseURL string
}

const (
	defaultPort              = "8080"
	defaultDB                = "pdf_extraction"
	defaultCollection        = "extractions"
	defaultMaxUploadBytes    = 25 * 1024 * 1024
	defaultExtractionTimeout = 30 * time.Second
	defaultMaxInFlight       = 32
	// defaultQueueSize is the default waiting buffer. Sized well above
	// Concurrency so a burst queues briefly instead of being shed, while still
	// bounding the backlog so latency and memory stay predictable.
	defaultQueueSize        = 200
	defaultAdmissionTimeout = 2 * time.Second
	defaultCacheTimeout     = 2 * time.Second
	defaultCacheTTL         = 24 * time.Hour
	defaultErrBaseURL       = "https://errors.example.com"
)

// Load reads configuration from environment variables, validates required fields
// and cross-field constraints (e.g., ADMISSION_TIMEOUT < EXTRACTION_TIMEOUT).
// Returns a fully initialized Config or an actionable error naming the offending variable.
func Load() (*Config, error) {
	cfg := &Config{
		Port:              getEnv("PORT", defaultPort),
		MongoURI:          os.Getenv("MONGODB_URI"),
		MongoDB:           getEnv("MONGODB_DB", defaultDB),
		MongoCollection:   getEnv("MONGODB_COLLECTION", defaultCollection),
		RedisURL:          os.Getenv("REDIS_URL"),
		MaxUploadBytes:    defaultMaxUploadBytes,
		ExtractionTimeout: defaultExtractionTimeout,
		QueueSize:         defaultQueueSize,
		MaxInFlight:       defaultMaxInFlight,
		AdmissionTimeout:  defaultAdmissionTimeout,
		CacheTimeout:      defaultCacheTimeout,
		CacheTTL:          defaultCacheTTL,
		ErrBaseURL:        getEnv("ERR_BASE_URL", defaultErrBaseURL),
	}

	if cfg.MongoURI == "" {
		return nil, fmt.Errorf("MONGODB_URI is required")
	}

	if cfg.RedisURL == "" {
		return nil, fmt.Errorf("REDIS_URL is required")
	}

	if raw := os.Getenv("MAX_UPLOAD_MB"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("MAX_UPLOAD_MB must be a positive integer of megabytes (e.g. 25), got %q", raw)
		}
		cfg.MaxUploadBytes = v * 1024 * 1024
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
		// 0 is a valid sentinel meaning "one slot per CPU" (the use case
		// resolves it to NumCPU), so only negatives are rejected here.
		if err != nil || v < 0 {
			return nil, fmt.Errorf("CONCURRENCY must be a non-negative integer (0 or unset for CPU count), got %q", raw)
		}
		cfg.Concurrency = v
	}

	if raw := os.Getenv("QUEUE_SIZE"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("QUEUE_SIZE must be a positive integer: it is how many requests may wait for an extraction slot before the service sheds (e.g. 200), got %q", raw)
		}
		cfg.QueueSize = v
	}

	if raw := os.Getenv("REDIS_TIMEOUT"); raw != "" {
		v, err := time.ParseDuration(raw)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("REDIS_TIMEOUT must be a positive duration (e.g. 2s), got %q", raw)
		}
		cfg.CacheTimeout = v
	}

	if raw := os.Getenv("MAX_IN_FLIGHT"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("MAX_IN_FLIGHT must be a positive integer (or unset for the default), got %q", raw)
		}
		cfg.MaxInFlight = v
	}

	if raw := os.Getenv("ADMISSION_TIMEOUT"); raw != "" {
		v, err := time.ParseDuration(raw)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("ADMISSION_TIMEOUT must be a positive duration (e.g. 2s), got %q", raw)
		}
		if v >= cfg.ExtractionTimeout {
			return nil, fmt.Errorf("ADMISSION_TIMEOUT (%s) must be shorter than EXTRACTION_TIMEOUT (%s), got %q", v, cfg.ExtractionTimeout, raw)
		}
		cfg.AdmissionTimeout = v
	}

	if raw := os.Getenv("CACHE_TTL"); raw != "" {
		v, err := time.ParseDuration(raw)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("CACHE_TTL must be a positive duration (e.g. 24h), got %q", raw)
		}
		cfg.CacheTTL = v
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
