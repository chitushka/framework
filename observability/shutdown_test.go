package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLegacyShutdownCancelsOutstandingExportAndReleasesQueue(t *testing.T) {
	exporter := &blockedExporter{started: make(chan struct{}), release: make(chan struct{})}
	tracer, err := NewTracer(exporter)
	if err != nil {
		t.Fatal(err)
	}
	handler := HTTP(HTTPConfig{Tracer: tracer})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := httptest.NewRequest("GET", "/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	select {
	case <-exporter.started:
	case <-time.After(time.Second):
		t.Fatal("export not started")
	}
	for range 100 {
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := tracer.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shutdown budget ignored", err)
	}
	select {
	case <-tracer.done:
	case <-time.After(time.Second):
		t.Fatal("export worker leaked after deadline")
	}
	if stats := tracer.Stats(); stats.Pending != 0 || stats.Dropped != 100 || stats.Failures != 1 {
		t.Fatal(stats)
	}
}
