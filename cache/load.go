package cache

import (
	"context"
	"errors"
)

type loadFlight struct {
	done  chan struct{}
	value []byte
	err   error
}

// GetOrLoad coalesces misses within this Store instance. Distributed exclusion
// is a separate contract: loaders must remain idempotent across processes.
// The leader's context bounds the loader; waiters may cancel independently.
func (s *Store) GetOrLoad(ctx context.Context, key string, options Options, loader func(context.Context) ([]byte, error)) ([]byte, error) {
	if loader == nil {
		return nil, errors.New("cache: loader is nil")
	}
	value, found, err := s.Get(ctx, key)
	if err != nil || found {
		return value, err
	}
	s.mu.Lock()
	if s.flights == nil {
		s.flights = make(map[string]*loadFlight)
	}
	if pending, exists := s.flights[key]; exists {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pending.done:
			return append([]byte(nil), pending.value...), pending.err
		}
	}
	if len(s.flights) >= 1024 {
		s.mu.Unlock()
		return nil, ErrCapacity
	}
	pending := &loadFlight{done: make(chan struct{})}
	s.flights[key] = pending
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.flights, key); close(pending.done); s.mu.Unlock() }()
	// Recheck after becoming leader: a previous leader may have just populated it.
	pending.value, found, pending.err = s.Get(ctx, key)
	if pending.err == nil && !found {
		pending.value, pending.err = loader(ctx)
		if pending.err == nil {
			pending.err = s.Set(ctx, key, pending.value, options)
		}
	}
	return append([]byte(nil), pending.value...), pending.err
}
