package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryBudgetsAndForgottenExpiry(t *testing.T) {
	b, err := NewMemoryBackendWithConfig(MemoryConfig{MaxEntries: 4, MaxBytes: 24})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	b.now = func() time.Time { return now }
	for i := 0; i < 1000; i++ {
		if err := b.Set(t.Context(), fmt.Sprintf("%d", i), []byte("data"), time.Second); err != nil {
			t.Fatal(err)
		}
		stats := b.Stats()
		if stats.Entries > 4 || stats.Bytes > 24 {
			t.Fatalf("unbounded cache: %+v", stats)
		}
	}
	if err := b.Set(t.Context(), "oversize", make([]byte, 25), 0); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if err := b.PurgeExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stats := b.Stats(); stats.Entries != 0 || stats.Bytes != 0 {
		t.Fatalf("forgotten entries retained: %+v", stats)
	}
}

func TestGetOrLoadCoalescesConcurrentMisses(t *testing.T) {
	s, err := New(NewMemoryBackend(), "flight")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var group sync.WaitGroup
	ready, release := make(chan struct{}), make(chan struct{})
	loader := func(context.Context) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(ready)
		}
		<-release
		return []byte("loaded"), nil
	}
	for i := 0; i < 50; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			value, err := s.GetOrLoad(t.Context(), "key", Options{TTL: time.Minute}, loader)
			if err != nil || string(value) != "loaded" {
				t.Errorf("load=%q %v", value, err)
			}
		}()
	}
	<-ready
	close(release)
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("stampede: %d loads", calls.Load())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.GetOrLoad(ctx, "other", Options{}, loader); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestMemcachedRejectsWireInjectionAndOversize(t *testing.T) {
	b, err := NewMemcachedBackend(MemcachedConfig{Address: "127.0.0.1:1", MaxValueBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Get(t.Context(), "x\r\ndelete y"); err == nil {
		t.Fatal("wire injection accepted")
	}
	if err := b.Set(t.Context(), "x", make([]byte, 11), 0); err == nil {
		t.Fatal("oversize accepted")
	}
	if ttl := memcachedExpiration(time.Duration(1<<63-1), time.Unix(100, 0)); ttl <= 100 {
		t.Fatalf("duration overflow: %d", ttl)
	}
}
