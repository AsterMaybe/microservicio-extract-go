package redis_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	redisadapter "microservicio-go/infrastructure/redis"
)

func newAdapter(t *testing.T, ttl time.Duration) (*redisadapter.Adapter, *miniredis.Miniredis) {
	t.Helper()
	srv := miniredis.RunT(t)
	a, err := redisadapter.NewAdapter("redis://"+srv.Addr()+"/0", ttl)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a, srv
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestAdapter_SetThenGet_RoundTripsContentAndPages(t *testing.T) {
	a, _ := newAdapter(t, time.Hour)
	ctx := context.Background()

	// The key is the SHA-256 of the PDF binary, as the use case derives it.
	key := hashOf([]byte("%PDF-1.7 body"))
	if err := a.Set(ctx, key, "texto extraído", 7); err != nil {
		t.Fatalf("Set: %v", err)
	}

	content, pages, ok := a.Get(ctx, key)
	if !ok {
		t.Fatal("Get reported a miss for a key that was just written")
	}
	if content != "texto extraído" {
		t.Errorf("content = %q, want %q", content, "texto extraído")
	}
	if pages != 7 {
		t.Errorf("page_count = %d, want 7", pages)
	}
}

func TestAdapter_Get_MissOnUnknownKey(t *testing.T) {
	a, _ := newAdapter(t, time.Hour)
	if _, _, ok := a.Get(context.Background(), hashOf([]byte("never stored"))); ok {
		t.Error("Get reported a hit for an unknown key")
	}
}

func TestAdapter_KeysAreNamespaced(t *testing.T) {
	a, srv := newAdapter(t, time.Hour)
	ctx := context.Background()
	key := hashOf([]byte("%PDF-1.7 namespaced"))

	if err := a.Set(ctx, key, "c", 1); err != nil {
		t.Fatalf("Set: %v", err)
	}
	want := redisadapter.DefaultKeyPrefix + key
	keys := srv.Keys()
	found := false
	for _, k := range keys {
		if k == want {
			found = true
		}
	}
	if !found {
		t.Errorf("keys = %v, want to contain the namespaced key %q", keys, want)
	}
}

func TestAdapter_EntryExpiresAfterTTL(t *testing.T) {
	a, srv := newAdapter(t, 50*time.Millisecond)
	ctx := context.Background()
	key := hashOf([]byte("%PDF-1.7 expiring"))

	if err := a.Set(ctx, key, "c", 1); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, _, ok := a.Get(ctx, key); !ok {
		t.Fatal("entry must be readable before the TTL elapses")
	}

	srv.FastForward(time.Second)
	if _, _, ok := a.Get(ctx, key); ok {
		t.Error("entry must be gone once the TTL elapses")
	}
}

func TestAdapter_CorruptEntryDegradesToMiss(t *testing.T) {
	a, srv := newAdapter(t, time.Hour)
	ctx := context.Background()
	key := hashOf([]byte("%PDF-1.7 corrupt"))

	// Simulate a truncated/garbage write (e.g. a partial flush or a foreign
	// writer). It must read as a miss so the caller re-parses.
	if err := srv.Set(redisadapter.DefaultKeyPrefix+key, "{not json"); err != nil {
		t.Fatalf("seed corrupt entry: %v", err)
	}
	if _, _, ok := a.Get(ctx, key); ok {
		t.Error("a corrupt entry must report a miss, not a hit")
	}
}

func TestAdapter_EmptyContentIsACacheHit(t *testing.T) {
	a, _ := newAdapter(t, time.Hour)
	ctx := context.Background()
	key := hashOf([]byte("%PDF-1.7 no text layer"))

	if err := a.Set(ctx, key, "", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	content, pages, ok := a.Get(ctx, key)
	if !ok {
		t.Fatal("an empty-but-valid extraction must be a hit, not a miss")
	}
	if content != "" || pages != 0 {
		t.Errorf("got (%q, %d), want (\"\", 0)", content, pages)
	}
}

func TestAdapter_PingAndUnreachableFailsFast(t *testing.T) {
	a, _ := newAdapter(t, time.Hour)
	if err := a.Ping(context.Background()); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestAdapter_UnreachableRedisFailsConstruction(t *testing.T) {
	// Port 1 is reserved and refuses connections: NewAdapter must fail fast so a
	// bad REDIS_URL surfaces at boot instead of as silent cache misses.
	if _, err := redisadapter.NewAdapter("redis://127.0.0.1:1/0", time.Hour); err == nil {
		t.Error("expected an error when Redis is unreachable")
	}
}

func TestAdapter_InvalidURLIsRejected(t *testing.T) {
	if _, err := redisadapter.NewAdapter("not-a-redis-url", time.Hour); err == nil {
		t.Error("expected an error for a malformed REDIS_URL")
	}
}
