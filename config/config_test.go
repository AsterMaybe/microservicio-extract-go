package config_test

import (
	"strings"
	"testing"
	"time"

	"microservicio-go/config"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"PORT", "MONGODB_URI", "MONGODB_DB", "MONGODB_COLLECTION", "REDIS_URL", "MAX_UPLOAD_MB", "EXTRACTION_TIMEOUT", "CONCURRENCY", "QUEUE_SIZE", "MAX_IN_FLIGHT", "ADMISSION_TIMEOUT", "REDIS_TIMEOUT", "CACHE_TTL", "ERR_BASE_URL"} {
		t.Setenv(k, "")
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("MONGODB_URI", "mongodb://admin:password@mongo:27017")
	t.Setenv("REDIS_URL", "redis://localhost:6379")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.MongoURI != "mongodb://admin:password@mongo:27017" {
		t.Errorf("MongoURI wrong: %q", cfg.MongoURI)
	}
	if cfg.MongoDB != "pdf_extraction" || cfg.MongoCollection != "extractions" {
		t.Errorf("Mongo naming defaults wrong: %q/%q", cfg.MongoDB, cfg.MongoCollection)
	}
	if cfg.RedisURL != "redis://localhost:6379" {
		t.Errorf("RedisURL = %q, want redis://localhost:6379", cfg.RedisURL)
	}
	if cfg.MaxUploadBytes != 25*1024*1024 {
		t.Errorf("MaxUploadBytes = %d, want %d", cfg.MaxUploadBytes, 25*1024*1024)
	}
	if cfg.ExtractionTimeout != 30*time.Second {
		t.Errorf("ExtractionTimeout = %v, want 30s", cfg.ExtractionTimeout)
	}
	if cfg.Concurrency != 0 { // 0 means "fall back to NumCPU in the use case"
		t.Errorf("Concurrency = %d, want default 0", cfg.Concurrency)
	}
	if cfg.QueueSize != 200 {
		t.Errorf("QueueSize = %d, want default 200", cfg.QueueSize)
	}
	if cfg.MaxInFlight != 32 {
		t.Errorf("MaxInFlight = %d, want default 32", cfg.MaxInFlight)
	}
	if cfg.AdmissionTimeout != 2*time.Second {
		t.Errorf("AdmissionTimeout = %v, want default 2s", cfg.AdmissionTimeout)
	}
	if cfg.CacheTimeout != 2*time.Second {
		t.Errorf("CacheTimeout = %v, want default 2s", cfg.CacheTimeout)
	}
	if cfg.CacheTTL != 24*time.Hour {
		t.Errorf("CacheTTL = %v, want default 24h", cfg.CacheTTL)
	}
	if cfg.ErrBaseURL != "https://errors.example.com" {
		t.Errorf("ErrBaseURL = %q, want default", cfg.ErrBaseURL)
	}
}

func TestLoad_RespectsOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("MONGODB_URI", "mongodb://u:p@h:27017")
	t.Setenv("REDIS_URL", "redis://redis:6379")
	t.Setenv("MONGODB_DB", "custom_db")
	t.Setenv("MONGODB_COLLECTION", "custom_col")
	t.Setenv("PORT", "9090")
	t.Setenv("MAX_UPLOAD_MB", "2")
	t.Setenv("EXTRACTION_TIMEOUT", "5s")
	t.Setenv("CONCURRENCY", "4")
	t.Setenv("QUEUE_SIZE", "500")
	t.Setenv("MAX_IN_FLIGHT", "16")
	t.Setenv("REDIS_TIMEOUT", "750ms")
	t.Setenv("ERR_BASE_URL", "https://errors.acme.dev")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "9090" || cfg.MongoDB != "custom_db" || cfg.MongoCollection != "custom_col" || cfg.RedisURL != "redis://redis:6379" || cfg.MaxUploadBytes != 2*1024*1024 || cfg.ExtractionTimeout != 5*time.Second || cfg.Concurrency != 4 || cfg.MaxInFlight != 16 || cfg.ErrBaseURL != "https://errors.acme.dev" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if cfg.QueueSize != 500 {
		t.Errorf("QueueSize = %d, want 500", cfg.QueueSize)
	}
	if cfg.CacheTimeout != 750*time.Millisecond {
		t.Errorf("CacheTimeout = %v, want 750ms", cfg.CacheTimeout)
	}
}

func TestLoad_AdmissionTimeoutRejectsLongerThanExtraction(t *testing.T) {
	clearEnv(t)
	t.Setenv("MONGODB_URI", "mongodb://admin:password@mongo:27017")
	t.Setenv("REDIS_URL", "redis://redis:6379")
	t.Setenv("ADMISSION_TIMEOUT", "60s")
	t.Setenv("EXTRACTION_TIMEOUT", "30s")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected an error when ADMISSION_TIMEOUT >= EXTRACTION_TIMEOUT")
	} else if !strings.Contains(err.Error(), "ADMISSION_TIMEOUT") {
		t.Errorf("error should mention ADMISSION_TIMEOUT, got: %v", err)
	}
}

// CONCURRENCY=0 is the documented "one slot per CPU" sentinel and is what the
// shipped .env sets, so Load must accept it (negatives stay invalid).
func TestLoad_ConcurrencyZeroIsValidSentinel(t *testing.T) {
	clearEnv(t)
	t.Setenv("MONGODB_URI", "mongodb://admin:password@mongo:27017")
	t.Setenv("REDIS_URL", "redis://redis:6379")
	t.Setenv("CONCURRENCY", "0")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load rejected CONCURRENCY=0: %v", err)
	}
	if cfg.Concurrency != 0 {
		t.Errorf("Concurrency = %d, want 0 (resolved to NumCPU by the use case)", cfg.Concurrency)
	}
}

// TestLoad_ComposeDefaultsAreLoadable pins the env that `docker compose config`
// resolves for the api service in microservicio-go/docker-compose.yml. A default
// that Load rejects only surfaces at container boot, so guard it here.
func TestLoad_ComposeDefaultsAreLoadable(t *testing.T) {
	clearEnv(t)
	for k, v := range map[string]string{
		"MONGODB_URI":        "mongodb://admin:password@mongo:27017",
		"MONGODB_DB":         "pdf_extraction",
		"MONGODB_COLLECTION": "extractions",
		"REDIS_URL":          "redis://redis:6379",
		"CACHE_TTL":          "24h",
		"GIN_MODE":           "release",
		"MAX_UPLOAD_MB":      "25",
		"MAX_IN_FLIGHT":      "32",
		"CONCURRENCY":        "0",
		"QUEUE_SIZE":         "200",
		"ADMISSION_TIMEOUT":  "2s",
		"REDIS_TIMEOUT":      "2s",
		"EXTRACTION_TIMEOUT": "30s",
	} {
		t.Setenv(k, v)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("the compose defaults must load cleanly: %v", err)
	}
	if cfg.Concurrency != 0 || cfg.QueueSize != 200 || cfg.MaxInFlight != 32 || cfg.AdmissionTimeout != 2*time.Second {
		t.Errorf("unexpected resolved config: %+v", cfg)
	}
}

func TestLoad_MissingMongoURI(t *testing.T) {
	clearEnv(t)
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when MONGODB_URI is missing")
	}
	if !strings.Contains(err.Error(), "MONGODB_URI") {
		t.Errorf("error should mention MONGODB_URI, got: %v", err)
	}
}

func TestLoad_MissingRedisURL(t *testing.T) {
	clearEnv(t)
	t.Setenv("MONGODB_URI", "mongodb://admin:password@mongo:27017")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when REDIS_URL is missing")
	}
	if !strings.Contains(err.Error(), "REDIS_URL") {
		t.Errorf("error should mention REDIS_URL, got: %v", err)
	}
}

func TestLoad_InvalidValues(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "invalid MAX_UPLOAD_MB",
			env:  map[string]string{"MAX_UPLOAD_MB": "abc"},
			want: "MAX_UPLOAD_MB",
		},
		{
			name: "negative MAX_UPLOAD_MB",
			env:  map[string]string{"MAX_UPLOAD_MB": "-5"},
			want: "MAX_UPLOAD_MB",
		},
		{
			name: "invalid EXTRACTION_TIMEOUT",
			env:  map[string]string{"EXTRACTION_TIMEOUT": "xyz"},
			want: "EXTRACTION_TIMEOUT",
		},
		{
			name: "invalid CONCURRENCY",
			env:  map[string]string{"CONCURRENCY": "-3"},
			want: "CONCURRENCY",
		},
		{
			name: "invalid MAX_IN_FLIGHT",
			env:  map[string]string{"MAX_IN_FLIGHT": "abc"},
			want: "MAX_IN_FLIGHT",
		},
		{
			name: "invalid QUEUE_SIZE",
			env:  map[string]string{"QUEUE_SIZE": "abc"},
			want: "QUEUE_SIZE",
		},
		{
			// 0 would mean "no waiting buffer", i.e. shed every request that
			// finds the workers busy. That is a misconfiguration, not a default.
			name: "zero QUEUE_SIZE",
			env:  map[string]string{"QUEUE_SIZE": "0"},
			want: "QUEUE_SIZE",
		},
		{
			name: "negative QUEUE_SIZE",
			env:  map[string]string{"QUEUE_SIZE": "-200"},
			want: "QUEUE_SIZE",
		},
		{
			name: "invalid REDIS_TIMEOUT",
			env:  map[string]string{"REDIS_TIMEOUT": "soon"},
			want: "REDIS_TIMEOUT",
		},
		{
			name: "zero REDIS_TIMEOUT",
			env:  map[string]string{"REDIS_TIMEOUT": "0s"},
			want: "REDIS_TIMEOUT",
		},
		{
			name: "invalid ERR_BASE_URL",
			env:  map[string]string{"ERR_BASE_URL": "not a uri"},
			want: "ERR_BASE_URL",
		},
		{
			name: "invalid PORT",
			env:  map[string]string{"PORT": "not-a-port"},
			want: "PORT",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("MONGODB_URI", "mongodb://admin:password@mongo:27017")
			t.Setenv("REDIS_URL", "redis://redis:6379")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := config.Load()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q, got: %v", tc.want, err)
			}
		})
	}
}
