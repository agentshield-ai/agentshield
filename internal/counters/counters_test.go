package counters

import (
	"sync"
	"testing"
	"time"
)

func TestRecordAndCount(t *testing.T) {
	wc := NewWindowCounter(WindowConfig{
		Windows:            []time.Duration{5 * time.Minute, 1 * time.Hour},
		MaxTimestampsPerKey: 1000,
		MaxKeys:            10000,
		CleanupInterval:    1 * time.Hour, // Don't run cleanup during test
	})
	defer wc.Stop()

	now := time.Now()
	key := "session:test"

	// Record 25 events spread over 4 minutes
	for i := 0; i < 25; i++ {
		ts := now.Add(-time.Duration(4*60-i*10) * time.Second) // spread over 4 minutes
		wc.Record(key, ts)
	}

	count5m := wc.Count(key, 5*time.Minute)
	if count5m != 25 {
		t.Errorf("Count(5m) = %d, want 25", count5m)
	}

	count1h := wc.Count(key, 1*time.Hour)
	if count1h != 25 {
		t.Errorf("Count(1h) = %d, want 25", count1h)
	}
}

func TestWindowExpiry(t *testing.T) {
	wc := NewWindowCounter(WindowConfig{
		Windows:            []time.Duration{5 * time.Minute},
		MaxTimestampsPerKey: 1000,
		MaxKeys:            10000,
		CleanupInterval:    1 * time.Hour,
	})
	defer wc.Stop()

	key := "session:expire"
	now := time.Now()

	// Record events 10 minutes ago (outside 5m window)
	for i := 0; i < 10; i++ {
		wc.Record(key, now.Add(-10*time.Minute))
	}

	// Record events 2 minutes ago (inside 5m window)
	for i := 0; i < 5; i++ {
		wc.Record(key, now.Add(-2*time.Minute))
	}

	count5m := wc.Count(key, 5*time.Minute)
	if count5m != 5 {
		t.Errorf("Count(5m) = %d, want 5 (old events should be outside window)", count5m)
	}
}

func TestMissingKeyReturnsZero(t *testing.T) {
	wc := NewWindowCounter(DefaultConfig())
	defer wc.Stop()

	count := wc.Count("nonexistent", 5*time.Minute)
	if count != 0 {
		t.Errorf("Count for missing key = %d, want 0", count)
	}
}

func TestCapEnforcement(t *testing.T) {
	wc := NewWindowCounter(WindowConfig{
		Windows:            []time.Duration{1 * time.Hour},
		MaxTimestampsPerKey: 100,
		MaxKeys:            10000,
		CleanupInterval:    1 * time.Hour,
	})
	defer wc.Stop()

	key := "session:cap"
	now := time.Now()

	// Record 200 events (cap is 100)
	for i := 0; i < 200; i++ {
		wc.Record(key, now.Add(-time.Duration(i)*time.Second))
	}

	wc.mu.Lock()
	bucketLen := len(wc.buckets[key].timestamps)
	wc.mu.Unlock()

	if bucketLen != 100 {
		t.Errorf("Bucket length = %d, want 100 (cap enforcement)", bucketLen)
	}
}

func TestMaxKeysEnforcement(t *testing.T) {
	wc := NewWindowCounter(WindowConfig{
		Windows:            []time.Duration{5 * time.Minute},
		MaxTimestampsPerKey: 100,
		MaxKeys:            5,
		CleanupInterval:    1 * time.Hour,
	})
	defer wc.Stop()

	now := time.Now()

	// Record events for 10 different keys (max is 5)
	for i := 0; i < 10; i++ {
		wc.Record("key"+string(rune('0'+i)), now)
	}

	wc.mu.Lock()
	keyCount := len(wc.buckets)
	wc.mu.Unlock()

	if keyCount > 5 {
		t.Errorf("Key count = %d, want <= 5 (max keys enforcement)", keyCount)
	}
}

func TestCleanup(t *testing.T) {
	wc := NewWindowCounter(WindowConfig{
		Windows:            []time.Duration{5 * time.Minute},
		MaxTimestampsPerKey: 1000,
		MaxKeys:            10000,
		CleanupInterval:    1 * time.Hour, // Don't auto-cleanup
	})
	defer wc.Stop()

	now := time.Now()

	// Record old events (6 minutes ago, outside 5m max window)
	wc.Record("old-key", now.Add(-6*time.Minute))

	// Record recent events
	wc.Record("recent-key", now)

	// Run cleanup
	wc.Cleanup()

	wc.mu.Lock()
	_, oldExists := wc.buckets["old-key"]
	_, recentExists := wc.buckets["recent-key"]
	wc.mu.Unlock()

	if oldExists {
		t.Error("Cleanup should have removed old-key")
	}
	if !recentExists {
		t.Error("Cleanup should have kept recent-key")
	}
}

func TestConcurrentAccess(t *testing.T) {
	wc := NewWindowCounter(DefaultConfig())
	defer wc.Stop()

	now := time.Now()
	var wg sync.WaitGroup

	// Concurrent writers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				wc.Record("concurrent-key", now)
			}
		}(i)
	}

	// Concurrent readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				wc.Count("concurrent-key", 5*time.Minute)
			}
		}()
	}

	wg.Wait()

	// Should have recorded 1000 events total (10 goroutines * 100 events)
	// but cap may have trimmed some
	count := wc.Count("concurrent-key", 5*time.Minute)
	if count == 0 {
		t.Error("Expected non-zero count after concurrent access")
	}
}

func TestStopPreventsCleanup(t *testing.T) {
	wc := NewWindowCounter(WindowConfig{
		Windows:            []time.Duration{5 * time.Minute},
		MaxTimestampsPerKey: 100,
		MaxKeys:            100,
		CleanupInterval:    10 * time.Millisecond,
	})

	// Stop should not panic
	wc.Stop()

	// Double-stop should not panic
	wc.Stop()
}
