package web_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/web"
)

func healthStatus(handler http.HandlerFunc, method string) *httptest.ResponseRecorder {
	r := httptest.NewRecorder()
	handler(r, httptest.NewRequest(method, "/", nil))
	return r
}

func TestHealthLifecycleAndDependencyIsolation(t *testing.T) {
	a := app.New(nil, nil)
	var failed atomic.Bool
	h, err := web.NewHealth(a, web.HealthConfig{Checks: []func(context.Context) error{func(context.Context) error {
		if failed.Load() {
			return errors.New("secret DSN")
		}
		return nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if healthStatus(h.Readiness, "GET").Code != 503 || healthStatus(h.Liveness, "GET").Code != 200 {
		t.Fatal("startup health")
	}
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if healthStatus(h.Readiness, "GET").Code != 200 {
		t.Fatal("running health")
	}
	failed.Store(true)
	r := healthStatus(h.Readiness, "GET")
	if r.Code != 503 || strings.Contains(r.Body.String(), "secret") || r.Header().Get("Cache-Control") != "no-store" || healthStatus(h.Liveness, "GET").Code != 200 {
		t.Fatal("dependency liveness/privacy")
	}
	if r := healthStatus(h.Readiness, "HEAD"); r.Code != 503 || r.Body.Len() != 0 {
		t.Fatal("HEAD")
	}
	if r := healthStatus(h.Readiness, "POST"); r.Code != 405 || r.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal("method policy")
	}
	if err := a.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if healthStatus(h.Liveness, "GET").Code != 503 {
		t.Fatal("stopped liveness")
	}
}

func TestHealthBoundsUncooperativeProbesGlobally(t *testing.T) {
	a := app.New(nil, nil)
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	h, err := web.NewHealth(a, web.HealthConfig{ProbeTimeout: 10 * time.Millisecond, MaxConcurrentProbes: 2, Checks: []func(context.Context) error{func(context.Context) error { calls.Add(1); <-release; return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for range 100 {
		if healthStatus(h.Readiness, "GET").Code != 503 {
			t.Fatal("hung probe passed")
		}
	}
	if calls.Load() != 2 || time.Since(start) > time.Second {
		t.Fatalf("unbounded probes: %d %v", calls.Load(), time.Since(start))
	}
}

func TestHealthDeadlineCancellationPanicAndDrainingRecheck(t *testing.T) {
	for _, kind := range []string{"panic", "cancel", "drain", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			a := app.New(nil, nil)
			if err := a.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			h, err := web.NewHealth(a, web.HealthConfig{ProbeTimeout: 5 * time.Millisecond, Checks: []func(context.Context) error{func(probe context.Context) error {
				switch kind {
				case "panic":
					panic("secret")
				case "cancel":
					cancel()
				case "drain":
					return a.Shutdown(t.Context())
				case "deadline":
					<-probe.Done()
					return probe.Err()
				}
				return nil
			}}})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRecorder()
			h.Readiness(r, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
			if r.Code != 503 {
				t.Fatal("probe failed open")
			}
		})
	}
}

func TestHealthValidation(t *testing.T) {
	a := app.New(nil, nil)
	if _, err := web.NewHealth(nil, web.HealthConfig{}); err == nil {
		t.Fatal("nil app")
	}
	for _, config := range []web.HealthConfig{{ProbeTimeout: -1}, {MaxConcurrentProbes: -1}, {MaxConcurrentProbes: 65}, {Checks: []func(context.Context) error{nil}}, {Checks: make([]func(context.Context) error, 33)}} {
		if _, err := web.NewHealth(a, config); err == nil {
			t.Fatal("invalid config")
		}
	}
}
