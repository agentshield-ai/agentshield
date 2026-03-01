package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
)

func makeVerdict(action models.Action) *CachedVerdict {
	return &CachedVerdict{
		Action:   action,
		Alerts:   []engine.RuleResult{{RuleID: "r1", Matched: true}},
		CachedAt: time.Now(),
	}
}

func TestGet_miss_returns_false(t *testing.T) {
	c := NewVerdictCache(10, time.Minute)
	v, ok := c.Get("nonexistent")
	if ok || v != nil {
		t.Fatal("expected miss for nonexistent key")
	}
}

func TestSet_and_Get_roundtrip(t *testing.T) {
	c := NewVerdictCache(10, time.Minute)
	verdict := makeVerdict(models.ActionBlock)
	c.Set("k1", verdict)

	got, ok := c.Get("k1")
	if !ok {
		t.Fatal("expected hit")
	}
	if got.Action != models.ActionBlock {
		t.Fatalf("got action %s, want block", got.Action)
	}
	if len(got.Alerts) != 1 {
		t.Fatalf("got %d alerts, want 1", len(got.Alerts))
	}
}

func TestSet_update_existing_key(t *testing.T) {
	c := NewVerdictCache(10, time.Minute)
	c.Set("k1", makeVerdict(models.ActionBlock))
	c.Set("k1", makeVerdict(models.ActionAllow))

	got, ok := c.Get("k1")
	if !ok {
		t.Fatal("expected hit after update")
	}
	if got.Action != models.ActionAllow {
		t.Fatalf("got action %s, want allow after update", got.Action)
	}

	stats := c.Stats()
	if stats.Size != 1 {
		t.Fatalf("expected size 1 after updating same key, got %d", stats.Size)
	}
}

func TestEviction_at_capacity(t *testing.T) {
	c := NewVerdictCache(3, time.Minute)

	c.Set("a", makeVerdict(models.ActionAllow))
	c.Set("b", makeVerdict(models.ActionAllow))
	c.Set("c", makeVerdict(models.ActionAllow))

	// This should evict "a" (LRU)
	c.Set("d", makeVerdict(models.ActionBlock))

	if _, ok := c.Get("a"); ok {
		t.Fatal("expected 'a' to be evicted")
	}
	if _, ok := c.Get("d"); !ok {
		t.Fatal("expected 'd' to be present")
	}

	stats := c.Stats()
	if stats.Evictions != 1 {
		t.Fatalf("expected 1 eviction, got %d", stats.Evictions)
	}
	if stats.Size != 3 {
		t.Fatalf("expected size 3, got %d", stats.Size)
	}
}

func TestEviction_promotes_accessed_entry(t *testing.T) {
	c := NewVerdictCache(3, time.Minute)

	c.Set("a", makeVerdict(models.ActionAllow))
	c.Set("b", makeVerdict(models.ActionAllow))
	c.Set("c", makeVerdict(models.ActionAllow))

	// Access "a" to promote it
	c.Get("a")

	// Insert "d" — should evict "b" (now LRU), not "a"
	c.Set("d", makeVerdict(models.ActionBlock))

	if _, ok := c.Get("a"); !ok {
		t.Fatal("expected 'a' to survive after promotion")
	}
	if _, ok := c.Get("b"); ok {
		t.Fatal("expected 'b' to be evicted as LRU")
	}
}

func TestTTL_expiry(t *testing.T) {
	c := NewVerdictCache(10, 50*time.Millisecond)

	verdict := &CachedVerdict{
		Action:   models.ActionBlock,
		CachedAt: time.Now(),
	}
	c.Set("k1", verdict)

	// Immediate get should hit
	if _, ok := c.Get("k1"); !ok {
		t.Fatal("expected hit before TTL expiry")
	}

	// Wait for TTL to expire
	time.Sleep(60 * time.Millisecond)

	if _, ok := c.Get("k1"); ok {
		t.Fatal("expected miss after TTL expiry")
	}

	stats := c.Stats()
	if stats.Size != 0 {
		t.Fatalf("expected expired entry to be removed, size=%d", stats.Size)
	}
}

func TestCacheKey_deterministic_regardless_of_arg_order(t *testing.T) {
	args1 := map[string]string{"path": "/etc/passwd", "command": "cat"}
	args2 := map[string]string{"command": "cat", "path": "/etc/passwd"}

	k1 := CacheKey("bash", args1)
	k2 := CacheKey("bash", args2)

	if k1 != k2 {
		t.Fatalf("same args in different order produced different keys:\n  k1=%s\n  k2=%s", k1, k2)
	}
}

func TestCacheKey_different_tools_different_keys(t *testing.T) {
	args := map[string]string{"path": "/tmp"}
	k1 := CacheKey("read_file", args)
	k2 := CacheKey("write_file", args)

	if k1 == k2 {
		t.Fatal("different tool names should produce different keys")
	}
}

func TestCacheKey_different_args_different_keys(t *testing.T) {
	k1 := CacheKey("bash", map[string]string{"command": "ls"})
	k2 := CacheKey("bash", map[string]string{"command": "rm -rf /"})

	if k1 == k2 {
		t.Fatal("different args should produce different keys")
	}
}

func TestCacheKey_nil_args(t *testing.T) {
	k1 := CacheKey("tool", nil)
	k2 := CacheKey("tool", map[string]string{})

	if k1 != k2 {
		t.Fatalf("nil and empty args should produce the same key:\n  k1=%s\n  k2=%s", k1, k2)
	}
}

func TestStats_accuracy(t *testing.T) {
	c := NewVerdictCache(10, time.Minute)

	// 2 misses
	c.Get("x")
	c.Get("y")

	// 1 set + 1 hit
	c.Set("x", makeVerdict(models.ActionAllow))
	c.Get("x")

	stats := c.Stats()
	if stats.Hits != 1 {
		t.Fatalf("expected 1 hit, got %d", stats.Hits)
	}
	if stats.Misses != 2 {
		t.Fatalf("expected 2 misses, got %d", stats.Misses)
	}
	if stats.Size != 1 {
		t.Fatalf("expected size 1, got %d", stats.Size)
	}
	if stats.MaxSize != 10 {
		t.Fatalf("expected max_size 10, got %d", stats.MaxSize)
	}
}

func TestInvalidate_clears_all_entries(t *testing.T) {
	c := NewVerdictCache(10, time.Minute)
	c.Set("a", makeVerdict(models.ActionAllow))
	c.Set("b", makeVerdict(models.ActionBlock))

	c.Invalidate()

	if _, ok := c.Get("a"); ok {
		t.Fatal("expected miss after invalidation")
	}
	if _, ok := c.Get("b"); ok {
		t.Fatal("expected miss after invalidation")
	}
	stats := c.Stats()
	if stats.Size != 0 {
		t.Fatalf("expected size 0 after invalidation, got %d", stats.Size)
	}
}

func TestInvalidate_preserves_counters(t *testing.T) {
	c := NewVerdictCache(10, time.Minute)
	c.Set("a", makeVerdict(models.ActionAllow))
	c.Get("a") // hit

	c.Invalidate()

	stats := c.Stats()
	if stats.Hits != 1 {
		t.Fatalf("expected hits to be preserved after invalidation, got %d", stats.Hits)
	}
}

func TestConcurrent_reads_and_writes(t *testing.T) {
	c := NewVerdictCache(100, time.Minute)
	const goroutines = 50
	const opsPerGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				key := fmt.Sprintf("key-%d-%d", id, i%10)
				if i%3 == 0 {
					c.Set(key, makeVerdict(models.ActionAllow))
				} else {
					c.Get(key)
				}
			}
		}(g)
	}

	wg.Wait()

	// Verify internal consistency
	stats := c.Stats()
	if stats.Size < 0 || stats.Size > 100 {
		t.Fatalf("cache size out of bounds: %d", stats.Size)
	}
	if stats.Hits+stats.Misses == 0 {
		t.Fatal("expected some cache operations to be recorded")
	}
}

func TestNewVerdictCache_defaults(t *testing.T) {
	c := NewVerdictCache(0, 0)
	if c.maxSize != 10000 {
		t.Fatalf("expected default maxSize=10000, got %d", c.maxSize)
	}
	if c.ttl != 5*time.Minute {
		t.Fatalf("expected default ttl=5m, got %s", c.ttl)
	}
}
