package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockedExporter struct {
	started, release chan struct{}
	once             sync.Once
}

func (exporter *blockedExporter) ExportSpan(ctx context.Context, _ Span) error {
	exporter.once.Do(func() { close(exporter.started) })
	select {
	case <-exporter.release:
		return errors.New("private credential")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestLegacyExportIsBoundedAndFlushHasDeadline(t *testing.T) {
	exporter := &blockedExporter{started: make(chan struct{}), release: make(chan struct{})}
	tracer, err := NewTracer(exporter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(exporter.release); _ = tracer.Shutdown(context.Background()) })
	handler := HTTP(HTTPConfig{Tracer: tracer})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := httptest.NewRequest("GET", "/"+strings.Repeat("x", 10000), nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	select {
	case <-exporter.started:
	case <-time.After(time.Second):
		t.Fatal("export did not start")
	}
	started := time.Now()
	for range 2000 {
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("collector blocked requests")
	}
	stats := tracer.Stats()
	if stats.Pending != 256 || stats.Dropped == 0 || stats.Accepted != 257 {
		t.Fatalf("unbounded queue: %+v", stats)
	}
	tracer.mu.Lock()
	if len(tracer.pending[0].Name) > 256 || len(tracer.pending[0].Attributes["http.path"]) > 256 {
		t.Fatal("unbounded span bytes")
	}
	tracer.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := tracer.Flush(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("flush deadline: %v", err)
	}
}

func TestLegacySamplingAndExportFailure(t *testing.T) {
	exporter := &blockedExporter{started: make(chan struct{}), release: make(chan struct{})}
	close(exporter.release)
	tracer, err := NewTracer(exporter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracer.Shutdown(context.Background()) })
	handler := HTTP(HTTPConfig{Tracer: tracer})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, flags := range []string{"00", "01", "03"} {
		request := httptest.NewRequest("GET", "/", nil)
		request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-"+flags)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if flags == "00" && !strings.HasSuffix(response.Header().Get("traceparent"), "-00") {
			t.Fatal("sampling bit changed")
		}
	}
	if err := tracer.Flush(t.Context()); !errors.Is(err, ErrExport) {
		t.Fatalf("hidden exporter failure: %v", err)
	}
	if stats := tracer.Stats(); stats.Accepted != 2 || stats.Failures != 2 {
		t.Fatal(stats)
	}
}
