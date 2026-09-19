package config_test

import (
	"strings"
	"testing"
	"time"

	"microservicio-go/config"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"PORT", "MONGODB_URI", "MONGODB_DB", "MONGODB_COLLECTION", "MAX_UPLOAD_BYTES", "EXTRACTION_TIMEOUT", "CONCURRENCY", "ERR_BASE_URL"} {
		t.Setenv(k, "")
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("MONGODB_URI", "mongodb://admin:password@mongo:27017")

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
	if cfg.MaxUploadBytes != 25*1024*1024 {
		t.Errorf("MaxUploadBytes = %d, want %d", cfg.MaxUploadBytes, 25*1024*1024)
	}
	if cfg.ExtractionTimeout != 30*time.Second {
		t.Errorf("ExtractionTimeout = %v, want 30s", cfg.ExtractionTimeout)
	}
	if cfg.Concurrency != 0 { // 0 means "fall back to NumCPU in the use case"
		t.Errorf("Concurrency = %d, want default 0", cfg.Concurrency)
	}
	if cfg.ErrBaseURL != "https://errors.example.com" {
		t.Errorf("ErrBaseURL = %q, want default", cfg.ErrBaseURL)
	}
}

func TestLoad_RespectsOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("MONGODB_URI", "mongodb://u:p@h:27017")
	t.Setenv("MONGODB_DB", "custom_db")
	t.Setenv("MONGODB_COLLECTION", "custom_col")
	t.Setenv("PORT", "9090")
	t.Setenv("MAX_UPLOAD_BYTES", "1048576")
	t.Setenv("EXTRACTION_TIMEOUT", "5s")
	t.Setenv("CONCURRENCY", "4")
	t.Setenv("ERR_BASE_URL", "https://errors.acme.dev")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "9090" || cfg.MongoDB != "custom_db" || cfg.MongoCollection != "custom_col" || cfg.MaxUploadBytes != 1048576 || cfg.ExtractionTimeout != 5*time.Second || cfg.Concurrency != 4 || cfg.ErrBaseURL != "https://errors.acme.dev" {
		t.Errorf("overrides not applied: %+v", cfg)
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

func TestLoad_InvalidValues(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "invalid MAX_UPLOAD_BYTES",
			env:  map[string]string{"MAX_UPLOAD_BYTES": "abc"},
			want: "MAX_UPLOAD_BYTES",
		},
		{
			name: "negative MAX_UPLOAD_BYTES",
			env:  map[string]string{"MAX_UPLOAD_BYTES": "-5"},
			want: "MAX_UPLOAD_BYTES",
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
