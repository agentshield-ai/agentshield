package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/triage"
)

// CachedVerdict stores the result of a previous evaluation for replay on cache hit.
type CachedVerdict struct {
	Action        models.Action         `json:"action"`
	Alerts        []engine.RuleResult   `json:"alerts"`
	TriageResults []triage.TriageResult `json:"triage_results,omitempty"`
	Overridable   bool                  `json:"overridable"`
	CachedAt      time.Time             `json:"cached_at"`
}

// CacheStats exposes operational metrics for the verdict cache.
type CacheStats struct {
	Hits      uint64 `json:"hits"`
	Misses    uint64 `json:"misses"`
	Size      int    `json:"size"`
	MaxSize   int    `json:"max_size"`
	Evictions uint64 `json:"evictions"`
}

// entry is the value stored in the doubly-linked list.
type entry struct {
	key     string
	verdict *CachedVerdict
}

// VerdictCache is a thread-safe LRU cache with TTL-based expiry.
// It uses a doubly-linked list + map for O(1) get/set/eviction.
type VerdictCache struct {
	mu        sync.RWMutex
	maxSize   int
	ttl       time.Duration
	items     map[string]*list.Element
	order     *list.List // front = most recently used
	hits      uint64
	misses    uint64
	evictions uint64
}

// NewVerdictCache creates a new verdict cache with the given capacity and TTL.
func NewVerdictCache(maxSize int, ttl time.Duration) *VerdictCache {
	if maxSize <= 0 {
		maxSize = 10000
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &VerdictCache{
		maxSize: maxSize,
		ttl:     ttl,
		items:   make(map[string]*list.Element, maxSize),
		order:   list.New(),
	}
}

// Get retrieves a cached verdict by key. Returns nil, false on miss or expiry.
func (c *VerdictCache) Get(key string) (*CachedVerdict, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		c.misses++
		return nil, false
	}

	ent := elem.Value.(*entry)

	// Check TTL expiry
	if time.Since(ent.verdict.CachedAt) > c.ttl {
		// Expired — evict
		c.removeLocked(elem)
		c.misses++
		return nil, false
	}

	// Move to front (most recently used)
	c.order.MoveToFront(elem)
	c.hits++
	return ent.verdict, true
}

// Set stores a verdict in the cache. If the cache is at capacity, the least
// recently used entry is evicted first.
func (c *VerdictCache) Set(key string, verdict *CachedVerdict) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If key already exists, update in-place and promote
	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		elem.Value.(*entry).verdict = verdict
		return
	}

	// Evict LRU if at capacity
	for c.order.Len() >= c.maxSize {
		c.evictLRULocked()
	}

	ent := &entry{key: key, verdict: verdict}
	elem := c.order.PushFront(ent)
	c.items[key] = elem
}

// Stats returns a snapshot of cache operational metrics.
func (c *VerdictCache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return CacheStats{
		Hits:      c.hits,
		Misses:    c.misses,
		Size:      c.order.Len(),
		MaxSize:   c.maxSize,
		Evictions: c.evictions,
	}
}

// Invalidate clears all entries from the cache. This should be called on rule
// hot-reload to prevent stale verdicts from being served.
func (c *VerdictCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*list.Element, c.maxSize)
	c.order.Init()
}

// CacheKey computes a deterministic SHA-256 cache key from a tool name and its
// arguments. Arguments are sorted by key so that logically identical requests
// (with args in different map-iteration order) produce the same key.
func CacheKey(toolName string, args map[string]string) string {
	return CacheKeyWithContext(toolName, args, "")
}

// CacheKeyWithContext computes a deterministic SHA-256 cache key from tool,
// args, and execution context.
func CacheKeyWithContext(toolName string, args map[string]string, context string) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	fmt.Fprintf(h, "tool=%s", toolName)
	fmt.Fprintf(h, "\ncontext=%s", normalizeContext(context))
	for _, k := range keys {
		fmt.Fprintf(h, "\n%s=%s", k, args[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func normalizeContext(context string) string {
	c := strings.ToLower(strings.TrimSpace(context))
	if c == "" {
		return "prod"
	}
	return c
}

// removeLocked removes an element from both the list and the map.
// Caller must hold c.mu.
func (c *VerdictCache) removeLocked(elem *list.Element) {
	ent := c.order.Remove(elem).(*entry)
	delete(c.items, ent.key)
}

// evictLRULocked removes the least recently used entry.
// Caller must hold c.mu.
func (c *VerdictCache) evictLRULocked() {
	back := c.order.Back()
	if back == nil {
		return
	}
	c.removeLocked(back)
	c.evictions++
}
