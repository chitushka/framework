package otel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chitushka/framework/observability"
)

func TestSDKShutdownCancelsBatchDrainAfterBudget(t *testing.T) {
	exporter := &stalledExporter{entered: make(chan struct{}), release: make(chan struct{})}
	provider, err := New(Config{ServiceName: "shutdown", SampleRatio: 1, QueueSize: 8, BatchSize: 1}, exporter)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	_, end := provider.StartHTTP(t.Context(), request)
	end(200, nil)
	select {
	case <-exporter.entered:
	case <-time.After(time.Second):
		t.Fatal("export not started")
	}
	for range 8 {
		_, end := provider.StartHTTP(t.Context(), request)
		end(200, nil)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := provider.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shutdown budget ignored", err)
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for provider.Stats().Failed != 9 {
		select {
		case <-deadline.C:
			t.Fatal("batch drain retained export work", provider.Stats())
		case <-ticker.C:
		}
	}
}

func TestCancelledShutdownStillStopsSDKProcessor(t *testing.T) {
	exporter := &stalledExporter{entered: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	provider, err := New(Config{ServiceName: "cancelled"}, exporter)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := provider.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("missing cancelled shutdown", err)
	}
	select {
	case <-exporter.stopped:
	case <-time.After(time.Second):
		t.Fatal("SDK processor leaked after cancelled shutdown")
	}
}

func TestOTLPOutageAndRecoveryHaveFiniteBudgets(t *testing.T) {
	var failed atomic.Bool
	failed.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failed.Load() {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(strings.Repeat("private-secret", 100000)))
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	provider, err := NewHTTP(t.Context(), Config{ServiceName: "outage", SampleRatio: 1, ExportTimeout: 100 * time.Millisecond}, HTTPConfig{Endpoint: server.URL + "/v1/traces", Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	request := httptest.NewRequest("GET", "/", nil)
	_, end := provider.StartHTTP(t.Context(), request)
	end(200, nil)
	if err := provider.Flush(t.Context()); !errors.Is(err, observability.ErrExport) || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("unsafe/hidden export error", err)
	}
	failed.Store(false)
	_, end = provider.StartHTTP(t.Context(), request)
	end(200, nil)
	_ = provider.Flush(t.Context())
	if stats := provider.Stats(); stats.Exported != 1 || stats.Failed != 1 {
		t.Fatal("export did not recover", stats)
	}
}
