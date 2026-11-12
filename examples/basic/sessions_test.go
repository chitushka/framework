package main

import (
	"errors"
	"testing"
	"time"

	"github.com/chitushka/framework/web"
)

func TestBoundedSessionsCapacityRotationExpiryAndStaleCAS(t *testing.T) {
	b := newBoundedSessions(1)
	ctx := t.Context()
	must(t, b.Save(ctx, "old", []byte("snapshot"), time.Minute))
	if err := b.Save(ctx, "other", []byte("x"), time.Minute); err == nil {
		t.Fatal("unbounded sessions")
	}
	ok, err := b.CompareAndSwap(ctx, "old", []byte("stale"), "new", []byte("value"), time.Minute)
	must(t, err)
	if ok {
		t.Fatal("stale CAS accepted")
	}
	ok, err = b.CompareAndSwap(ctx, "old", []byte("snapshot"), "new", []byte("value"), time.Minute)
	must(t, err)
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
	must(t, b.Delete(ctx, "new"))
	must(t, b.Save(ctx, "expires", []byte("x"), time.Millisecond))
	time.Sleep(3 * time.Millisecond)
	must(t, b.Save(ctx, "next", []byte("y"), time.Minute))
	if _, exists, err := b.Load(ctx, "expires"); err != nil || exists {
		t.Fatal("expired ID retained")
	}
}

func TestSessionCapacityFailsClosedOverHTTP(t *testing.T) {
	c := fixture(t)
	c.MaxSessions = 1
	s := testSite(t, c)
	first := call(t, s, "GET", "/login", "", "", "", nil)
	if first.Code != 200 {
		t.Fatal("first session")
	}
	if call(t, s, "GET", "/login", "", "", "", nil).Code != 503 {
		t.Fatal("capacity did not fail closed")
	}
	if call(t, s, "GET", "/login", "", "", "", cookieOf(t, first)).Code != 200 {
		t.Fatal("existing session blocked")
	}
}
