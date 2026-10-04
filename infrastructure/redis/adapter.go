package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"microservicio-go/domain"
)

var _ domain.Cache = (*Adapter)(nil)

// entry is the stored JSON document. Field names match the API response so a
// cache read is a straight pass-through to the client.
type entry struct {
	Content   string `json:"content"`
	PageCount int    `json:"page_count"`
}

// Adapter is the Redis-backed domain.Cache. Keys are namespaced with keyPrefix
// so the instance can be shared with the monolith without collisions.
type Adapter struct {
	client    *redis.Client
	ttl       time.Duration
	keyPrefix string
}

// DefaultKeyPrefix namespaces cached extractions.
const DefaultKeyPrefix = "pdf:extract:"

// NewAdapter connects and verifies reachability, failing fast so a
// misconfigured REDIS_URL does not silently serve an empty cache forever.
func NewAdapter(url string, ttl time.Duration) (*Adapter, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	client := redis.NewClient(opt)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return &Adapter{
		client:    client,
		ttl:       ttl,
		keyPrefix: DefaultKeyPrefix,
	}, nil
}

// Get returns the cached result for key, or ok=false on a miss. Any Redis
// failure degrades to a miss: the caller re-parses instead of erroring, so a
// cache outage costs latency but not availability.
func (a *Adapter) Get(ctx context.Context, key string) (content string, pageCount int, ok bool) {
	raw, err := a.client.Get(ctx, a.keyPrefix+key).Result()
	if err != nil {
		return "", 0, false
	}
	var e entry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		// Corrupt entry: drop it so the next request re-parses cleanly.
		a.client.Del(ctx, a.keyPrefix+key)
		return "", 0, false
	}
	return e.Content, e.PageCount, true
}

// Set stores the result under key with the configured TTL.
func (a *Adapter) Set(ctx context.Context, key, content string, pageCount int) error {
	raw, err := json.Marshal(entry{Content: content, PageCount: pageCount})
	if err != nil {
		return fmt.Errorf("marshal cache entry: %w", err)
	}
	if err := a.client.Set(ctx, a.keyPrefix+key, raw, a.ttl).Err(); err != nil {
		return fmt.Errorf("set cache entry: %w", err)
	}
	return nil
}

// Ping reports Redis reachability for health checks.
func (a *Adapter) Ping(ctx context.Context) error {
	return a.client.Ping(ctx).Err()
}

// Close releases the connection pool.
func (a *Adapter) Close() error {
	return a.client.Close()
}
