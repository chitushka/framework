package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryExpirationPreservesConcurrentReplacement(t *testing.T) {
	for _, ttl := range []time.Duration{0, time.Hour} {
		t.Run(ttl.String(), func(t *testing.T) {
			backend := NewMemoryBackend()
			now := time.Unix(100, 0)
			backend.now = func() time.Time { return now }
			if err := backend.Set(t.Context(), "key", []byte("expired"), time.Second); err != nil {
				t.Fatal(err)
			}

			snapshot := make(chan struct{})
			resume := make(chan struct{})
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(resume) }) })
			var clockCalls atomic.Int32
			backend.now = func() time.Time {
				if clockCalls.Add(1) == 1 {
					close(snapshot)
					<-resume
				}
				return now.Add(2 * time.Second)
			}
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_, _, _ = backend.Get(context.Background(), "key")
			}()
			select {
			case <-snapshot:
			case <-time.After(time.Second):
				t.Fatal("reader did not reach expiration check")
			}
			if err := backend.Set(t.Context(), "key", []byte("fresh"), ttl); err != nil {
				t.Fatal(err)
			}
			release.Do(func() { close(resume) })
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("expiration check did not finish")
			}
			value, found, err := backend.Get(t.Context(), "key")
			if err != nil || !found || string(value) != "fresh" {
				t.Fatalf("concurrent replacement was removed: value=%q, found=%t, err=%v", value, found, err)
			}
		})
	}
}
