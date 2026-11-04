package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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

// Tracer создаёт W3C-compatible spans и fan-out экспортирует их.
type Tracer struct{ exporters []SpanExporter }

// NewTracer создаёт tracer. Пустой список допустим для propagation-only режима.
func NewTracer(exporters ...SpanExporter) (*Tracer, error) {
	for _, exporter := range exporters {
		if exporter == nil {
			return nil, errors.New("observability: SpanExporter не может быть nil")
		}
	}
	return &Tracer{exporters: append([]SpanExporter(nil), exporters...)}, nil
}

type activeSpan struct {
	span      Span
	once      sync.Once
	exporters []SpanExporter
}

type spanContextKey struct{}

// SpanFromContext возвращает идентификаторы активного span.
func SpanFromContext(ctx context.Context) (traceID, spanID string, ok bool) {
	if ctx == nil {
		return "", "", false
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
	span := &activeSpan{span: Span{TraceID: traceID, SpanID: spanID, ParentSpanID: parentID, Name: name, StartedAt: time.Now(), Attributes: attributes}}
	if tracer != nil {
		span.exporters = append([]SpanExporter(nil), tracer.exporters...)
	}
	return context.WithValue(ctx, spanContextKey{}, span), span, nil
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
		for _, exporter := range span.exporters {
			_ = exporter.ExportSpan(ctx, cloneSpan(span.span))
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
	if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 || allZero(parts[1]) || allZero(parts[2]) {
		return "", "", false
	}
	if _, err := hex.DecodeString(parts[1] + parts[2] + parts[3]); err != nil {
		return "", "", false
	}
	return strings.ToLower(parts[1]), strings.ToLower(parts[2]), true
}

func allZero(value string) bool { return strings.Trim(value, "0") == "" }
