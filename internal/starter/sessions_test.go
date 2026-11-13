package starter

import (
	"errors"
	"github.com/chitushka/framework/web"
	"testing"
	"time"
)

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestBoundedSessionsCapacityRotationExpiryAndStaleCAS(t *testing.T) {
	b := NewSessionBackend(1)
	ctx := t.Context()
	check(t, b.Save(ctx, "old", []byte("snapshot"), time.Minute))
	if err := b.Save(ctx, "other", []byte("x"), time.Minute); err == nil {
		t.Fatal("unbounded sessions")
	}
	ok, err := b.CompareAndSwap(ctx, "old", []byte("stale"), "new", []byte("value"), time.Minute)
	check(t, err)
	if ok {
		t.Fatal("stale CAS accepted")
	}
	ok, err = b.CompareAndSwap(ctx, "old", []byte("snapshot"), "new", []byte("value"), time.Minute)
	check(t, err)
	if !ok {
		t.Fatal("rotation rejected at capacity")
	}
	if _, exists, err := b.Load(ctx, "old"); err != nil || exists {
		t.Fatal("old ID not revoked")
	}
	_, err = b.CompareAndSwap(ctx, "other", nil, "another", []byte("x"), time.Minute)
	var status *web.HTTPError
	if !errors.As(err, &status) {
		t.Fatal("missing capacity error")
	}
	check(t, b.Delete(ctx, "new"))
	check(t, b.Save(ctx, "expires", []byte("x"), time.Millisecond))
	time.Sleep(3 * time.Millisecond)
	check(t, b.Save(ctx, "next", []byte("y"), time.Minute))
	if _, exists, err := b.Load(ctx, "expires"); err != nil || exists {
		t.Fatal("expired ID retained")
	}
}
