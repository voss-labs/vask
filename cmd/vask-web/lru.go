package main

import (
	"sync"
	"time"
)

// In-process TTL'd cache for rendered pages and the sitemap. Coarse single
// mutex is fine — the work it guards is microseconds, never a hot loop.
// On a cold-set when the map is at capacity, we sweep expired entries first,
// then drop arbitrary entries until we're under the cap. Not strict LRU, but
// good enough for a forum where access patterns track a long-tail of posts.

type lruEntry struct {
	body []byte
	exp  time.Time
}

type lru struct {
	mu  sync.Mutex
	m   map[string]lruEntry
	max int
}

func newLRU(max int) *lru { return &lru{m: make(map[string]lruEntry, max), max: max} }

func (l *lru) get(key string) ([]byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[key]
	if !ok || time.Now().After(e.exp) {
		delete(l.m, key)
		return nil, false
	}
	return e.body, true
}

func (l *lru) set(key string, body []byte, ttl time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.m) >= l.max {
		now := time.Now()
		for k, e := range l.m {
			if now.After(e.exp) {
				delete(l.m, k)
			}
		}
		for len(l.m) >= l.max {
			for k := range l.m {
				delete(l.m, k)
				break
			}
		}
	}
	l.m[key] = lruEntry{body: body, exp: time.Now().Add(ttl)}
}
