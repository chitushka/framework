package web_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	frameworkdebug "github.com/chitushka/framework/debug"
	"github.com/chitushka/framework/observability"
	"github.com/chitushka/framework/web"
)

type auditExporter struct{ spans []observability.Span }

func (e *auditExporter) ExportSpan(_ context.Context, span observability.Span) error {
	e.spans = append(e.spans, span)
	return nil
}

func TestProductionDisablesDiagnosticsAndSanitizesErrorsAndTelemetry(t *testing.T) {
	const secret = "private-db-password"
	t.Setenv("DATABASE_PASSWORD", secret)
	toolbar, err := frameworkdebug.New(frameworkdebug.Config{Enabled: true, Environment: []string{"DATABASE_PASSWORD"}, CaptureSQLArgs: true, Session: func(context.Context) map[string]any {
		t.Fatal("production invoked debug session snapshot")
		return nil
	}})
	must(t, err)
	router := web.NewRouter()
	must(t, router.Use(toolbar.Middleware))
	must(t, router.SetErrorHandler(func(ctx *web.Context, err error) {
		t.Error("production used application error handler")
		_ = ctx.HTML(500, err.Error())
	}))
	must(t, router.GET("/page", func(ctx *web.Context) error { return ctx.HTML(200, "<body>page</body>") }))
	must(t, router.GET("/failure", func(*web.Context) error { return web.NewHTTPError(500, secret, errors.New(secret)) }))
	must(t, router.GET("/bad", func(*web.Context) error { return web.NewHTTPError(400, secret, nil) }))
	must(t, router.GET("/panic/{value}", func(ctx *web.Context) error { ctx.Writer.Header().Set("Set-Cookie", secret); panic(secret) }))
	profiling := observability.PprofHandler()
	must(t, router.GET("/profile", func(ctx *web.Context) error { profiling.ServeHTTP(ctx.Writer, ctx.Request); return nil }))
	var logs bytes.Buffer
	exporter := &auditExporter{}
	tracer, err := observability.NewTracer(exporter)
	must(t, err)
	t.Cleanup(func() { must(t, tracer.Shutdown(context.Background())) })
	observed := observability.HTTP(observability.HTTPConfig{Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Tracer: tracer})(router)
	production, err := web.NewProductionHandler(observed)
	must(t, err)
	for _, tc := range []struct {
		path   string
		status int
	}{{"/page", 200}, {"/failure", 500}, {"/bad", 400}, {"/panic/" + secret, 500}, {"/missing", 404}, {"/debug/pprof/", 404}, {"/%64ebug/pprof/cmdline", 404}, {"/profile", 404}} {
		response := httptest.NewRecorder()
		production.ServeHTTP(response, httptest.NewRequest("GET", tc.path, nil))
		if response.Code != tc.status || strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), "chitu-debug-toolbar") || strings.Contains(response.Header().Get("Set-Cookie"), secret) {
			t.Fatalf("unsafe response for %s: %d %s %v", tc.path, response.Code, response.Body.String(), response.Header())
		}
		if response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing production content-type policy")
		}
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "/panic/") {
		t.Fatal("raw path leaked to logs")
	}
	must(t, tracer.Flush(t.Context()))
	for _, span := range exporter.spans {
		if strings.Contains(span.Name+span.Error+span.Attributes["http.path"], secret) {
			t.Fatal("secret leaked to span")
		}
	}
	if _, err := web.NewProductionHandler(nil); err == nil {
		t.Fatal("nil handler accepted")
	}
}

func TestServerErrorsArePrivateAndInvalidStatusesFailClosed(t *testing.T) {
	for _, status := range []int{500, 503, 0, 99, 200, 700} {
		router := web.NewRouter()
		must(t, router.GET("/", func(*web.Context) error { return web.NewHTTPError(status, "private-backend-secret", nil) }))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
		if response.Code < 500 || response.Code > 599 || strings.Contains(response.Body.String(), "private-backend-secret") {
			t.Fatal("unsafe error status/message")
		}
	}
}

func TestProductionAbortsPartialResponseWithoutLoggingPanic(t *testing.T) {
	production, err := web.NewProductionHandler(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "partial")
		panic("private panic")
	}))
	must(t, err)
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatalf("unsafe partial-response panic: %v", recovered)
		}
	}()
	production.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}

func TestRootedFilesRejectTraversalAndRetainRangeSemantics(t *testing.T) {
	directory := t.TempDir()
	must(t, os.WriteFile(filepath.Join(directory, "file.txt"), []byte("0123456789"), 0600))
	root, err := os.OpenRoot(directory)
	must(t, err)
	t.Cleanup(func() { _ = root.Close() })
	for _, name := range []string{"../outside", "/absolute", "C:/Windows/file", `..\outside`, ".", "dir/../file.txt", "file\x00.txt"} {
		ctx := &web.Context{Writer: httptest.NewRecorder(), Request: httptest.NewRequest("GET", "/", nil)}
		if err := ctx.FileFromRoot(200, root, name); err == nil || ctx.Written() {
			t.Fatalf("accepted unsafe file path: %q", name)
		}
	}
	router := web.NewRouter()
	must(t, router.GET("/file/{name}", func(ctx *web.Context) error {
		return ctx.DownloadFromRoot(200, root, ctx.Param("name"), "download.txt")
	}))
	production, err := web.NewProductionHandler(router)
	must(t, err)
	server := httptest.NewServer(production)
	t.Cleanup(server.Close)
	response, body := readNetworkResponse(t, networkClient(t), "GET", server.URL+"/file/file.txt", http.Header{"Range": {"bytes=2-4"}})
	if response.StatusCode != 206 || string(body) != "234" || response.Header.Get("Content-Disposition") == "" {
		t.Fatal("rooted file broke Range/download")
	}
	response, body = readNetworkResponse(t, networkClient(t), "GET", server.URL+"/file/..%2Foutside", nil)
	if response.StatusCode != 500 || strings.Contains(string(body), directory) {
		t.Fatal("traversal disclosed path")
	}
	t.Run("symlink containment", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "secret.txt")
		must(t, os.WriteFile(outside, []byte("outside secret"), 0600))
		if err := os.Symlink(outside, filepath.Join(directory, "outside.txt")); err != nil {
			if runtime.GOOS == "windows" {
				t.Skip("Windows symlink privilege unavailable")
			}
			t.Fatal(err)
		}
		must(t, os.Symlink("file.txt", filepath.Join(directory, "inside.txt")))
		response, body := readNetworkResponse(t, networkClient(t), "GET", server.URL+"/file/outside.txt", nil)
		if response.StatusCode != 500 || strings.Contains(string(body), "outside secret") {
			t.Fatal("symlink escaped file root")
		}
		response, body = readNetworkResponse(t, networkClient(t), "GET", server.URL+"/file/inside.txt", nil)
		if response.StatusCode != 200 || string(body) != "0123456789" {
			t.Fatal("contained symlink unavailable")
		}
	})
}
