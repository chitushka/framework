// Package otel подключает ограниченный OpenTelemetry export к HTTP, SQL и queue.
package otel

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/observability"
	"github.com/chitushka/framework/queue"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Config задаётся приложением; SampleRatio=0 отключает новые корневые traces.
// Решение sampling родителя сохраняется (включая remote sampled=0).
type Config struct {
	ServiceName                                string
	SampleRatio                                float64
	QueueSize, BatchSize                       int
	BatchInterval, ExportTimeout, FlushTimeout time.Duration
}

// HTTPConfig требует явный полный OTLP URL, включая /v1/traces.
// Plain HTTP разрешён только с Insecure для изолированного тестового collector.
type HTTPConfig struct {
	Endpoint  string
	Insecure  bool
	TLSConfig *tls.Config
	Headers   map[string]string
}

// Stats не содержит exporter errors: они могут включать credentials/PII.
// Unexported включает ещё ожидающие и отброшенные SDK spans.
type Stats struct{ Ended, Exported, Failed, ExportErrors, Unexported uint64 }

type counters struct{ ended, exported, failed, errors atomic.Uint64 }

// Provider реализует observability.HTTPTracer и queue.Observer.
// SDK provider/глобальные propagators не публикуются и не изменяются.
type Provider struct {
	processor    sdktrace.SpanProcessor
	cancelExport context.CancelFunc
	sdk          *sdktrace.TracerProvider
	tracer       trace.Tracer
	config       Config
	stats        *counters
	control      chan struct{}
}

var (
	_ observability.HTTPTracer = (*Provider)(nil)
	_ queue.Observer           = (*Provider)(nil)
)

func normalize(config Config) (Config, error) {
	if config.ServiceName == "" || len(config.ServiceName) > 128 || math.IsNaN(config.SampleRatio) || config.SampleRatio < 0 || config.SampleRatio > 1 {
		return config, errors.New("otel: service name and finite sample ratio [0,1] required")
	}
	if config.QueueSize == 0 {
		config.QueueSize = 1024
	}
	if config.BatchSize == 0 {
		config.BatchSize = min(128, config.QueueSize)
	}
	if config.BatchInterval == 0 {
		config.BatchInterval = 200 * time.Millisecond
	}
	if config.ExportTimeout == 0 {
		config.ExportTimeout = time.Second
	}
	if config.FlushTimeout == 0 {
		config.FlushTimeout = 5 * time.Second
	}
	if config.QueueSize < 1 || config.QueueSize > 8192 || config.BatchSize < 1 || config.BatchSize > min(config.QueueSize, 512) || config.BatchInterval < time.Millisecond || config.BatchInterval > time.Minute || config.ExportTimeout < time.Millisecond || config.ExportTimeout > 30*time.Second || config.FlushTimeout < time.Millisecond || config.FlushTimeout > 30*time.Second {
		return config, errors.New("otel: invalid queue, batch or time budgets")
	}
	return config, nil
}

// New принимает SDK exporter, соблюдающий cancellation, и передаёт ему ownership.
// ExportSpans выполняется одним batch worker; заполнение очереди теряет telemetry.
func New(config Config, exporter sdktrace.SpanExporter) (*Provider, error) {
	config, err := normalize(config)
	if err != nil {
		return nil, err
	}
	if exporter == nil {
		return nil, errors.New("otel: exporter required")
	}
	stats := &counters{}
	exportContext, cancelExport := context.WithCancel(context.Background())
	batch := sdktrace.NewBatchSpanProcessor(&safeExporter{next: exporter, stats: stats, lifetime: exportContext},
		sdktrace.WithMaxQueueSize(config.QueueSize), sdktrace.WithMaxExportBatchSize(config.BatchSize),
		sdktrace.WithBatchTimeout(config.BatchInterval), sdktrace.WithExportTimeout(config.ExportTimeout))
	limits := sdktrace.NewSpanLimits()
	limits.AttributeCountLimit, limits.AttributeValueLengthLimit = 16, 128
	limits.EventCountLimit, limits.LinkCountLimit = 1, 1
	limits.AttributePerEventCountLimit, limits.AttributePerLinkCountLimit = 1, 1
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", config.ServiceName))),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(config.SampleRatio))),
		sdktrace.WithRawSpanLimits(limits), sdktrace.WithSpanProcessor(&countingProcessor{SpanProcessor: batch, stats: stats}))
	return &Provider{sdk: provider, tracer: provider.Tracer("github.com/chitushka/framework"), config: config, stats: stats, control: make(chan struct{}, 1), cancelExport: cancelExport, processor: batch}, nil
}

// NewHTTP использует поддерживаемый OTLP/HTTP exporter с конечным deadline без retry.
// TLS/auth задаются явно; переменные окружения не могут сменить endpoint/headers.
func NewHTTP(ctx context.Context, config Config, transport HTTPConfig) (*Provider, error) {
	if ctx == nil {
		return nil, errors.New("otel: context required")
	}
	var err error
	config, err = normalize(config)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(transport.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/v1/traces" || u.Scheme != "https" && (!transport.Insecure || u.Scheme != "http") {
		return nil, errors.New("otel: explicit OTLP HTTP(S) /v1/traces endpoint required")
	}
	if len(transport.Headers) > 16 {
		return nil, errors.New("otel: too many authentication headers")
	}
	headers := make(map[string]string, len(transport.Headers))
	for key, value := range transport.Headers {
		if len(key) > 128 || len(value) > 4096 {
			return nil, errors.New("otel: authentication header exceeds budget")
		}
		headers[key] = value
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if transport.TLSConfig != nil {
		tlsConfig = transport.TLSConfig.Clone()
		if tlsConfig.InsecureSkipVerify {
			return nil, errors.New("otel: TLS verification must remain enabled")
		}
		tlsConfig.MinVersion = max(tlsConfig.MinVersion, tls.VersionTLS12)
	}
	clientTransport := &http.Transport{TLSClientConfig: tlsConfig, MaxConnsPerHost: 1, MaxIdleConns: 1, IdleConnTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10, TLSHandshakeTimeout: config.ExportTimeout, ResponseHeaderTimeout: config.ExportTimeout}
	client := &http.Client{Transport: boundedTransport{clientTransport}, Timeout: config.ExportTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	options := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(transport.Endpoint), otlptracehttp.WithHeaders(headers),
		otlptracehttp.WithTimeout(config.ExportTimeout), otlptracehttp.WithHTTPClient(client),
		otlptracehttp.WithTLSClientConfig(nil),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}),
	}
	if u.Scheme == "http" {
		options = append(options, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return nil, errors.New("otel: OTLP exporter initialization failed")
	}
	return New(config, &httpExporter{SpanExporter: exporter, transport: clientTransport})
}

type httpExporter struct {
	sdktrace.SpanExporter
	transport *http.Transport
}

func (exporter *httpExporter) Shutdown(ctx context.Context) error {
	defer exporter.transport.CloseIdleConnections()
	return exporter.SpanExporter.Shutdown(ctx)
}

type boundedTransport struct{ next http.RoundTripper }

func (transport boundedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.next.RoundTrip(request)
	if err == nil {
		response.Body = &limitedBody{Reader: io.LimitReader(response.Body, 64<<10), Closer: response.Body}
	}
	return response, err
}

type limitedBody struct {
	io.Reader
	io.Closer
}

type countingProcessor struct {
	sdktrace.SpanProcessor
	stats *counters
}

func (processor *countingProcessor) OnEnd(span sdktrace.ReadOnlySpan) {
	if span.SpanContext().IsSampled() {
		processor.stats.ended.Add(1)
	}
	processor.SpanProcessor.OnEnd(span)
}

type safeExporter struct {
	lifetime context.Context
	next     sdktrace.SpanExporter
	stats    *counters
}

func (exporter *safeExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) (err error) {
	defer func() {
		if recover() != nil {
			err = observability.ErrExport
		}
		if err != nil {
			exporter.stats.errors.Add(1)
			exporter.stats.failed.Add(uint64(len(spans)))
			// SDK вызывает глобальный ErrorHandler; не передаём ему ошибку с secrets.
			err = observability.ErrExport
		} else {
			exporter.stats.exported.Add(uint64(len(spans)))
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(exporter.lifetime, cancel)
	defer stop()
	defer cancel()
	if exporter.lifetime.Err() != nil {
		return exporter.lifetime.Err()
	}
	return exporter.next.ExportSpans(ctx, spans)
}

func (exporter *safeExporter) Shutdown(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = observability.ErrExport
		}
	}()
	if exporter.next.Shutdown(ctx) != nil {
		return observability.ErrExport
	}
	return nil
}

// Stats возвращает накопленные числа; при quiescent Flush Unexported — потери очереди.
func (provider *Provider) Stats() Stats {
	s := Stats{Ended: provider.stats.ended.Load(), Exported: provider.stats.exported.Load(), Failed: provider.stats.failed.Load(), ExportErrors: provider.stats.errors.Load()}
	if s.Ended > s.Exported+s.Failed {
		s.Unexported = s.Ended - s.Exported - s.Failed
	}
	return s
}

func (provider *Provider) controlExport(ctx context.Context, shutdown bool) error {
	if ctx == nil {
		return errors.New("otel: flush context required")
	}
	ctx, cancel := context.WithTimeout(ctx, provider.config.FlushTimeout)
	defer cancel()
	select {
	case provider.control <- struct{}{}:
	default:
		select {
		case provider.control <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() { <-provider.control }()
	target := provider.stats.ended.Load()
	var err error
	if shutdown {
		err = provider.sdk.Shutdown(ctx)
		provider.cancelExport()
		// SDK provider может пропустить processors при уже отменённом context.
		err = errors.Join(err, provider.processor.Shutdown(ctx))
	} else {
		err = provider.sdk.ForceFlush(ctx)
	}
	if err != nil {
		return err
	}
	stats := provider.Stats()
	if stats.ExportErrors > 0 || stats.Exported+stats.Failed < target {
		return observability.ErrExport
	}
	return ctx.Err()
}

// Flush ограничивает ожидание caller deadline и FlushTimeout; ошибки не скрываются.
func (provider *Provider) Flush(ctx context.Context) error { return provider.controlExport(ctx, false) }

// Shutdown вызывается после HTTP/worker drain с независимым cleanup context.
func (provider *Provider) Shutdown(ctx context.Context) error {
	return provider.controlExport(ctx, true)
}

func method(value string) string {
	switch value {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		return value
	default:
		return "OTHER"
	}
}

func cleanParent(ctx context.Context) context.Context {
	sc := trace.SpanContextFromContext(ctx).WithTraceState(trace.TraceState{})
	return trace.ContextWithSpanContext(ctx, sc)
}

func bridge(ctx context.Context) context.Context {
	sc := trace.SpanContextFromContext(ctx)
	return observability.WithSpanContext(ctx, sc.TraceID().String(), sc.SpanID().String())
}

// StartHTTP не записывает URL, headers, user IDs, panic values или exception messages.
func (provider *Provider) StartHTTP(ctx context.Context, request *http.Request) (context.Context, func(int, any)) {
	carrier := propagation.MapCarrier{}
	if value := request.Header.Get("traceparent"); len(value) <= 512 {
		carrier.Set("traceparent", value)
	}
	ctx = propagation.TraceContext{}.Extract(cleanParent(ctx), carrier)
	ctx, span := provider.tracer.Start(ctx, "HTTP "+method(request.Method), trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attribute.String("http.request.method", method(request.Method))))
	ctx = provider.WithSQL(bridge(ctx))
	var once sync.Once
	return ctx, func(status int, recovered any) {
		once.Do(func() {
			if status < 100 || status > 599 {
				status = 500
			}
			span.SetAttributes(attribute.Int("http.response.status_code", status))
			if status >= 500 || recovered != nil {
				span.SetStatus(codes.Error, "request failed")
			}
			span.End()
		})
	}
}

// Inject передаёт только W3C traceparent. Baggage/tracestate намеренно не копируются.
func (provider *Provider) Inject(ctx context.Context, headers http.Header) {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	if parent := carrier.Get("traceparent"); parent != "" {
		headers.Set("traceparent", parent)
	}
}

// WithSQL добавляет безопасный observer, сохраняя имеющиеся diagnostic observers.
func (provider *Provider) WithSQL(ctx context.Context) context.Context {
	if ctx.Value(provider) != nil {
		return ctx
	}
	return db.AddQueryObserver(context.WithValue(ctx, provider, true), provider)
}

// ObserveQuery измеряет database/sql call, не обработку rows/Scan и prepared Stmt calls.
func (provider *Provider) ObserveQuery(ctx context.Context, event db.QueryEvent) {
	operation := event.Operation
	switch operation {
	case "exec", "query", "query_row", "prepare", "tx_exec", "tx_query", "tx_prepare":
	default:
		operation = "other"
	}
	ended := time.Now()
	_, span := provider.tracer.Start(cleanParent(ctx), "SQL "+operation, trace.WithSpanKind(trace.SpanKindClient), trace.WithTimestamp(ended.Add(-max(event.Duration, 0))), trace.WithAttributes(attribute.String("db.operation.name", operation)))
	if event.Err != nil {
		span.SetStatus(codes.Error, "database operation failed")
	}
	span.End(trace.WithTimestamp(ended))
}

// StartEnqueue записывает producer context в совместимые необязательные поля envelope.
func (provider *Provider) StartEnqueue(ctx context.Context, message *queue.Message) (context.Context, func(error)) {
	ctx, span := provider.tracer.Start(cleanParent(ctx), "queue publish", trace.WithSpanKind(trace.SpanKindProducer), trace.WithAttributes(attribute.String("messaging.operation.type", "send")))
	headers := http.Header{}
	provider.Inject(ctx, headers)
	message.TraceParent, message.TraceState = headers.Get("traceparent"), ""
	return provider.WithSQL(bridge(ctx)), endJob(span)
}

// StartProcess игнорирует span контекста запуска worker: parent берётся из сообщения.
func (provider *Provider) StartProcess(ctx context.Context, message queue.Message) (context.Context, func(error)) {
	ctx = trace.ContextWithSpanContext(ctx, trace.SpanContext{})
	carrier := propagation.MapCarrier{}
	if len(message.TraceParent) <= 512 {
		carrier.Set("traceparent", message.TraceParent)
	}
	ctx = propagation.TraceContext{}.Extract(ctx, carrier)
	ctx, span := provider.tracer.Start(ctx, "queue process", trace.WithSpanKind(trace.SpanKindConsumer), trace.WithAttributes(attribute.String("messaging.operation.type", "process"), attribute.Int("messaging.delivery.attempt", min(max(message.Attempt, 0), 1000))))
	return provider.WithSQL(bridge(ctx)), endJob(span)
}

func endJob(span trace.Span) func(error) {
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			if err != nil {
				span.SetStatus(codes.Error, "job operation failed")
			}
			span.End()
		})
	}
}
