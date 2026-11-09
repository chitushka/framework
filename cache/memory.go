package cache

import (
	"context"
	"errors"
	"sync"
	"time"
)

type memoryEntry struct {
	value     []byte
	expiresAt time.Time
	sequence  uint64
}

type MemoryConfig struct {
	MaxEntries int
	MaxBytes   int64
}
type MemoryStats struct {
	Entries int
	Bytes   int64
}

var ErrCapacity = errors.New("cache: capacity exceeded")

// MemoryBackend is a concurrency-safe in-process backend with lazy expiration.
type MemoryBackend struct {
	mu       sync.RWMutex
	entries  map[string]memoryEntry
	now      func() time.Time
	config   MemoryConfig
	bytes    int64
	sequence uint64
}

// NewMemoryBackend creates an empty memory backend.
func NewMemoryBackend() *MemoryBackend {
	backend, _ := NewMemoryBackendWithConfig(MemoryConfig{})
	return backend
}

func NewMemoryBackendWithConfig(config MemoryConfig) (*MemoryBackend, error) {
	if config.MaxEntries == 0 {
		config.MaxEntries = 10000
	}
	if config.MaxBytes == 0 {
		config.MaxBytes = 64 << 20
	}
	if config.MaxEntries < 1 || config.MaxBytes < 1 {
		return nil, errors.New("cache: memory limits must be positive")
	}
	return &MemoryBackend{entries: make(map[string]memoryEntry), now: time.Now, config: config}, nil
}
func (b *MemoryBackend) remove(key string) {
	if e, ok := b.entries[key]; ok {
		b.bytes -= int64(len(key) + len(e.value))
		delete(b.entries, key)
	}
}
func (b *MemoryBackend) purge(now time.Time) {
	for key, e := range b.entries {
		if !e.expiresAt.IsZero() && !now.Before(e.expiresAt) {
			b.remove(key)
		}
	}
}
func (b *MemoryBackend) PurgeExpired(ctx context.Context) error {
	if ctx == nil {
		return errors.New("cache: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.purge(b.now())
	return nil
}
func (b *MemoryBackend) Stats() MemoryStats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return MemoryStats{len(b.entries), b.bytes}
}

func (backend *MemoryBackend) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if ctx == nil {
		return nil, false, errors.New("cache: context is nil")
	}
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
			backend.remove(key)
		}
		backend.mu.Unlock()
		return nil, false, nil
	}
	return append([]byte(nil), entry.value...), true, nil
}

func (backend *MemoryBackend) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ctx == nil {
		return errors.New("cache: context is nil")
	}
	if ttl < 0 {
		return errors.New("cache: negative TTL")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if int64(len(key)+len(value)) > backend.config.MaxBytes {
		return ErrCapacity
	}
	entry := memoryEntry{value: append([]byte(nil), value...)}
	if ttl > 0 {
		entry.expiresAt = backend.now().Add(ttl)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	backend.sequence++
	if backend.sequence%64 == 0 || len(backend.entries) >= backend.config.MaxEntries || backend.bytes+int64(len(key)+len(value)) > backend.config.MaxBytes {
		backend.purge(backend.now())
	}
	backend.remove(key)
	for len(backend.entries) >= backend.config.MaxEntries || backend.bytes+int64(len(key)+len(value)) > backend.config.MaxBytes {
		var oldest string
		sequence := ^uint64(0)
		for k, e := range backend.entries {
			if e.sequence < sequence {
				oldest, sequence = k, e.sequence
			}
		}
		backend.remove(oldest)
	}
	entry.sequence = backend.sequence
	backend.entries[key] = entry
	backend.bytes += int64(len(key) + len(value))
	return nil
}

func (backend *MemoryBackend) Delete(ctx context.Context, key string) error {
	if ctx == nil {
		return errors.New("cache: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	backend.mu.Lock()
	backend.remove(key)
	backend.mu.Unlock()
	return nil
}
