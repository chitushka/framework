package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chitushka/framework/web"
)

// Span описывает завершённый trace span для exporter-адаптера.
type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	StartedAt    time.Time
	EndedAt      time.Time
	Attributes   map[string]string
	Error        string
}

// SpanExporter передаёт span в OpenTelemetry-adapter или другое хранилище.
type SpanExporter interface {
	ExportSpan(context.Context, Span) error
}

// HTTPTracer позволяет подключить SDK без обязательной зависимости ядра от него.
type HTTPTracer interface {
	StartHTTP(context.Context, *http.Request) (context.Context, func(int, any))
	Inject(context.Context, http.Header)
}

// Tracer создаёт W3C spans и экспортирует их через ограниченную очередь.
// Для HTTP/SQL/queue с OpenTelemetry используйте adapters/otel.
type Tracer struct {
	exportContext                          context.Context
	cancelExport                           context.CancelFunc
	exporters                              []SpanExporter
	mu                                     sync.Mutex
	pending                                []Span
	accepted, completed, dropped, failures uint64
	closed                                 bool
	wake                                   chan struct{}
	changed                                chan struct{}
	done                                   chan struct{}
}

// ExportStats содержит ограниченные счётчики без текстов ошибок и секретов.
type ExportStats struct {
	Accepted, Completed, Dropped, Failures uint64
	Pending                                int
}

// ErrExport означает, что часть телеметрии не была доставлена.
var ErrExport = errors.New("observability: trace export failed; inspect export statistics")

// NewTracer создаёт tracer. Пустой список допустим для propagation-only режима.
func NewTracer(exporters ...SpanExporter) (*Tracer, error) {
	if len(exporters) > 16 {
		return nil, errors.New("observability: at most 16 exporters")
	}
	for _, exporter := range exporters {
		if exporter == nil {
			return nil, errors.New("observability: SpanExporter не может быть nil")
		}
	}
	tracer := &Tracer{exporters: append([]SpanExporter(nil), exporters...), wake: make(chan struct{}, 1), changed: make(chan struct{}), done: make(chan struct{})}
	tracer.exportContext, tracer.cancelExport = context.WithCancel(context.Background())
	if len(exporters) > 0 {
		go tracer.run()
	} else {
		close(tracer.done)
	}
	return tracer, nil
}

type activeSpan struct {
	span    Span
	once    sync.Once
	tracer  *Tracer
	sampled bool
}

type spanContextKey struct{}

type traceIDs struct{ traceID, spanID string }
type traceIDsKey struct{}

// WithSpanContext связывает SDK identifiers с logging/debug без импорта SDK.
func WithSpanContext(ctx context.Context, traceID, spanID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, _, ok := parseTraceParent("00-" + traceID + "-" + spanID + "-00"); !ok {
		traceID, spanID = "", ""
	}
	return context.WithValue(ctx, traceIDsKey{}, traceIDs{traceID, spanID})
}

// SpanFromContext возвращает идентификаторы активного span.
func SpanFromContext(ctx context.Context) (traceID, spanID string, ok bool) {
	if ctx == nil {
		return "", "", false
	}
	if ids, ok := ctx.Value(traceIDsKey{}).(traceIDs); ok {
		return ids.traceID, ids.spanID, ids.traceID != "" && ids.spanID != ""
	}
	span, ok := ctx.Value(spanContextKey{}).(*activeSpan)
	if !ok || span == nil {
		return "", "", false
	}
	return span.span.TraceID, span.span.SpanID, true
}

func (tracer *Tracer) start(ctx context.Context, name, traceParent string, attributes map[string]string) (context.Context, *activeSpan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	traceID, parentID, valid := parseTraceParent(traceParent)
	if !valid {
		var err error
		traceID, err = randomHex(16)
		if err != nil {
			return ctx, nil, err
		}
	}
	spanID, err := randomHex(8)
	if err != nil {
		return ctx, nil, err
	}
	sampled := true
	if valid {
		flags, _ := strconv.ParseUint(traceParent[len(traceParent)-2:], 16, 8)
		sampled = flags&1 == 1
	}
	span := &activeSpan{span: Span{TraceID: traceID, SpanID: spanID, ParentSpanID: parentID, Name: name, StartedAt: time.Now(), Attributes: attributes}, tracer: tracer, sampled: sampled}
	return context.WithValue(ctx, spanContextKey{}, span), span, nil
}

// StartHTTP начинает HTTP span; raw paths доступны только вне production profile.
func (tracer *Tracer) StartHTTP(ctx context.Context, request *http.Request) (context.Context, func(int, any)) {
	method, path := request.Method, request.URL.Path
	if web.IsProduction(ctx) {
		method, path = metricMethod(method), "[redacted]"
	}
	ctx, span, err := tracer.start(ctx, method+" "+path, request.Header.Get("traceparent"), map[string]string{"http.method": method, "http.path": path})
	if err != nil {
		return ctx, nil
	}
	return ctx, func(status int, recovered any) { span.finish(ctx, status, recovered) }
}

// Inject передаёт sampling bit активного legacy span.
func (tracer *Tracer) Inject(ctx context.Context, headers http.Header) {
	if span, ok := ctx.Value(spanContextKey{}).(*activeSpan); ok && span != nil {
		flags := "00"
		if span.sampled {
			flags = "01"
		}
		headers.Set("traceparent", "00-"+span.span.TraceID+"-"+span.span.SpanID+"-"+flags)
	}
}

func (span *activeSpan) finish(ctx context.Context, status int, recovered any) {
	if span == nil {
		return
	}
	span.once.Do(func() {
		span.span.EndedAt = time.Now()
		span.span.Attributes["http.status_code"] = fmt.Sprint(status)
		if recovered != nil {
			if web.IsProduction(ctx) {
				span.span.Error = "request panic"
			} else {
				span.span.Error = fmt.Sprint(recovered)
			}
		} else if status >= 500 {
			span.span.Error = fmt.Sprintf("HTTP %d", status)
		}
		if span.sampled && span.tracer != nil {
			span.tracer.enqueue(span.span)
		}
	})
}

func cloneSpan(span Span) Span {
	span.Attributes = mapClone(span.Attributes)
	return span
}

func mapClone(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func randomHex(bytesCount int) (string, error) {
	buffer := make([]byte, bytesCount)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("observability: trace id: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func parseTraceParent(value string) (string, string, bool) {
	parts := strings.Split(value, "-")
	if len(parts) != 4 || value != strings.ToLower(value) || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 || allZero(parts[1]) || allZero(parts[2]) {
		return "", "", false
	}
	if _, err := hex.DecodeString(parts[1] + parts[2] + parts[3]); err != nil {
		return "", "", false
	}
	return strings.ToLower(parts[1]), strings.ToLower(parts[2]), true
}

func allZero(value string) bool { return strings.Trim(value, "0") == "" }

func (tracer *Tracer) enqueue(span Span) {
	if len(tracer.exporters) == 0 {
		return
	}
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	if tracer.closed || len(tracer.pending) >= 256 {
		tracer.dropped++
		return
	}
	span = cloneSpan(span)
	span.Name = truncate(span.Name, 256)
	span.Error = truncate(span.Error, 256)
	for key, value := range span.Attributes {
		span.Attributes[key] = truncate(value, 256)
	}
	tracer.pending = append(tracer.pending, span)
	tracer.accepted++
	select {
	case tracer.wake <- struct{}{}:
	default:
	}
}

func truncate(value string, limit int) string {
	if len(value) > limit {
		return strings.Clone(value[:limit])
	}
	return strings.Clone(value)
}

func (tracer *Tracer) run() {
	defer close(tracer.done)
	for {
		tracer.mu.Lock()
		if tracer.exportContext.Err() != nil {
			tracer.dropped += uint64(len(tracer.pending))
			tracer.completed += uint64(len(tracer.pending))
			clear(tracer.pending)
			tracer.pending = nil
			close(tracer.changed)
			tracer.mu.Unlock()
			return
		}
		if len(tracer.pending) == 0 {
			closed := tracer.closed
			tracer.mu.Unlock()
			if closed {
				return
			}
			<-tracer.wake
			continue
		}
		span := tracer.pending[0]
		tracer.pending[0] = Span{}
		tracer.pending = tracer.pending[1:]
		tracer.mu.Unlock()
		failures := uint64(0)
		for _, exporter := range tracer.exporters {
			if err := exportSpan(tracer.exportContext, exporter, span); err != nil {
				failures++
			}
		}
		tracer.mu.Lock()
		tracer.completed++
		tracer.failures += failures
		close(tracer.changed)
		tracer.changed = make(chan struct{})
		tracer.mu.Unlock()
	}
}

func exportSpan(parent context.Context, exporter SpanExporter, span Span) (err error) {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	defer func() {
		if recover() != nil {
			err = ErrExport
		}
	}()
	return exporter.ExportSpan(ctx, cloneSpan(span))
}

// Stats возвращает накопленные счётчики, включая потери при переполнении.
func (tracer *Tracer) Stats() ExportStats {
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	return ExportStats{tracer.accepted, tracer.completed, tracer.dropped, tracer.failures, len(tracer.pending)}
}

// Flush ждёт уже принятые spans не более пяти секунд. Exporter обязан соблюдать context.
// ErrExport сообщает о любых накопленных ошибках/потерях за жизнь tracer.
func (tracer *Tracer) Flush(ctx context.Context) error {
	if ctx == nil {
		return errors.New("observability: nil flush context")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tracer.mu.Lock()
	target := tracer.accepted
	for tracer.completed < target {
		changed := tracer.changed
		tracer.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		tracer.mu.Lock()
	}
	failed := tracer.failures > 0 || tracer.dropped > 0
	tracer.mu.Unlock()
	if failed {
		return ErrExport
	}
	return ctx.Err()
}

// Shutdown запрещает новые spans и ждёт drain в пределах caller budget, максимум 5s.
func (tracer *Tracer) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("observability: nil shutdown context")
	}
	tracer.mu.Lock()
	tracer.closed = true
	tracer.mu.Unlock()
	select {
	case tracer.wake <- struct{}{}:
	default:
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		tracer.cancelExport()
		return ctx.Err()
	case <-tracer.done:
		tracer.cancelExport()
		return tracer.Flush(ctx)
	}
}
