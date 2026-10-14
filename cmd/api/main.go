package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"microservicio-go/application"
	"microservicio-go/config"
	fitzadapter "microservicio-go/infrastructure/fitz"
	redisadapter "microservicio-go/infrastructure/redis"
	"microservicio-go/presentation"
)

func main() {
	if err := run(); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cache, err := redisadapter.NewAdapter(cfg.RedisURL, cfg.CacheTTL)
	if err != nil {
		return fmt.Errorf("init redis: %w", err)
	}

	useCase := application.NewExtractTextUseCase(fitzadapter.NewAdapter(), cache, application.Limits{
		Workers:      cfg.Concurrency,
		Admission:    cfg.AdmissionTimeout,
		CacheTimeout: cfg.CacheTimeout,
	})

	// The extractor is stateless: Redis is its only dependency. Health covers
	// exactly that — a degraded Redis means no cache, so report the instance
	// unhealthy instead of silently serving uncached extractions.
	router := presentation.NewRouter(useCase, cache, presentation.Config{
		ErrBaseURL:        cfg.ErrBaseURL,
		MaxUploadBytes:    cfg.MaxUploadBytes,
		ExtractionTimeout: cfg.ExtractionTimeout,
		MaxInFlight:       cfg.MaxInFlight,
		QueueSize:         cfg.QueueSize,
		AdmissionTimeout:  cfg.AdmissionTimeout,
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	log.Printf("listening on :%s", cfg.Port)

	select {
	case err := <-errCh:
		_ = cache.Close()
		return fmt.Errorf("server: %w", err)
	case <-ctx.Done():
		log.Printf("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = cache.Close()
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		_ = cache.Close()
		log.Printf("shutdown complete")
		return nil
	}
}
