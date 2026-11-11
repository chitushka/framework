package otel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/observability"
	"github.com/chitushka/framework/queue"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func testProvider(t *testing.T, ratio float64) (*Provider, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	provider, err := New(Config{ServiceName: "test", SampleRatio: ratio}, exporter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return provider, exporter
}

func TestHTTPQueueSQLTraceAndRedaction(t *testing.T) {
	provider, exporter := testProvider(t, 1)
	broker := queue.NewMemoryBroker()
	jobs, err := queue.New(broker, provider)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := queue.NewWorker(broker, queue.WorkerConfig{Observer: provider, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	if err := worker.RegisterHandler("secret-job-user-id", func(ctx context.Context, _ json.RawMessage) error {
		provider.ObserveQuery(ctx, db.QueryEvent{Operation: "tx_query", Query: "SELECT secret", Args: []any{"secret"}, Err: errors.New("secret"), Duration: time.Millisecond})
		close(done)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	handler := observability.HTTP(observability.HTTPConfig{Tracer: provider})(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		traceID, _, ok := observability.SpanFromContext(request.Context())
		if !ok || traceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
			t.Error("bridge lost trace identifiers")
		}
		provider.ObserveQuery(request.Context(), db.QueryEvent{Operation: "exec", Query: "secret", Duration: time.Millisecond})
		if _, err := jobs.Push(request.Context(), "secret-job-user-id", map[string]string{"password": "secret"}, queue.Options{}); err != nil {
			t.Error(err)
		}
	}))
	request := httptest.NewRequest("GET", "/users/secret?token=secret", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	request.Header.Set("tracestate", "vendor=secret")
	request.Header.Set("baggage", "user=secret")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker blocked")
	}
	if err := worker.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := provider.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 5 {
		t.Fatalf("missing spans: %+v", spans)
	}
	byName := map[string]tracetest.SpanStub{}
	for _, span := range spans {
		if span.SpanContext.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" || span.SpanContext.TraceState().Len() != 0 {
			t.Fatal("broken propagation or leaked tracestate")
		}
		for _, attr := range span.Attributes {
			if strings.Contains(attr.Value.AsString(), "secret") {
				t.Fatal("PII attribute")
			}
		}
		if strings.Contains(span.Name+span.Status.Description, "secret") || len(span.Events) > 0 {
			t.Fatal("secret in exported span")
		}
		byName[span.Name] = span
	}
	if byName["queue process"].Parent.SpanID() != byName["queue publish"].SpanContext.SpanID() || byName["SQL tx_query"].Parent.SpanID() != byName["queue process"].SpanContext.SpanID() || byName["queue publish"].Parent.SpanID() != byName["HTTP GET"].SpanContext.SpanID() || byName["SQL tx_query"].Status.Code != codes.Error {
		t.Fatal("invalid trace tree or error status")
	}
}

func TestSamplingAndMalformedParent(t *testing.T) {
	for _, tc := range []struct {
		ratio   float64
		parent  string
		count   int
		sampled bool
	}{
		{0, "", 0, false}, {1, "", 1, true},
		{1, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00", 0, false},
		{0, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", 1, true},
		{1, "00-00000000000000000000000000000000-00f067aa0ba902b7-01", 1, true},
	} {
		provider, exporter := testProvider(t, tc.ratio)
		request := httptest.NewRequest("GET", "/", nil)
		request.Header.Set("traceparent", tc.parent)
		ctx, end := provider.StartHTTP(t.Context(), request)
		if trace.SpanContextFromContext(ctx).IsSampled() != tc.sampled {
			t.Fatal("sampling decision changed")
		}
		end(200, nil)
		end(500, "secret")
		if err := provider.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(exporter.GetSpans()) != tc.count {
			t.Fatal("sampling/export count")
		}
	}
}

type stalledExporter struct {
	entered, release chan struct{}
	once             sync.Once
	stopped          chan struct{}
}

func (exporter *stalledExporter) ExportSpans(ctx context.Context, _ []sdktrace.ReadOnlySpan) error {
	exporter.once.Do(func() { close(exporter.entered) })
	select {
	case <-exporter.release:
		return errors.New("credential-secret")
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (exporter *stalledExporter) Shutdown(context.Context) error {
	if exporter.stopped != nil {
		close(exporter.stopped)
	}
	return nil
}

func TestCollectorStallBoundsQueueLatencyAndGoroutines(t *testing.T) {
	exporter := &stalledExporter{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(exporter.release) }) }
	provider, err := New(Config{ServiceName: "stall", SampleRatio: 1, QueueSize: 8, BatchSize: 1, ExportTimeout: time.Second}, exporter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { release(); _ = provider.Shutdown(context.Background()) })
	handler := observability.HTTP(observability.HTTPConfig{Tracer: provider})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := httptest.NewRequest("CUSTOM-SECRET", "/secret", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	select {
	case <-exporter.entered:
	case <-time.After(time.Second):
		t.Fatal("export did not start")
	}
	before := runtime.NumGoroutine()
	started := time.Now()
	for range 5000 {
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	if time.Since(started) > 2*time.Second || runtime.NumGoroutine() > before+8 {
		t.Fatal("requests/export spawned unbounded work")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := provider.Flush(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unbounded flush: %v", err)
	}
	release()
	if err := provider.Flush(t.Context()); !errors.Is(err, observability.ErrExport) {
		t.Fatal("export error hidden", err)
	}
	stats := provider.Stats()
	if stats.Ended != 5001 || stats.Failed > 9 || stats.Unexported < 4990 || stats.ExportErrors == 0 {
		t.Fatal(stats)
	}
}

func TestOTLPHTTPAuthTLSAndRedirectPolicy(t *testing.T) {
	var received atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/traces" || request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Error("OTLP request contract")
		}
		received.Add(1)
		writer.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	provider, err := NewHTTP(t.Context(), Config{ServiceName: "tls", SampleRatio: 1}, HTTPConfig{Endpoint: server.URL + "/v1/traces", TLSConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, Headers: map[string]string{"Authorization": "Bearer secret"}})
	if err != nil {
		t.Fatal(err)
	}
	_, end := provider.StartHTTP(t.Context(), httptest.NewRequest("GET", "/", nil))
	end(200, nil)
	if err := provider.Shutdown(t.Context()); err != nil || received.Load() != 1 {
		t.Fatal("TLS export failed", err, received.Load())
	}
	var redirects atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirects.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	provider, err = NewHTTP(t.Context(), Config{ServiceName: "redirect", SampleRatio: 1}, HTTPConfig{Endpoint: source.URL + "/v1/traces", Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	_, end = provider.StartHTTP(t.Context(), httptest.NewRequest("GET", "/", nil))
	end(200, nil)
	if err := provider.Shutdown(t.Context()); !errors.Is(err, observability.ErrExport) || redirects.Load() != 0 {
		t.Fatal("redirect followed or error hidden", err)
	}
}

func TestConfigurationBudgets(t *testing.T) {
	for _, config := range []Config{{}, {ServiceName: "x", SampleRatio: math.NaN()}, {ServiceName: "x", SampleRatio: 2}, {ServiceName: "x", QueueSize: 8193}, {ServiceName: "x", QueueSize: -1}, {ServiceName: "x", QueueSize: 1, BatchSize: 2}, {ServiceName: "x", ExportTimeout: -1}, {ServiceName: "x", FlushTimeout: time.Minute}} {
		if _, err := New(config, tracetest.NewInMemoryExporter()); err == nil {
			t.Fatal("invalid config accepted", config)
		}
	}
	if _, err := New(Config{ServiceName: "x"}, nil); err == nil {
		t.Fatal("nil exporter")
	}
	for _, endpoint := range []string{"http://localhost/v1/traces", "https://user:secret@localhost/v1/traces", "https://localhost/v1/traces?secret=x", "https://localhost/wrong"} {
		if _, err := NewHTTP(t.Context(), Config{ServiceName: "x"}, HTTPConfig{Endpoint: endpoint}); err == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
}
