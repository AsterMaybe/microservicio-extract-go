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
	mongorepo "microservicio-go/infrastructure/mongo"
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

	useCase := application.NewExtractTextUseCase(fitzadapter.NewAdapter(), repo, cfg.Concurrency)

	router := presentation.NewRouter(useCase, repo, presentation.Config{
		ErrBaseURL:        cfg.ErrBaseURL,
		MaxUploadBytes:    cfg.MaxUploadBytes,
		ExtractionTimeout: cfg.ExtractionTimeout,
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
		return fmt.Errorf("server: %w", err)
	case <-ctx.Done():
		log.Printf("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = repo.Disconnect(context.Background())
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		if err := repo.Disconnect(context.Background()); err != nil {
			return fmt.Errorf("disconnect mongo: %w", err)
		}
		log.Printf("shutdown complete")
		return nil
	}
}
