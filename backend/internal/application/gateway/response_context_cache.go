package gateway

import (
	"sync"
	"time"
)

const responseContextCacheLimit = 8192

type responseContextKey struct {
	clientKeyID uint64
	responseID  string
}

type responseContextEntry struct {
	tokens    int64
	expiresAt time.Time
}

// responseContextCache remembers the token size (input + output) of recently
// stored responses so a previous_response_id continuation can reserve, and
// estimate when usage is missing, the server-side context the upstream will
// replay. It is process-local and best effort: responses created by another
// replica or before a restart fall back to the request body alone.
type responseContextCache struct {
	mu      sync.Mutex
	entries map[responseContextKey]responseContextEntry
	order   []responseContextKey
}

func (c *responseContextCache) remember(clientKeyID uint64, responseID string, tokens int64, expiresAt time.Time) {
	if responseID == "" || tokens <= 0 {
		return
	}
	key := responseContextKey{clientKeyID: clientKeyID, responseID: responseID}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[responseContextKey]responseContextEntry)
	}
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = responseContextEntry{tokens: tokens, expiresAt: expiresAt}
	for len(c.entries) > responseContextCacheLimit && len(c.order) > 0 {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
}

func (c *responseContextCache) lookup(clientKeyID uint64, responseID string, now time.Time) int64 {
	if responseID == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[responseContextKey{clientKeyID: clientKeyID, responseID: responseID}]
	if !ok || !now.Before(entry.expiresAt) {
		return 0
	}
	return entry.tokens
}
