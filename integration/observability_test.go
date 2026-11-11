package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	telemetry "github.com/chitushka/framework/adapters/otel"
	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/observability"
	"github.com/chitushka/framework/queue"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func collectorProvider(t *testing.T, name string) *telemetry.Provider {
	t.Helper()
	provider, err := telemetry.NewHTTP(testContext(t), telemetry.Config{ServiceName: name, SampleRatio: 1}, telemetry.HTTPConfig{Endpoint: required(t, "OTEL_TEST_ENDPOINT"), Insecure: true})
	check(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		check(t, provider.Shutdown(ctx))
	})
	return provider
}

func waitCollector(t *testing.T) {
	t.Helper()
	ctx := testContext(t)
	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, required(t, "OTEL_TEST_HEALTH"), nil)
		check(t, err)
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("collector not ready", ctx.Err())
		case <-ticker.C:
		}
	}
}

// OTLP JSON использует hex для IDs, в отличие от protobuf JSON base64.
type collectedSpan struct {
	TraceID      string          `json:"traceId"`
	SpanID       string          `json:"spanId"`
	ParentSpanID string          `json:"parentSpanId"`
	Name         string          `json:"name"`
	Attributes   json.RawMessage `json:"attributes"`
	Status       json.RawMessage `json:"status"`
}

type collectedTraces struct {
	ResourceSpans []struct {
		ScopeSpans []struct {
			Spans []*collectedSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

func TestCollectorJSONUsesHexIdentifiers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traces.json")
	const data = `{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"00f067aa0ba902b7","parentSpanId":"0011223344556677","name":"HTTP POST"}]}]}]}`
	check(t, os.WriteFile(path, []byte(data+"\n"), 0600))
	t.Setenv("OTEL_TEST_FILE", path)
	spans := collectorSpans(t, "4bf92f3577b34da6a3ce929d0e0e4736", 1)
	if len(spans) != 1 || spans[0].SpanID != "00f067aa0ba902b7" || spans[0].ParentSpanID != "0011223344556677" {
		t.Fatal("OTLP hex IDs changed", spans)
	}
}

func collectorSpans(t *testing.T, traceID string, count int) []*collectedSpan {
	t.Helper()
	ctx := testContext(t)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		var spans []*collectedSpan
		file, err := os.Open(required(t, "OTEL_TEST_FILE"))
		if err == nil {
			scanner := bufio.NewScanner(io.LimitReader(file, 8<<20))
			scanner.Buffer(make([]byte, 64<<10), 1<<20)
			for scanner.Scan() {
				var request collectedTraces
				// Последняя строка может ещё записываться file exporter.
				if json.Unmarshal(scanner.Bytes(), &request) != nil {
					continue
				}
				for _, resource := range request.ResourceSpans {
					for _, scope := range resource.ScopeSpans {
						for _, span := range scope.Spans {
							if span.TraceID == traceID {
								spans = append(spans, span)
							}
						}
					}
				}
			}
			check(t, scanner.Err())
			check(t, file.Close())
		}
		if len(spans) >= count {
			return spans
		}
		select {
		case <-ctx.Done():
			t.Fatalf("collector received %d/%d spans for %s", len(spans), count, traceID)
		case <-ticker.C:
		}
	}
}

func assertTraceTree(t *testing.T, spans []*collectedSpan, want int) {
	t.Helper()
	if len(spans) != want {
		t.Fatalf("collector span count=%d, want=%d", len(spans), want)
	}
	ids := map[string]*collectedSpan{}
	var producer, consumer *collectedSpan
	for _, span := range spans {
		ids[span.SpanID] = span
		if span.Name == "queue publish" {
			producer = span
		}
		if span.Name == "queue process" {
			consumer = span
		}
		encoded, err := json.Marshal(span)
		check(t, err)
		if bytes.Contains(encoded, []byte("private-secret")) {
			t.Fatal("credentials/PII reached collector")
		}
	}
	if producer == nil || consumer == nil || consumer.ParentSpanID != producer.SpanID {
		t.Fatal("queue did not propagate traceparent")
	}
	if parent := ids[producer.ParentSpanID]; parent == nil || parent.Name != "HTTP POST" {
		t.Fatal("producer lost HTTP parent")
	}
	for _, span := range spans {
		if strings.HasPrefix(span.Name, "SQL ") && ids[span.ParentSpanID] == nil {
			t.Fatal("orphan SQL span")
		}
	}
}

func TestCollectorHTTPWorkerSQL(t *testing.T) {
	waitCollector(t)
	sqlBaseline(t, func(t *testing.T, connection *db.Connection) {
		for _, kind := range []string{"redis", "rabbitmq", "sql"} {
			t.Run(kind, func(t *testing.T) {
				ctx := testContext(t)
				var broker queue.Broker
				name := unique()
				if kind == "sql" {
					b, err := queue.NewDatabaseBroker(queue.DatabaseConfig{DB: connection.DB(), Dialect: connection.Dialect(), Table: name, PollInterval: time.Millisecond, TraceContext: true})
					check(t, err)
					_, err = connection.ExecContext(ctx, b.SchemaSQL())
					check(t, err)
					legacy, err := queue.NewDatabaseBroker(queue.DatabaseConfig{DB: connection.DB(), Dialect: connection.Dialect(), Table: name})
					check(t, err)
					check(t, legacy.Enqueue(ctx, queue.Message{ID: unique(), Name: "legacy", Payload: json.RawMessage(`null`), MaxAttempts: 1, CreatedAt: time.Now()}, 0))
					old, err := b.Reserve(ctx, 0)
					check(t, err)
					if old.Message().TraceParent != "" || old.Message().TraceState != "" {
						t.Fatal("legacy SQL NULL metadata changed")
					}
					check(t, old.Ack(ctx))
					t.Cleanup(func() {
						_, err := connection.ExecContext(context.Background(), "DROP TABLE "+connection.Dialect().QuoteIdentifier(name))
						check(t, err)
					})
					broker = b
				} else {
					var cleanup func()
					broker, cleanup = persistentBroker(t, kind, name, unique())
					t.Cleanup(cleanup)
				}
				appProvider, workerProvider := collectorProvider(t, "test-app"), collectorProvider(t, "test-worker")
				jobs, err := queue.New(broker, appProvider)
				check(t, err)
				worker, err := queue.NewWorker(broker, queue.WorkerConfig{Observer: workerProvider, PollInterval: 5 * time.Millisecond, RetryDelay: func(int) time.Duration { return 0 }})
				check(t, err)
				var calls atomic.Int64
				done := make(chan struct{})
				check(t, worker.RegisterHandler("private-secret-job", func(ctx context.Context, _ json.RawMessage) error {
					_, err := connection.ExecContext(ctx, "SELECT 1 /* private-secret */")
					if err != nil {
						return err
					}
					if calls.Add(1) == 1 {
						return fmt.Errorf("private-secret retry")
					}
					close(done)
					return nil
				}))
				check(t, worker.Start(ctx))
				t.Cleanup(func() { check(t, worker.Shutdown(context.Background())) })
				traceID := strings.Repeat("a", 16) + unique()[3:]
				handler := observability.HTTP(observability.HTTPConfig{Tracer: appProvider})(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
					_, err := connection.ExecContext(request.Context(), "SELECT 1 /* private-secret */")
					check(t, err)
					_, err = jobs.Push(request.Context(), "private-secret-job", map[string]string{"password": "private-secret"}, queue.Options{})
					check(t, err)
				}))
				server := httptest.NewServer(handler)
				defer server.Close()
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/private-secret?token=private-secret", nil)
				check(t, err)
				request.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
				response, err := server.Client().Do(request)
				check(t, err)
				check(t, response.Body.Close())
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				check(t, worker.Shutdown(ctx))
				check(t, appProvider.Flush(ctx))
				check(t, workerProvider.Flush(ctx))
				// HTTP, producer, два consumer attempts, три SQL calls.
				spans := collectorSpans(t, traceID, 7)
				// Обе доставки должны быть детьми одного producer.
				assertTraceTree(t, spans, 7)
			})
		}
	})
}

func TestTelemetryWorkerProcess(t *testing.T) {
	if os.Getenv("CHITU_TELEMETRY_CHILD") != "1" {
		return
	}
	ctx := testContext(t)
	provider := collectorProvider(t, "separate-worker")
	broker, cleanup := persistentBroker(t, "redis", required(t, "CHITU_TELEMETRY_QUEUE"), required(t, "CHITU_TELEMETRY_NAMESPACE"))
	defer cleanup()
	connection, err := db.Open(ctx, db.Config{Driver: "pgx", DSN: required(t, "POSTGRES_DSN"), Dialect: db.PostgreSQL}, db.NewDefaultDialectRegistry())
	check(t, err)
	defer func() { check(t, connection.Close()) }()
	worker, err := queue.NewWorker(broker, queue.WorkerConfig{Observer: provider, PollInterval: 5 * time.Millisecond})
	check(t, err)
	done := make(chan struct{})
	check(t, worker.RegisterHandler("job", func(ctx context.Context, _ json.RawMessage) error {
		_, err := connection.ExecContext(ctx, "SELECT 1")
		close(done)
		return err
	}))
	check(t, worker.Start(ctx))
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	check(t, worker.Shutdown(ctx))
	check(t, provider.Flush(ctx))
}

func TestCollectorAcrossProcesses(t *testing.T) {
	waitCollector(t)
	ctx := testContext(t)
	name, namespace := unique(), unique()
	broker, cleanup := persistentBroker(t, "redis", name, namespace)
	defer cleanup()
	provider := collectorProvider(t, "separate-http")
	jobs, err := queue.New(broker, provider)
	check(t, err)
	executable, err := os.Executable()
	check(t, err)
	child := exec.CommandContext(ctx, executable, "-test.run=^TestTelemetryWorkerProcess$", "-test.v")
	child.Env = append(os.Environ(), "CHITU_TELEMETRY_CHILD=1", "CHITU_TELEMETRY_QUEUE="+name, "CHITU_TELEMETRY_NAMESPACE="+namespace)
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	check(t, child.Start())
	traceID := strings.Repeat("b", 16) + unique()[3:]
	handler := observability.HTTP(observability.HTTPConfig{Tracer: provider})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, err := jobs.Push(r.Context(), "job", nil, queue.Options{})
		check(t, err)
	}))
	request := httptest.NewRequest("POST", "/", nil)
	request.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if err := child.Wait(); err != nil {
		t.Fatalf("worker process: %v\n%s", err, output.String())
	}
	check(t, provider.Flush(ctx))
	assertTraceTree(t, collectorSpans(t, traceID, 4), 4)
}

func TestPrometheusExpositionContract(t *testing.T) {
	metrics, err := observability.NewMetrics()
	check(t, err)
	handler := observability.HTTP(observability.HTTPConfig{Metrics: metrics})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	for range 20 {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("SECRET-METHOD", "/private-secret", nil))
	}
	response := httptest.NewRecorder()
	metrics.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(response.Body)
	check(t, err)
	if len(families) != 4 || families["chitu_http_requests_total"].Metric[0].GetCounter().GetValue() != 20 || families["chitu_http_request_duration_seconds"].Metric[0].GetHistogram().GetSampleCount() != 20 || len(families["chitu_http_requests_total"].Metric) != 1 {
		t.Fatal("invalid Prometheus metrics")
	}
}
