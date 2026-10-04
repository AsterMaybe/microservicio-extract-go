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
	"microservicio-go/domain"
	fitzadapter "microservicio-go/infrastructure/fitz"
	mongorepo "microservicio-go/infrastructure/mongo"
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

	repo, err := mongorepo.NewRepository(ctx, cfg.MongoURI, cfg.MongoDB, cfg.MongoCollection)
	if err != nil {
		return fmt.Errorf("init mongo: %w", err)
	}

	cache, err := redisadapter.NewAdapter(cfg.RedisURL, cfg.CacheTTL)
	if err != nil {
		_ = repo.Disconnect(context.Background())
		return fmt.Errorf("init redis: %w", err)
	}

	useCase := application.NewExtractTextUseCase(fitzadapter.NewAdapter(), repo, cache, application.Limits{
		Workers:      cfg.Concurrency,
		Admission:    cfg.AdmissionTimeout,
		CacheTimeout: cfg.CacheTimeout,
	})

	// Health must cover every dependency the extract path needs: a degraded
	// Redis or Mongo means cache misses and failed persistence, so report the
	// instance unhealthy instead of silently serving degraded extractions.
	router := presentation.NewRouter(useCase, healthChecker{mongo: repo, redis: cache}, presentation.Config{
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
		_ = repo.Disconnect(context.Background())
		_ = cache.Close()
		return fmt.Errorf("server: %w", err)
	case <-ctx.Done():
		log.Printf("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = repo.Disconnect(context.Background())
			_ = cache.Close()
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		if err := repo.Disconnect(context.Background()); err != nil {
			_ = cache.Close()
			return fmt.Errorf("disconnect mongo: %w", err)
		}
		_ = cache.Close()
		log.Printf("shutdown complete")
		return nil
	}
}

// healthChecker aggregates dependency probes into the single HealthChecker seam the
// router depends on. Every probe must pass for the instance to report healthy.
type healthChecker struct {
	mongo domain.HealthChecker
	redis domain.HealthChecker
}

func (h healthChecker) Ping(ctx context.Context) error {
	if err := h.mongo.Ping(ctx); err != nil {
		return fmt.Errorf("mongo: %w", err)
	}
	if err := h.redis.Ping(ctx); err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	return nil
}
