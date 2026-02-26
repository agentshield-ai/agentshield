// Package counters provides sliding-window event counters for temporal detection.
// It enables Sigma rules to match on patterns like "more than 20 tool calls in 5 minutes"
// by tracking event timestamps per key (e.g., session:tool) and providing count lookups
// over configurable time windows.
package counters

import (
	"context"
	"sort"
	"sync"
	"time"
)

const (
	// DefaultMaxTimestampsPerKey caps stored timestamps per key to prevent memory bloat.
	DefaultMaxTimestampsPerKey = 1000
	// DefaultMaxKeys caps total tracked keys as a safety valve.
	DefaultMaxKeys = 10000
)

// WindowConfig configures the counter's time windows and limits.
type WindowConfig struct {
	Windows            []time.Duration // Time windows to support (e.g., 5m, 1h)
	MaxTimestampsPerKey int
	MaxKeys            int
	CleanupInterval    time.Duration
}

// DefaultConfig returns a sensible default configuration.
func DefaultConfig() WindowConfig {
	return WindowConfig{
		Windows:            []time.Duration{5 * time.Minute, 1 * time.Hour},
		MaxTimestampsPerKey: DefaultMaxTimestampsPerKey,
		MaxKeys:            DefaultMaxKeys,
		CleanupInterval:    1 * time.Minute,
	}
}

// eventBucket holds a sorted slice of event timestamps for a single key.
type eventBucket struct {
	timestamps []time.Time
}

// WindowCounter tracks event counts per key over sliding time windows.
// Thread-safe via sync.Mutex.
type WindowCounter struct {
	mu      sync.Mutex
	buckets map[string]*eventBucket
	config  WindowConfig
	cancel  context.CancelFunc
}

// NewWindowCounter creates a new counter and starts a background cleanup goroutine.
func NewWindowCounter(cfg WindowConfig) *WindowCounter {
	if cfg.MaxTimestampsPerKey <= 0 {
		cfg.MaxTimestampsPerKey = DefaultMaxTimestampsPerKey
	}
	if cfg.MaxKeys <= 0 {
		cfg.MaxKeys = DefaultMaxKeys
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 1 * time.Minute
	}
	if len(cfg.Windows) == 0 {
		cfg.Windows = []time.Duration{5 * time.Minute, 1 * time.Hour}
	}

	ctx, cancel := context.WithCancel(context.Background())
	wc := &WindowCounter{
		buckets: make(map[string]*eventBucket),
		config:  cfg,
		cancel:  cancel,
	}

	go wc.cleanupLoop(ctx)
	return wc
}

// Record adds a timestamp for the given key.
func (wc *WindowCounter) Record(key string, ts time.Time) {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	bucket, ok := wc.buckets[key]
	if !ok {
		// Enforce max keys
		if len(wc.buckets) >= wc.config.MaxKeys {
			wc.evictOldestKeyLocked()
		}
		bucket = &eventBucket{}
		wc.buckets[key] = bucket
	}

	bucket.timestamps = append(bucket.timestamps, ts)

	// Enforce per-key cap: keep only the most recent timestamps
	if len(bucket.timestamps) > wc.config.MaxTimestampsPerKey {
		excess := len(bucket.timestamps) - wc.config.MaxTimestampsPerKey
		bucket.timestamps = bucket.timestamps[excess:]
	}
}

// Count returns the number of events for key within the given window duration.
func (wc *WindowCounter) Count(key string, window time.Duration) int {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	bucket, ok := wc.buckets[key]
	if !ok {
		return 0
	}

	cutoff := time.Now().Add(-window)
	count := 0
	for i := len(bucket.timestamps) - 1; i >= 0; i-- {
		if bucket.timestamps[i].After(cutoff) {
			count++
		} else {
			break // timestamps are in insertion order (approximately sorted)
		}
	}

	// If the simple reverse scan didn't work perfectly (out-of-order timestamps),
	// fall back to a full scan for correctness.
	if count == 0 && len(bucket.timestamps) > 0 {
		for _, ts := range bucket.timestamps {
			if ts.After(cutoff) {
				count++
			}
		}
	}

	return count
}

// Stop stops the background cleanup goroutine.
func (wc *WindowCounter) Stop() {
	wc.cancel()
}

// cleanupLoop removes entries older than the largest configured window.
func (wc *WindowCounter) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(wc.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			wc.Cleanup()
		}
	}
}

// Cleanup removes timestamps older than the largest configured window
// and deletes empty buckets.
func (wc *WindowCounter) Cleanup() {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	maxWindow := wc.maxWindow()
	cutoff := time.Now().Add(-maxWindow)

	for key, bucket := range wc.buckets {
		// Find the first timestamp that is within the window
		idx := sort.Search(len(bucket.timestamps), func(i int) bool {
			return bucket.timestamps[i].After(cutoff)
		})

		if idx >= len(bucket.timestamps) {
			// All timestamps are expired
			delete(wc.buckets, key)
		} else if idx > 0 {
			// Trim expired timestamps
			bucket.timestamps = bucket.timestamps[idx:]
		}
	}
}

// maxWindow returns the largest configured window duration.
func (wc *WindowCounter) maxWindow() time.Duration {
	max := time.Duration(0)
	for _, w := range wc.config.Windows {
		if w > max {
			max = w
		}
	}
	return max
}

// evictOldestKeyLocked removes the key with the oldest most-recent timestamp.
// Must be called with wc.mu held.
func (wc *WindowCounter) evictOldestKeyLocked() {
	var oldestKey string
	var oldestTime time.Time
	first := true

	for key, bucket := range wc.buckets {
		if len(bucket.timestamps) == 0 {
			delete(wc.buckets, key)
			return
		}
		lastTs := bucket.timestamps[len(bucket.timestamps)-1]
		if first || lastTs.Before(oldestTime) {
			oldestKey = key
			oldestTime = lastTs
			first = false
		}
	}

	if oldestKey != "" {
		delete(wc.buckets, oldestKey)
	}
}
