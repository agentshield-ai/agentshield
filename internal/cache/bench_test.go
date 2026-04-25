package cache

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/models"
)

// preloadedCache returns a cache with `size` entries already inserted.
func preloadedCache(size int) (*VerdictCache, []string) {
	c := NewVerdictCache(size, 5*time.Minute)
	keys := make([]string, size)
	for i := 0; i < size; i++ {
		k := fmt.Sprintf("key_%010d", i)
		keys[i] = k
		c.Set(k, makeVerdict(models.ActionAllow))
	}
	return c, keys
}

func BenchmarkVerdictCache_Get_Hit(b *testing.B) {
	c, keys := preloadedCache(10000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Get(keys[i%len(keys)])
	}
}

func BenchmarkVerdictCache_Get_Miss(b *testing.B) {
	c, _ := preloadedCache(10000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Get("absent_key")
	}
}

func BenchmarkVerdictCache_Set(b *testing.B) {
	c, keys := preloadedCache(10000)
	verdict := makeVerdict(models.ActionAllow)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Set(keys[i%len(keys)], verdict)
	}
}

// BenchmarkVerdictCache_GetParallel documents the cost of cache.Get under
// concurrent load. Get takes an exclusive Lock() (cache.go:82) because LRU
// MoveToFront mutates state, so this should NOT scale with GOMAXPROCS — that
// is the point. Issue P2-A proposes splitting the read path; this bench is
// the baseline that fix must beat.
func BenchmarkVerdictCache_GetParallel(b *testing.B) {
	c, keys := preloadedCache(10000)
	var counter atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := counter.Add(1)
			c.Get(keys[i%uint64(len(keys))])
		}
	})
}

// BenchmarkVerdictCache_MixedParallel simulates a realistic 90/10 read/write
// workload under concurrent load.
func BenchmarkVerdictCache_MixedParallel(b *testing.B) {
	c, keys := preloadedCache(10000)
	verdict := makeVerdict(models.ActionAllow)
	var counter atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := counter.Add(1)
			k := keys[i%uint64(len(keys))]
			if i%10 == 0 {
				c.Set(k, verdict)
			} else {
				c.Get(k)
			}
		}
	})
}

// BenchmarkCacheKeyWithFields measures the SHA-256 cache-key cost on the hot
// path varied by field-map size; the eval pipeline computes this on every
// non-cached evaluation.
func BenchmarkCacheKeyWithFields(b *testing.B) {
	tool := "Bash"
	args := map[string]string{
		"command": "echo hello",
	}
	for _, n := range []int{4, 16, 64} {
		b.Run(fmt.Sprintf("fields=%d", n), func(b *testing.B) {
			fields := make(map[string]string, n)
			fields["event_type"] = "tool_call"
			fields["command"] = "echo hello"
			for i := 0; i < n-2; i++ {
				fields[fmt.Sprintf("field_%d", i)] = fmt.Sprintf("value_%d", i)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				CacheKeyWithFields(tool, args, fields, "prod")
			}
		})
	}
}
