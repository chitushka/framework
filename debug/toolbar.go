// Package debug предоставляет development-only панель диагностики HTTP-запроса.
package debug

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/internal/httpwriter"
	"github.com/chitushka/framework/observability"
	"github.com/chitushka/framework/web"
)

// SessionSnapshot возвращает безопасные для диагностики значения сессии.
type SessionSnapshot func(context.Context) map[string]any

// Config настраивает toolbar. По умолчанию он выключен.
type Config struct {
	Enabled        bool
	Environment    []string
	Session        SessionSnapshot
	CaptureSQLArgs bool
	MaxSQLQueries  int
	// MaxResponseBytes bounds the HTML buffer; larger responses pass through unchanged.
	MaxResponseBytes int
}

// Toolbar добавляет панель только в успешные несжатые HTML-ответы.
type Toolbar struct{ config Config }

// New создаёт toolbar. Environment является явным allowlist.
func New(config Config) (*Toolbar, error) {
	if config.MaxSQLQueries < 0 {
		return nil, fmt.Errorf("debug: MaxSQLQueries не может быть отрицательным")
	}
	if config.MaxResponseBytes < 0 {
		return nil, fmt.Errorf("debug: MaxResponseBytes cannot be negative")
	}
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = 1 << 20
	}
	if config.MaxSQLQueries == 0 {
		config.MaxSQLQueries = 100
	}
	config.Environment = slices.Clone(config.Environment)
	return &Toolbar{config: config}, nil
}

// Middleware собирает данные запроса и встраивает панель перед </body>.
func (toolbar *Toolbar) Middleware(next web.Handler) web.Handler {
	if next == nil {
		panic("debug: next handler не может быть nil")
	}
	if toolbar == nil || !toolbar.config.Enabled {
		return next
	}
	return func(ctx *web.Context) error {
		started := time.Now()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		collector := &collector{captureArgs: toolbar.config.CaptureSQLArgs, limit: toolbar.config.MaxSQLQueries}
		requestContext := context.WithValue(ctx.Request.Context(), collectorContextKey{}, collector)
		requestContext = db.WithQueryObserver(requestContext, collector)
		ctx.Request = ctx.Request.WithContext(requestContext)

		original := ctx.Writer
		buffered := newBufferedWriter(original, toolbar.config.MaxResponseBytes)
		ctx.Writer = httpwriter.Preserve(buffered)
		defer func() { ctx.Writer = original }()
		err := next(ctx)
		ctx.Writer = original
		if buffered.passthrough {
			return err
		}
		if err != nil || !shouldInject(ctx.Request, buffered) {
			if buffered.wroteHeader {
				if commitErr := buffered.commit(original, nil); commitErr != nil {
					return errors.Join(err, commitErr)
				}
			}
			return err
		}

		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		traceID, _, _ := observability.SpanFromContext(ctx.Request.Context())
		view := toolbarView{
			Duration: time.Since(started), Heap: after.Alloc, Allocated: after.TotalAlloc - before.TotalAlloc,
			Method: ctx.Request.Method, Path: ctx.Request.URL.Path, Route: ctx.RoutePattern(), RouteName: ctx.RouteName(),
			Params: stringPairs(ctx.Params()), Status: buffered.status, TraceID: traceID,
			SQL: collector.snapshot(), Environment: environmentPairs(toolbar.config.Environment),
		}
		if toolbar.config.Session != nil {
			view.Session = anyPairs(toolbar.config.Session(ctx.Request.Context()))
		}
		fragment, renderErr := renderToolbar(view)
		if renderErr != nil {
			if commitErr := buffered.commit(original, nil); commitErr != nil {
				return fmt.Errorf("%w; %v", renderErr, commitErr)
			}
			return renderErr
		}
		return buffered.commit(original, fragment)
	}
}

// RecordSQL добавляет событие от нестандартного DB-клиента в текущую панель.
func RecordSQL(ctx context.Context, event db.QueryEvent) {
	if ctx == nil {
		return
	}
	collector, _ := ctx.Value(collectorContextKey{}).(*collector)
	if collector != nil {
		collector.ObserveQuery(ctx, event)
	}
}

// RecordSQLPlan прикрепляет полученный приложением EXPLAIN plan к последнему совпавшему query.
func RecordSQLPlan(ctx context.Context, query, plan string) {
	if ctx == nil {
		return
	}
	collector, _ := ctx.Value(collectorContextKey{}).(*collector)
	if collector != nil {
		collector.recordPlan(query, plan)
	}
}

type collectorContextKey struct{}
type collector struct {
	mu          sync.Mutex
	queries     []SQLQuery
	captureArgs bool
	limit       int
}

// SQLQuery является безопасным snapshot диагностической SQL-операции.
type SQLQuery struct {
	Operation string
	Query     string
	Args      []string
	Duration  time.Duration
	Error     string
	Plan      string
}

func (collector *collector) ObserveQuery(_ context.Context, event db.QueryEvent) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if len(collector.queries) >= collector.limit {
		return
	}
	item := SQLQuery{Operation: event.Operation, Query: event.Query, Duration: event.Duration}
	if event.Err != nil {
		item.Error = event.Err.Error()
	}
	if collector.captureArgs {
		item.Args = make([]string, len(event.Args))
		for index, argument := range event.Args {
			item.Args[index] = fmt.Sprint(argument)
		}
	}
	collector.queries = append(collector.queries, item)
}

func (collector *collector) recordPlan(query, plan string) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	for index := len(collector.queries) - 1; index >= 0; index-- {
		if collector.queries[index].Query == query {
			collector.queries[index].Plan = plan
			return
		}
	}
}

func (collector *collector) snapshot() []SQLQuery {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	result := make([]SQLQuery, len(collector.queries))
	for index, query := range collector.queries {
		result[index] = query
		result[index].Args = slices.Clone(query.Args)
	}
	return result
}

type bufferedWriter struct {
	original    http.ResponseWriter
	limit       int
	passthrough bool
	header      http.Header
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func newBufferedWriter(original http.ResponseWriter, limit int) *bufferedWriter {
	return &bufferedWriter{original: original, limit: limit, header: original.Header().Clone(), status: http.StatusOK}
}
func (writer *bufferedWriter) Header() http.Header {
	if writer.passthrough {
		return writer.original.Header()
	}
	return writer.header
}
func (writer *bufferedWriter) WriteHeader(status int) {
	if writer.passthrough {
		writer.original.WriteHeader(status)
		return
	}
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		writer.copyHeaders(writer.original)
		writer.original.WriteHeader(status)
		return
	}
	if !writer.wroteHeader {
		writer.status, writer.wroteHeader = status, true
	}
}
func (writer *bufferedWriter) Write(payload []byte) (int, error) {
	if writer.passthrough {
		return writer.original.Write(payload)
	}
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	if len(payload) > writer.limit-writer.body.Len() {
		if err := writer.pass(); err != nil {
			return 0, err
		}
		return writer.original.Write(payload)
	}
	return writer.body.Write(payload)
}

func (writer *bufferedWriter) Unwrap() http.ResponseWriter { return writer.original }
func (writer *bufferedWriter) pass() error {
	if writer.passthrough {
		return nil
	}
	writer.passthrough = true
	err := writer.commit(writer.original, nil)
	writer.body.Reset()
	return err
}
func (writer *bufferedWriter) FlushError() error {
	if err := writer.pass(); err != nil {
		return err
	}
	return http.NewResponseController(writer.original).Flush()
}
func (writer *bufferedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if writer.wroteHeader {
		if err := writer.pass(); err != nil {
			return nil, nil, err
		}
	} else {
		writer.copyHeaders(writer.original)
		writer.passthrough = true
	}
	return http.NewResponseController(writer.original).Hijack()
}
func (writer *bufferedWriter) copyHeaders(target http.ResponseWriter) {
	for name := range target.Header() {
		target.Header().Del(name)
	}
	for name, values := range writer.header {
		target.Header()[name] = slices.Clone(values)
	}
}

func (writer *bufferedWriter) commit(target http.ResponseWriter, fragment []byte) error {
	payload := writer.body.Bytes()
	if len(fragment) > 0 {
		lower := strings.ToLower(string(payload))
		position := strings.LastIndex(lower, "</body>")
		if position < 0 {
			payload = append(append(slices.Clone(payload), fragment...), '\n')
		} else {
			combined := make([]byte, 0, len(payload)+len(fragment))
			combined = append(combined, payload[:position]...)
			combined = append(combined, fragment...)
			combined = append(combined, payload[position:]...)
			payload = combined
		}
		writer.header.Del("Content-Length")
	}
	writer.copyHeaders(target)
	target.WriteHeader(writer.status)
	if len(payload) == 0 {
		return nil
	}
	written, err := target.Write(payload)
	if err != nil {
		return fmt.Errorf("debug: запись buffered response: %w", err)
	}
	if written != len(payload) {
		return fmt.Errorf("debug: запись buffered response: %w", fmt.Errorf("short write"))
	}
	return nil
}

func shouldInject(request *http.Request, writer *bufferedWriter) bool {
	return request.Method != http.MethodHead && writer.status >= 200 && writer.status < 300 &&
		writer.status != http.StatusPartialContent && writer.header.Get("Content-Range") == "" && writer.header.Get("ETag") == "" &&
		writer.header.Get("Accept-Ranges") == "" && writer.header.Get("Content-Disposition") == "" &&
		strings.HasPrefix(strings.ToLower(writer.header.Get("Content-Type")), "text/html") && writer.header.Get("Content-Encoding") == ""
}

type pair struct{ Name, Value string }
type toolbarView struct {
	Duration                                time.Duration
	Heap, Allocated                         uint64
	Method, Path, Route, RouteName, TraceID string
	Status                                  int
	Params, Session, Environment            []pair
	SQL                                     []SQLQuery
}

func stringPairs(values map[string]string) []pair {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return anyPairs(result)
}
func anyPairs(values map[string]any) []pair {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]pair, 0, len(keys))
	for _, key := range keys {
		result = append(result, pair{Name: key, Value: fmt.Sprint(values[key])})
	}
	return result
}
func environmentPairs(allowed []string) []pair {
	keys := slices.Clone(allowed)
	slices.Sort(keys)
	keys = slices.Compact(keys)
	result := make([]pair, 0, len(keys))
	for _, key := range keys {
		result = append(result, pair{Name: key, Value: os.Getenv(key)})
	}
	return result
}

var toolbarTemplate = template.Must(template.New("toolbar").Funcs(template.FuncMap{
	"ms": func(duration time.Duration) string {
		return strconv.FormatFloat(float64(duration)/float64(time.Millisecond), 'f', 2, 64)
	},
	"kb": func(bytes uint64) string { return strconv.FormatFloat(float64(bytes)/1024, 'f', 1, 64) },
}).Parse(`<aside id="chitu-debug-toolbar" style="position:fixed;z-index:2147483647;left:0;right:0;bottom:0;background:#17202a;color:#f8f9fa;font:12px/1.4 monospace;border-top:2px solid #3b7ddd;padding:7px 12px">
<strong>Chitu Debug</strong> · {{.Method}} {{.Path}} · {{.Status}} · {{ms .Duration}} ms · heap {{kb .Heap}} KB · alloc {{kb .Allocated}} KB
<details style="display:inline-block;margin-left:12px"><summary style="cursor:pointer">route</summary><div>{{.Route}} {{.RouteName}}{{range .Params}}<br>{{.Name}}={{.Value}}{{end}}</div></details>
<details style="display:inline-block;margin-left:12px"><summary style="cursor:pointer">SQL ({{len .SQL}})</summary><div style="max-height:240px;overflow:auto">{{range .SQL}}<p><b>{{.Operation}}</b> {{ms .Duration}} ms<br>{{.Query}}{{if .Args}}<br>args: {{.Args}}{{end}}{{if .Error}}<br>error: {{.Error}}{{end}}{{if .Plan}}<br>plan: <pre>{{.Plan}}</pre>{{end}}</p>{{else}}no queries{{end}}</div></details>
<details style="display:inline-block;margin-left:12px"><summary style="cursor:pointer">session</summary><div>{{range .Session}}{{.Name}}={{.Value}}<br>{{else}}not exposed{{end}}</div></details>
<details style="display:inline-block;margin-left:12px"><summary style="cursor:pointer">environment</summary><div>{{range .Environment}}{{.Name}}={{.Value}}<br>{{else}}not exposed{{end}}</div></details>
{{if .TraceID}}<span style="margin-left:12px">trace {{.TraceID}}</span>{{end}}</aside>`))

func renderToolbar(view toolbarView) ([]byte, error) {
	var output bytes.Buffer
	if err := toolbarTemplate.Execute(&output, view); err != nil {
		return nil, fmt.Errorf("debug: render toolbar: %w", err)
	}
	return output.Bytes(), nil
}
