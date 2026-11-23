package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"microservicio-go/application"
	"microservicio-go/config"
	"microservicio-go/consul"
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
	consulAddress := getEnv("CONSUL_HTTP_ADDR", "consul:8500")
	consulToken := os.Getenv("CONSUL_HTTP_TOKEN")

	// Load config with optional Consul KV watcher for hot-reload
	cfg, configWatcher, err := config.LoadWithWatcher(consulAddress, consulToken)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// Stop config watcher on shutdown
	defer func() {
		if configWatcher != nil {
			configWatcher.Stop()
		}
	}()

	// Initialize Consul registrar
	port, err := consul.ParsePort(cfg.Port)
	if err != nil {
		return fmt.Errorf("invalid port: %w", err)
	}

	consulCfg := consul.Config{
		ServiceName:   "microservicio-go",
		ServicePort:   port,
		HealthPath:    "/api/v1/health",
		ConsulAddress: consulAddress,
		ConsulToken:   consulToken,
		// Traefik tags for Consul Catalog provider (Phase 6)
		Tags: []string{
			"traefik.http.routers.go-extract.rule=Host(`${MICROSERVICE_HOST:-extract.localhost}`)",
			"traefik.http.routers.go-extract.entrypoints=web",
			"traefik.http.routers.go-extract.service=go-extract",
			"traefik.http.routers.go-extract.middlewares=go-errors,go-ratelimit,go-cb",
			"traefik.http.middlewares.go-ratelimit.ratelimit.average=500",
			"traefik.http.middlewares.go-ratelimit.ratelimit.burst=1000",
			"traefik.http.middlewares.go-cb.circuitbreaker.expression=NetworkErrorRatio() > 0.33",
			"traefik.http.middlewares.go-errors.errors.status=429,503",
			"traefik.http.middlewares.go-errors.errors.query=/traefik/errors/{status}",
			"traefik.http.middlewares.go-errors.errors.service=go-extract",
			"traefik.http.services.go-extract.loadbalancer.server.port=8080",
			"traefik.http.services.go-extract.loadbalancer.healthcheck.path=/api/v1/health",
			"traefik.http.services.go-extract.loadbalancer.healthcheck.interval=10s",
			"traefik.http.services.go-extract.loadbalancer.healthcheck.timeout=3s",
		},
	}

	registrar, err := consul.NewRegistrar(consulCfg)
	if err != nil {
		log.Printf("warning: failed to create Consul registrar: %v", err)
		registrar = nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Register with Consul before starting the server
	if registrar != nil {
		if err := registrar.Register(ctx); err != nil {
			log.Printf("warning: failed to register with Consul: %v", err)
			registrar = nil
		} else {
			go registrar.WaitForShutdown()
		}
	}

	// Initialize Consul service discovery for external dependencies (optional)
	// For Redis (external), use regular discovery. For service-to-service, use Connect sidecar.
	discoveryCfg := consul.DiscoveryConfig{
		ConsulAddress:      consulAddress,
		ConsulToken:        consulToken,
		DNSPort:            8600,
		CacheTTL:           30 * time.Second,
		UseConnectSidecar:  false, // Redis doesn't have a sidecar
	}
	discovery, _ := consul.NewServiceDiscovery(discoveryCfg) // Ignore error, fallback to env var

	// Resolve Redis connection via Consul discovery, fallback to config
	redisURL := cfg.RedisURL
	if discovery != nil {
		if addr, err := discovery.ResolveService(ctx, "redis"); err == nil {
			// Build Redis URL from discovered address
			redisURL = buildRedisURL(cfg.RedisURL, addr.Host, addr.Port)
			log.Printf("Resolved Redis via Consul: %s:%d", addr.Host, addr.Port)
		} else {
			log.Printf("Warning: Failed to resolve Redis via Consul, using config: %v", err)
		}
	}

	cache, err := redisadapter.NewAdapter(redisURL, cfg.CacheTTL)
	if err != nil {
		if registrar != nil {
			registrar.SignalShutdown()
		}
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
	
	// Convert config snapshot to presentation.Config
	toPresentationConfig := func(s config.ConfigSnapshot) presentation.Config {
		return presentation.Config{
			ErrBaseURL:        s.ErrBaseURL,
			MaxUploadBytes:    s.MaxUploadBytes,
			ExtractionTimeout: s.ExtractionTimeout,
			MaxInFlight:       s.MaxInFlight,
			QueueSize:         s.QueueSize,
			AdmissionTimeout:  s.AdmissionTimeout,
		}
	}

	// Use ConfigProvider for hot-reload support
	configProvider := presentation.NewStaticConfigProvider(toPresentationConfig(cfg.Snapshot()))
	
	// If config watcher is available, update provider on config changes
	if cfg.ConfigWatcher != nil {
		go func() {
			for {
				time.Sleep(5 * time.Second)
				configProvider.Update(toPresentationConfig(cfg.Snapshot()))
			}
		}()
	}
	
	router := presentation.NewRouter(useCase, cache, configProvider)

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
		if registrar != nil {
			registrar.SignalShutdown()
		}
		_ = cache.Close()
		return fmt.Errorf("server: %w", err)
	case <-ctx.Done():
		log.Printf("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			if registrar != nil {
				registrar.SignalShutdown()
			}
			_ = cache.Close()
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		if registrar != nil {
			registrar.SignalShutdown()
		}
		_ = cache.Close()
		log.Printf("shutdown complete")
		return nil
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// buildRedisURL constructs a Redis connection URL from the original URL (for auth/db)
// and the discovered host:port from Consul.
func buildRedisURL(originalURL, host string, port int) string {
	// If original URL has credentials or db number, preserve them
	// Format: redis://[username:password@]host[:port][/db][?options]
	if strings.Contains(originalURL, "@") || strings.Contains(originalURL, "/") {
		// Extract the part after redis:// and before host
		prefix := "redis://"
		if strings.HasPrefix(originalURL, prefix) {
			rest := originalURL[len(prefix):]
			// Find the host part (after @ or start)
			if idx := strings.Index(rest, "@"); idx >= 0 {
				// Has credentials: user:pass@host:port/db
				creds := rest[:idx+1] // includes @
				// Find db number if present
				dbPart := ""
				if dbIdx := strings.Index(rest[idx+1:], "/"); dbIdx >= 0 {
					dbPart = rest[idx+1+dbIdx:]
				}
				return prefix + creds + host + ":" + strconv.Itoa(port) + dbPart
			}
			// No credentials, check for db number
			if dbIdx := strings.Index(rest, "/"); dbIdx >= 0 {
				dbPart := rest[dbIdx:]
				return prefix + host + ":" + strconv.Itoa(port) + dbPart
			}
			return prefix + host + ":" + strconv.Itoa(port)
		}
	}
	// Simple case: redis://host:port
	return "redis://" + host + ":" + strconv.Itoa(port)
}
