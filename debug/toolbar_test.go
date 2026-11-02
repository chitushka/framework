package debug_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chitushka/framework/db"
	frameworkdebug "github.com/chitushka/framework/debug"
	"github.com/chitushka/framework/observability"
	"github.com/chitushka/framework/web"
)

func TestToolbarInjectsRequestSQLSessionEnvironmentAndTrace(t *testing.T) {
	t.Setenv("CHITU_DEBUG_SAFE", `<development>`)
	toolbar, err := frameworkdebug.New(frameworkdebug.Config{
		Enabled: true, Environment: []string{"CHITU_DEBUG_SAFE"}, CaptureSQLArgs: true,
		Session: func(context.Context) map[string]any { return map[string]any{"user": `<admin>`} },
	})
	if err != nil {
		t.Fatal(err)
	}
	router := web.NewRouter()
	if err := router.GET("/users/{id}", func(ctx *web.Context) error {
		query := "SELECT * FROM users WHERE id = ?"
		frameworkdebug.RecordSQL(ctx.Request.Context(), db.QueryEvent{Operation: "query", Query: query, Args: []any{42}, Duration: 2 * time.Millisecond})
		frameworkdebug.RecordSQLPlan(ctx.Request.Context(), query, "Index Scan <users>")
		return ctx.HTML(http.StatusOK, "<!doctype html><body><h1>User</h1></body>")
	}, web.WithName("users.show"), web.WithMiddleware(toolbar.Middleware)); err != nil {
		t.Fatal(err)
	}
	tracer, _ := observability.NewTracer()
	response := httptest.NewRecorder()
	observability.HTTP(observability.HTTPConfig{Tracer: tracer})(router).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/users/42", nil))
	body := response.Body.String()
	for _, expected := range []string{"chitu-debug-toolbar", "/users/{id}", "users.show", "id=42", "SQL (1)", "SELECT * FROM users", "Index Scan &lt;users&gt;", "CHITU_DEBUG_SAFE=&lt;development&gt;", "user=&lt;admin&gt;", "trace "} {
		if !strings.Contains(body, expected) {
			t.Fatalf("toolbar misses %q:\n%s", expected, body)
		}
	}
	if strings.Index(body, "chitu-debug-toolbar") > strings.Index(body, "</body>") {
		t.Fatal("toolbar was injected after body")
	}
}

func TestToolbarLeavesNonHTMLAndErrorsIntact(t *testing.T) {
	toolbar, _ := frameworkdebug.New(frameworkdebug.Config{Enabled: true})
	router := web.NewRouter()
	_ = router.GET("/json", func(ctx *web.Context) error { return ctx.JSON(http.StatusOK, map[string]bool{"ok": true}) }, web.WithMiddleware(toolbar.Middleware))
	_ = router.GET("/error", func(*web.Context) error { return web.NewHTTPError(http.StatusTeapot, "teapot", errors.New("broken")) }, web.WithMiddleware(toolbar.Middleware))
	for path := range map[string]bool{"/json": true, "/error": true} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if strings.Contains(response.Body.String(), "chitu-debug-toolbar") {
			t.Fatalf("toolbar injected into %s: %s", path, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/error", nil))
	if response.Code != http.StatusTeapot || !strings.Contains(response.Body.String(), "teapot") {
		t.Fatalf("error response was lost: %d %s", response.Code, response.Body.String())
	}
}

func TestToolbarDisabledValidationAndQueryLimit(t *testing.T) {
	if _, err := frameworkdebug.New(frameworkdebug.Config{MaxSQLQueries: -1}); err == nil {
		t.Fatal("expected invalid query limit")
	}
	disabled, _ := frameworkdebug.New(frameworkdebug.Config{})
	handler := disabled.Middleware(func(ctx *web.Context) error { return ctx.HTML(http.StatusOK, "<body>plain</body>") })
	response := httptest.NewRecorder()
	ctx := &web.Context{Writer: response, Request: httptest.NewRequest(http.MethodGet, "/", nil)}
	if err := handler(ctx); err != nil || response.Body.String() != "<body>plain</body>" {
		t.Fatalf("disabled toolbar changed response: %v %s", err, response.Body.String())
	}
}
