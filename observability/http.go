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

	"github.com/chitushka/framework/internal/httpwriter"
	"github.com/chitushka/framework/web"
)

// HTTPConfig объединяет наблюдаемость одного HTTP request lifecycle.
type HTTPConfig struct {
	Logger  *slog.Logger
	Metrics *Metrics
	Tracer  HTTPTracer
}

// HTTP возвращает net/http middleware для логов, метрик и tracing.
func HTTP(config HTTPConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if next == nil {
			panic("observability: next http.Handler не может быть nil")
		}
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			started := time.Now()
			path := request.URL.Path
			method := request.Method
			if web.IsProduction(request.Context()) {
				path = "[redacted]"
				method = metricMethod(method)
			}
			wrapped := &statusWriter{ResponseWriter: writer, status: http.StatusOK}
			config.Metrics.begin()
			var finish func(int, any)
			if config.Tracer != nil {
				ctx, end := config.Tracer.StartHTTP(request.Context(), request)
				finish = end
				config.Tracer.Inject(ctx, wrapped.Header())
				request = request.WithContext(ctx)
			}
			var recovered any
			defer func() {
				if recovered != nil && !wrapped.wroteHeader {
					wrapped.status = http.StatusInternalServerError
				}
				duration := time.Since(started)
				config.Metrics.observe(request.Method, wrapped.status, duration, recovered != nil)
				if finish != nil {
					finish(wrapped.status, recovered)
				}
				if config.Logger != nil {
					level := slog.LevelInfo
					if wrapped.status >= http.StatusInternalServerError || recovered != nil {
						level = slog.LevelError
					}
					traceID, _, _ := SpanFromContext(request.Context())
					config.Logger.Log(request.Context(), level, "http request",
						"method", method, "path", path, "status", wrapped.status,
						"bytes", wrapped.bytes, "duration", duration, "trace_id", traceID,
					)
				}
				if recovered != nil {
					panic(recovered)
				}
			}()
			func() {
				defer func() { recovered = recover() }()
				next.ServeHTTP(httpwriter.Preserve(wrapped), request)
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
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		writer.ResponseWriter.WriteHeader(status)
		return
	}
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
	_ = writer.FlushError()
}

func (writer *statusWriter) FlushError() error {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(writer.ResponseWriter).Flush()
}

func (writer *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(writer.ResponseWriter).Hijack()
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
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if web.IsProduction(request.Context()) {
			http.NotFound(writer, request)
			return
		}
		mux.ServeHTTP(writer, request)
	})
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
