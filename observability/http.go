// Package observability предоставляет logging, Prometheus metrics, tracing и profiling для HTTP.
package observability

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// HTTPConfig объединяет наблюдаемость одного HTTP request lifecycle.
type HTTPConfig struct {
	Logger  *slog.Logger
	Metrics *Metrics
	Tracer  *Tracer
}

// HTTP возвращает net/http middleware для логов, метрик и tracing.
func HTTP(config HTTPConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if next == nil {
			panic("observability: next http.Handler не может быть nil")
		}
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			started := time.Now()
			wrapped := &statusWriter{ResponseWriter: writer, status: http.StatusOK}
			config.Metrics.begin()
			attributes := map[string]string{"http.method": request.Method, "http.path": request.URL.Path}
			ctx, span, traceErr := config.Tracer.start(request.Context(), request.Method+" "+request.URL.Path, request.Header.Get("traceparent"), attributes)
			if traceErr == nil && span != nil {
				wrapped.Header().Set("traceparent", "00-"+span.span.TraceID+"-"+span.span.SpanID+"-01")
				request = request.WithContext(ctx)
			}
			var recovered any
			defer func() {
				if recovered != nil && !wrapped.wroteHeader {
					wrapped.status = http.StatusInternalServerError
				}
				duration := time.Since(started)
				config.Metrics.observe(request.Method, wrapped.status, duration, recovered != nil)
				span.finish(request.Context(), wrapped.status, recovered)
				if config.Logger != nil {
					level := slog.LevelInfo
					if wrapped.status >= http.StatusInternalServerError || recovered != nil {
						level = slog.LevelError
					}
					traceID, _, _ := SpanFromContext(request.Context())
					config.Logger.Log(request.Context(), level, "http request",
						"method", request.Method, "path", request.URL.Path, "status", wrapped.status,
						"bytes", wrapped.bytes, "duration", duration, "trace_id", traceID,
					)
				}
				if recovered != nil {
					panic(recovered)
				}
			}()
			func() {
				defer func() { recovered = recover() }()
				next.ServeHTTP(wrapped, request)
			}()
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (writer *statusWriter) WriteHeader(status int) {
	if writer.wroteHeader {
		return
	}
	writer.status, writer.wroteHeader = status, true
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusWriter) Write(payload []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	written, err := writer.ResponseWriter.Write(payload)
	writer.bytes += written
	return written, err
}

func (writer *statusWriter) Unwrap() http.ResponseWriter { return writer.ResponseWriter }

func (writer *statusWriter) Flush() {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := writer.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("observability: ResponseWriter не поддерживает hijacking")
	}
	return hijacker.Hijack()
}

func (writer *statusWriter) Push(target string, options *http.PushOptions) error {
	pusher, ok := writer.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, options)
}

func (writer *statusWriter) ReadFrom(reader io.Reader) (int64, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	if readerFrom, ok := writer.ResponseWriter.(io.ReaderFrom); ok {
		read, err := readerFrom.ReadFrom(reader)
		writer.bytes += int(read)
		return read, err
	}
	return io.Copy(writerOnly{writer}, reader)
}

type writerOnly struct{ io.Writer }

// PprofHandler возвращает изолированный mux стандартного net/http/pprof.
// Приложение должно самостоятельно ограничить доступ к нему.
func PprofHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// RemoteAddress возвращает host без порта для безопасного log attribute.
func RemoteAddress(request *http.Request) string {
	if request == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return fmt.Sprint(request.RemoteAddr)
	}
	return host
}
