package cache

import (
	"context"
	"sync"
	"time"
)

type memoryEntry struct {
	value     []byte
	expiresAt time.Time
}

// MemoryBackend is a concurrency-safe in-process backend with lazy expiration.
type MemoryBackend struct {
	mu      sync.RWMutex
	entries map[string]memoryEntry
	now     func() time.Time
}

// NewMemoryBackend creates an empty memory backend.
func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{entries: make(map[string]memoryEntry), now: time.Now}
}

func (backend *MemoryBackend) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	backend.mu.RLock()
	entry, found := backend.entries[key]
	backend.mu.RUnlock()
	if !found {
		return nil, false, nil
	}
	if !entry.expiresAt.IsZero() && !backend.now().Before(entry.expiresAt) {
		backend.mu.Lock()
		// A writer may have replaced the expired snapshot since RUnlock.
		if current, exists := backend.entries[key]; exists && !current.expiresAt.IsZero() && !backend.now().Before(current.expiresAt) {
			delete(backend.entries, key)
		}
		backend.mu.Unlock()
		return nil, false, nil
	}
	return append([]byte(nil), entry.value...), true, nil
}

func (backend *MemoryBackend) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry := memoryEntry{value: append([]byte(nil), value...)}
	if ttl > 0 {
		entry.expiresAt = backend.now().Add(ttl)
	}
	backend.mu.Lock()
	backend.entries[key] = entry
	backend.mu.Unlock()
	return nil
}

func (backend *MemoryBackend) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	backend.mu.Lock()
	delete(backend.entries, key)
	backend.mu.Unlock()
	return nil
}
