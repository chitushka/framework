package security_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chitushka/framework/security"
	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

func TestCSRFIssuesAndValidatesToken(t *testing.T) {
	manager, _ := session.New(session.NewMemoryBackend(), session.Config{Name: "sid"})
	router := web.NewRouter()
	_ = router.Use(manager.Middleware, security.CSRF(security.CSRFConfig{}))
	_ = router.GET("/", func(ctx *web.Context) error { return ctx.NoContent(204) })
	_ = router.POST("/", func(ctx *web.Context) error { return ctx.NoContent(204) })
	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	token := first.Header().Get("X-CSRF-Token")
	cookies := first.Result().Cookies()
	if token == "" || len(cookies) != 1 {
		t.Fatalf("token=%q cookies=%#v", token, cookies)
	}
	badRequest := httptest.NewRequest(http.MethodPost, "/", nil)
	badRequest.AddCookie(cookies[0])
	bad := httptest.NewRecorder()
	router.ServeHTTP(bad, badRequest)
	if bad.Code != http.StatusForbidden {
		t.Fatalf("bad status = %d", bad.Code)
	}
	goodRequest := httptest.NewRequest(http.MethodPost, "/", nil)
	goodRequest.AddCookie(cookies[0])
	goodRequest.Header.Set("X-CSRF-Token", token)
	good := httptest.NewRecorder()
	router.ServeHTTP(good, goodRequest)
	if good.Code != http.StatusNoContent {
		t.Fatalf("good status = %d body=%s", good.Code, good.Body.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	router := web.NewRouter()
	_ = router.Use(security.Headers(security.HeadersConfig{ContentSecurityPolicy: "default-src 'self'", HSTS: "max-age=31536000"}))
	_ = router.GET("/", func(ctx *web.Context) error { return ctx.NoContent(204) })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Header().Get("X-Frame-Options") != "DENY" || response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("Strict-Transport-Security") == "" {
		t.Fatalf("headers = %#v", response.Header())
	}
}
