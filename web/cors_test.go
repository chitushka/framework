package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chitushka/framework/web"
)

func TestCORSScenarios(t *testing.T) {
	for _, tc := range []struct {
		name, method    string
		headers         http.Header
		status          int
		called, allowed bool
	}{
		{"no Origin", "GET", http.Header{}, 201, true, false},
		{"same origin is not CORS", "DELETE", http.Header{"Origin": {"http://example.com"}}, 201, true, false},
		{"actual", "POST", http.Header{"Origin": {"https://ui.example"}}, 201, true, true},
		{"preflight", "OPTIONS", http.Header{"Origin": {"https://ui.example"}, "Access-Control-Request-Method": {"POST"}, "Access-Control-Request-Headers": {"Content-Type, AUTHORIZATION"}}, 204, false, true},
		{"preflight no headers", "OPTIONS", http.Header{"Origin": {"https://ui.example"}, "Access-Control-Request-Method": {"GET"}}, 204, false, true},
		{"unknown origin", "POST", http.Header{"Origin": {"https://evil.example"}}, 403, false, false},
		{"null origin", "POST", http.Header{"Origin": {"null"}}, 403, false, false},
		{"origin path", "POST", http.Header{"Origin": {"https://ui.example/path"}}, 403, false, false},
		{"origin slash", "POST", http.Header{"Origin": {"https://ui.example/"}}, 403, false, false},
		{"multiple origins", "GET", http.Header{"Origin": {"https://ui.example", "https://evil.example"}}, 403, false, false},
		{"origin fragment", "GET", http.Header{"Origin": {"https://ui.example#"}}, 403, false, false},
		{"actual method denied", "DELETE", http.Header{"Origin": {"https://ui.example"}}, 403, false, false},
		{"preflight method denied", "OPTIONS", http.Header{"Origin": {"https://ui.example"}, "Access-Control-Request-Method": {"DELETE"}}, 403, false, false},
		{"duplicate method", "OPTIONS", http.Header{"Origin": {"https://ui.example"}, "Access-Control-Request-Method": {"POST", "GET"}}, 403, false, false},
		{"header denied", "OPTIONS", http.Header{"Origin": {"https://ui.example"}, "Access-Control-Request-Method": {"POST"}, "Access-Control-Request-Headers": {"X-Secret"}}, 403, false, false},
		{"malformed header list", "OPTIONS", http.Header{"Origin": {"https://ui.example"}, "Access-Control-Request-Method": {"POST"}, "Access-Control-Request-Headers": {"Content-Type,,Authorization"}}, 403, false, false},
		{"OPTIONS without Origin", "OPTIONS", http.Header{"Access-Control-Request-Method": {"POST"}}, 201, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler, err := web.NewCORSHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(201) }), web.CORSConfig{
				AllowedOrigins: []string{"https://ui.example"}, AllowedMethods: []string{"GET", "POST"},
				AllowedHeaders: []string{"Content-Type", "Authorization"}, ExposedHeaders: []string{"X-Total"}, AllowCredentials: true, MaxAgeSeconds: 600,
			})
			must(t, err)
			request := httptest.NewRequest(tc.method, "/", nil)
			request.Header = tc.headers.Clone()
			response := httptest.NewRecorder()
			response.Header().Set("Vary", "Accept-Encoding, origin")
			handler.ServeHTTP(response, request)
			if response.Code != tc.status || called != tc.called {
				t.Fatalf("status=%d called=%v", response.Code, called)
			}
			if tc.allowed {
				if response.Header().Get("Access-Control-Allow-Origin") != "https://ui.example" || response.Header().Get("Access-Control-Allow-Credentials") != "true" {
					t.Fatalf("CORS headers: %v", response.Header())
				}
				if tc.status == 204 {
					if response.Body.Len() != 0 || response.Header().Get("Access-Control-Max-Age") != "600" || !strings.Contains(response.Header().Get("Access-Control-Allow-Headers"), "authorization") {
						t.Fatal("invalid preflight response")
					}
				} else if response.Header().Get("Access-Control-Expose-Headers") != "x-total" {
					t.Fatal("missing exposed headers")
				}
			} else if response.Header().Get("Access-Control-Allow-Origin") != "" || response.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatal("CORS granted to rejected request")
			}
			vary := strings.ToLower(strings.Join(response.Header().Values("Vary"), ","))
			if strings.Count(vary, "origin") != 1 || !strings.Contains(vary, "accept-encoding") {
				t.Fatalf("invalid Vary: %s", vary)
			}
			if tc.method == "OPTIONS" && (!strings.Contains(vary, "access-control-request-method") || !strings.Contains(vary, "access-control-request-headers")) {
				t.Fatal("missing preflight Vary")
			}
		})
	}
}

func TestCORSValidationWildcardAndRouterErrors(t *testing.T) {
	for _, config := range []web.CORSConfig{
		{AllowedOrigins: []string{"*"}, AllowCredentials: true}, {AllowedOrigins: []string{"null"}},
		{AllowedMethods: []string{"GET\r\nX-Evil:1"}}, {AllowedHeaders: []string{"*"}},
		{ExposedHeaders: []string{"bad header"}}, {MaxAgeSeconds: -1}, {MaxAgeSeconds: 86401},
	} {
		if _, err := web.NewCORSHandler(http.NotFoundHandler(), config); err == nil {
			t.Fatalf("accepted %+v", config)
		}
	}
	if _, err := web.NewCORSHandler(nil, web.CORSConfig{}); err == nil {
		t.Fatal("accepted nil handler")
	}
	router := web.NewRouter()
	must(t, router.GET("/", func(ctx *web.Context) error { return ctx.NoContent(204) }))
	handler, err := web.NewCORSHandler(router, web.CORSConfig{AllowedOrigins: []string{"*"}})
	must(t, err)
	for _, path := range []string{"/", "/missing"} {
		request := httptest.NewRequest("GET", path, nil)
		request.Header.Set("Origin", "https://any.example")
		response := httptest.NewRecorder()
		response.Header().Set("Vary", "*")
		handler.ServeHTTP(response, request)
		if response.Header().Get("Access-Control-Allow-Origin") != "*" || response.Header().Get("Access-Control-Allow-Credentials") != "" || response.Header().Get("Vary") != "*" {
			t.Fatalf("invalid wildcard response: %v", response.Header())
		}
		if path == "/missing" && response.Code != 404 {
			t.Fatal("CORS changed router error")
		}
	}
	// Host validation must precede even a successful preflight response.
	trusted, err := web.NewProxyHandler(handler, web.ProxyConfig{AllowedHosts: []string{"app.example"}})
	must(t, err)
	request := httptest.NewRequest("OPTIONS", "http://evil.example/", nil)
	request.Header.Set("Origin", "https://ui.example")
	request.Header.Set("Access-Control-Request-Method", "GET")
	response := httptest.NewRecorder()
	trusted.ServeHTTP(response, request)
	if response.Code != 400 {
		t.Fatal("preflight bypassed host policy")
	}
}

func TestCORSSameOriginUsesTrustedProxyMetadata(t *testing.T) {
	router := web.NewRouter()
	must(t, router.POST("/", func(ctx *web.Context) error {
		if ctx.Scheme() != "https" || ctx.Host() != "app.example" {
			t.Error("outer trust metadata was lost")
		}
		return ctx.Redirect(302, "https://login.example/callback")
	}))
	cors, err := web.NewCORSHandler(router, web.CORSConfig{})
	must(t, err)
	handler, err := web.NewProxyHandler(cors, web.ProxyConfig{
		TrustedProxies: []string{"10.0.0.0/24"}, ForwardedHeader: "X-Forwarded", AllowedHosts: []string{"app.example"},
		AllowedRedirectOrigins: []string{"https://login.example"},
	})
	must(t, err)
	request := httptest.NewRequest("POST", "http://internal.example/", nil)
	request.RemoteAddr = "10.0.0.1:1234"
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "app.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 302 || response.Header().Get("Access-Control-Allow-Origin") != "" || response.Header().Get("Location") != "https://login.example/callback" {
		t.Fatalf("same-origin request failed: %d %s", response.Code, response.Body.String())
	}
}
