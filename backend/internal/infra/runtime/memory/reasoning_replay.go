package memory

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// defaultReasoningReplayMaxBytes bounds cached ciphertext: one entry may hold
// several encrypted reasoning items of up to 8 MiB each, so an entry count
// alone does not bound memory.
const defaultReasoningReplayMaxBytes int64 = 256 << 20

type reasoningReplayEntry struct {
	key       string
	items     [][]byte
	bytes     int64
	expiresAt time.Time
	storedAt  time.Time
}

// ReasoningReplayStore 提供单实例有界推理回放缓存，按条目数与总字节数做 LRU 淘汰。
type ReasoningReplayStore struct {
	mu         sync.Mutex
	maxSize    int
	maxBytes   int64
	totalBytes int64
	order      *list.List // 队头为最久未使用
	values     map[string]*list.Element
	ttlSlide   bool
}

// NewReasoningReplayStore 创建内存推理回放仓储；maxSize 为全局条目上限。
func NewReasoningReplayStore(maxSize int) *ReasoningReplayStore {
	if maxSize < 1 {
		maxSize = 10240
	}
	return &ReasoningReplayStore{
		maxSize:  maxSize,
		maxBytes: defaultReasoningReplayMaxBytes,
		order:    list.New(),
		values:   make(map[string]*list.Element),
		ttlSlide: true,
	}
}

func reasoningReplayMapKey(model, sessionKey string) string {
	return model + "\x00" + sessionKey
}

func cloneReplayItems(items [][]byte) [][]byte {
	if len(items) == 0 {
		return nil
	}
	cloned := make([][]byte, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, append([]byte(nil), item...))
	}
	return cloned
}

func replayEntryBytes(key string, items [][]byte) int64 {
	size := int64(len(key))
	for _, item := range items {
		size += int64(len(item))
	}
	return size
}

func (s *ReasoningReplayStore) Get(_ context.Context, model, sessionKey string, now time.Time, ttl time.Duration) ([][]byte, bool, error) {
	if model == "" || sessionKey == "" {
		return nil, false, nil
	}
	key := reasoningReplayMapKey(model, sessionKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	element, ok := s.values[key]
	if !ok {
		return nil, false, nil
	}
	entry := element.Value.(*reasoningReplayEntry)
	if !now.Before(entry.expiresAt) {
		s.removeLocked(element)
		return nil, false, nil
	}
	if s.ttlSlide {
		if ttl <= 0 {
			ttl = entry.expiresAt.Sub(entry.storedAt)
			if ttl <= 0 {
				ttl = time.Hour
			}
		}
		entry.expiresAt = now.Add(ttl)
		entry.storedAt = now
		s.order.MoveToBack(element)
	}
	return cloneReplayItems(entry.items), true, nil
}

func (s *ReasoningReplayStore) Set(_ context.Context, model, sessionKey string, items [][]byte, expiresAt time.Time) error {
	if model == "" || sessionKey == "" || len(items) == 0 || expiresAt.IsZero() {
		return nil
	}
	key := reasoningReplayMapKey(model, sessionKey)
	cloned := cloneReplayItems(items)
	size := replayEntryBytes(key, cloned)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	// The previous state for this session is superseded even when the new one
	// is too large to cache; replaying the stale one would be wrong.
	if element, exists := s.values[key]; exists {
		s.removeLocked(element)
	}
	if size > s.maxBytes {
		return nil
	}
	s.evictLocked(now, size)
	entry := &reasoningReplayEntry{key: key, items: cloned, bytes: size, expiresAt: expiresAt, storedAt: now}
	s.values[key] = s.order.PushBack(entry)
	s.totalBytes += size
	return nil
}

func (s *ReasoningReplayStore) Delete(_ context.Context, model, sessionKey string) error {
	if model == "" || sessionKey == "" {
		return nil
	}
	key := reasoningReplayMapKey(model, sessionKey)
	s.mu.Lock()
	if element, ok := s.values[key]; ok {
		s.removeLocked(element)
	}
	s.mu.Unlock()
	return nil
}

// evictLocked drops expired entries from the LRU front, then the least recently
// used ones until an incoming entry of the given size fits both limits.
func (s *ReasoningReplayStore) evictLocked(now time.Time, incoming int64) {
	for element := s.order.Front(); element != nil; element = s.order.Front() {
		entry := element.Value.(*reasoningReplayEntry)
		expired := !now.Before(entry.expiresAt)
		full := len(s.values) >= s.maxSize || s.totalBytes+incoming > s.maxBytes
		if !expired && !full {
			return
		}
		s.removeLocked(element)
	}
}

func (s *ReasoningReplayStore) removeLocked(element *list.Element) {
	entry := s.order.Remove(element).(*reasoningReplayEntry)
	delete(s.values, entry.key)
	s.totalBytes -= entry.bytes
}
