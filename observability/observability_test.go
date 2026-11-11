package observability_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitushka/framework/observability"
)

type spanExporter struct {
	mu    sync.Mutex
	spans []observability.Span
}

func (exporter *spanExporter) ExportSpan(_ context.Context, span observability.Span) error {
	exporter.mu.Lock()
	exporter.spans = append(exporter.spans, span)
	exporter.mu.Unlock()
	return nil
}

func TestHTTPLogsMetricsAndPropagatesTrace(t *testing.T) {
	metrics, err := observability.NewMetrics(time.Millisecond, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	exporter := &spanExporter{}
	tracer, err := observability.NewTracer(exporter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tracer.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	var logs bytes.Buffer
	middleware := observability.HTTP(observability.HTTPConfig{
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Metrics: metrics, Tracer: tracer,
	})
	handler := middleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		traceID, spanID, ok := observability.SpanFromContext(request.Context())
		if !ok || len(traceID) != 32 || len(spanID) != 16 {
			t.Errorf("missing active span: %q %q", traceID, spanID)
		}
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte("created"))
	}))
	request := httptest.NewRequest(http.MethodPost, "/orders", nil)
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if err := tracer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusCreated || !strings.HasPrefix(response.Header().Get("traceparent"), "00-4bf92f3577b34da6a3ce929d0e0e4736-") {
		t.Fatalf("unexpected traced response: %d %s", response.Code, response.Header())
	}
	if !strings.Contains(logs.String(), `"status":201`) || !strings.Contains(logs.String(), `"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"`) {
		t.Fatalf("unexpected log: %s", logs.String())
	}
	if len(exporter.spans) != 1 || exporter.spans[0].ParentSpanID != "00f067aa0ba902b7" || exporter.spans[0].Attributes["http.status_code"] != "201" {
		t.Fatalf("unexpected exported spans: %#v", exporter.spans)
	}
	metricsResponse := httptest.NewRecorder()
	metrics.ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metricsResponse.Body.String()
	for _, expected := range []string{`chitu_http_requests_total{method="POST",status="201"} 1`, `chitu_http_request_duration_seconds_count 1`, `chitu_http_requests_active 0`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics do not contain %q:\n%s", expected, body)
		}
	}
}

func TestHTTPRecordsErrorsAndPanics(t *testing.T) {
	metrics, _ := observability.NewMetrics()
	exporter := &spanExporter{}
	tracer, _ := observability.NewTracer(exporter)
	t.Cleanup(func() {
		if err := tracer.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	var logs bytes.Buffer
	handler := observability.HTTP(observability.HTTPConfig{Metrics: metrics, Tracer: tracer, Logger: slog.New(slog.NewTextHandler(&logs, nil))})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	func() {
		defer func() {
			if recover() != "boom" {
				t.Fatal("panic was not propagated")
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panic", nil))
	}()
	if err := tracer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(exporter.spans) != 1 || exporter.spans[0].Error != "boom" || !strings.Contains(logs.String(), "status=500") {
		t.Fatalf("panic not observed: %#v %s", exporter.spans, logs.String())
	}
	response := httptest.NewRecorder()
	metrics.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), `chitu_http_errors_total{method="GET",status="500"} 1`) {
		t.Fatalf("missing error metric: %s", response.Body.String())
	}
}

func TestValidationPprofAndRemoteAddress(t *testing.T) {
	if _, err := observability.NewMetrics(time.Second, time.Millisecond); err == nil {
		t.Fatal("expected bucket order error")
	}
	if _, err := observability.NewMetrics(0); err == nil {
		t.Fatal("expected zero bucket error")
	}
	if _, err := observability.NewTracer(nil); err == nil {
		t.Fatal("expected nil exporter error")
	}
	var nilMetrics *observability.Metrics
	response := httptest.NewRecorder()
	nilMetrics.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected nil metrics status: %d", response.Code)
	}
	response = httptest.NewRecorder()
	observability.PprofHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "profile") {
		t.Fatalf("unexpected pprof response: %d", response.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	if observability.RemoteAddress(request) != "192.0.2.1" || observability.RemoteAddress(nil) != "" {
		t.Fatal("unexpected remote address")
	}
}

func TestHTTPPreservesStreamingInterfaces(t *testing.T) {
	metrics, _ := observability.NewMetrics()
	handler := observability.HTTP(observability.HTTPConfig{Metrics: metrics})(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Fatal("http.Flusher was lost")
		}
		flusher.Flush()
		_, _ = io.Copy(writer, strings.NewReader("stream"))
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/stream", nil))
	if response.Body.String() != "stream" {
		t.Fatalf("unexpected stream: %q", response.Body.String())
	}
}
