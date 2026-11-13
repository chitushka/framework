package main

import "testing"

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
