package starter

import (
	"context"
	"sync"
	"time"

	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

// Bound the process-local store and reclaim expired IDs even when a browser
// never returns. All backend operations participate in the same outer lock.
type SessionBackend struct {
	mu      sync.Mutex
	backend *session.MemoryBackend
	expires map[string]time.Time
	limit   int
}

func NewSessionBackend(limit int) *SessionBackend {
	return &SessionBackend{backend: session.NewMemoryBackend(), expires: make(map[string]time.Time), limit: limit}
}

func (b *SessionBackend) prune(ctx context.Context) error {
	now := time.Now()
	for id, until := range b.expires {
		if !now.Before(until) {
			if err := b.backend.Delete(ctx, id); err != nil {
				return err
			}
			delete(b.expires, id)
		}
	}
	return ctx.Err()
}

func (b *SessionBackend) capacity(newID, oldID string) error {
	_, exists := b.expires[newID]
	_, replacing := b.expires[oldID]
	if newID != "" && !exists && !replacing && len(b.expires) >= b.limit {
		return web.NewHTTPError(503, "Session capacity reached; try again later", nil)
	}
	return nil
}

func (b *SessionBackend) Load(ctx context.Context, id string) ([]byte, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.prune(ctx); err != nil {
		return nil, false, err
	}
	return b.backend.Load(ctx, id)
}
func (b *SessionBackend) Save(ctx context.Context, id string, value []byte, ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.prune(ctx); err != nil {
		return err
	}
	if err := b.capacity(id, ""); err != nil {
		return err
	}
	if err := b.backend.Save(ctx, id, value, ttl); err != nil {
		return err
	}
	b.expires[id] = time.Now().Add(ttl)
	return nil
}
func (b *SessionBackend) Delete(ctx context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.backend.Delete(ctx, id); err != nil {
		return err
	}
	delete(b.expires, id)
	return nil
}
func (b *SessionBackend) CompareAndSwap(ctx context.Context, oldID string, expected []byte, newID string, value []byte, ttl time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.prune(ctx); err != nil {
		return false, err
	}
	if err := b.capacity(newID, oldID); err != nil {
		return false, err
	}
	ok, err := b.backend.CompareAndSwap(ctx, oldID, expected, newID, value, ttl)
	if err != nil || !ok {
		return ok, err
	}
	delete(b.expires, oldID)
	if newID != "" {
		b.expires[newID] = time.Now().Add(ttl)
	}
	return true, nil
}
