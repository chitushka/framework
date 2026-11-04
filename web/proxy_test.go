package web_test

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chitushka/framework/web"
)

func TestProxyTrustScenarios(t *testing.T) {
	for _, tc := range []struct {
		name, peer, family string
		headers            http.Header
		tls                bool
		status             int
		ip, scheme, host   string
	}{
		{"direct spoof", "198.51.100.9:1234", "Forwarded", http.Header{"Forwarded": {`for=6.6.6.6;proto=https;host=evil.example`}, "X-Forwarded-For": {"6.6.6.6"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"evil.example"}}, false, 204, "198.51.100.9", "http", "app.example"},
		{"direct TLS malformed spoof", "198.51.100.9:1234", "X-Forwarded", http.Header{"Forwarded": {`"bad`}, "X-Forwarded-Host": {"evil, bad"}, "X-Forwarded-Proto": {"http"}}, true, 204, "198.51.100.9", "https", "app.example"},
		{"headers disabled", "10.0.0.1:1234", "", http.Header{"Forwarded": {`for=6.6.6.6;proto=https;host=evil.example`}}, false, 204, "10.0.0.1", "http", "app.example"},
		{"trusted X single", "10.0.0.1:1234", "X-Forwarded", http.Header{"X-Forwarded-For": {"198.51.100.9"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"APP.EXAMPLE:8443"}}, false, 204, "198.51.100.9", "https", "app.example:8443"},
		{"trusted X chain", "10.0.0.3:1234", "X-Forwarded", http.Header{"X-Forwarded-For": {"6.6.6.6, 198.51.100.9", "10.0.0.2"}, "X-Forwarded-Proto": {"https"}}, false, 204, "198.51.100.9", "https", "app.example"},
		{"trusted metadata without XFF", "10.0.0.1:1234", "X-Forwarded", http.Header{"X-Forwarded-Proto": {"https"}}, false, 204, "10.0.0.1", "https", "app.example"},
		{"mapped peer", "[::ffff:10.0.0.1]:1234", "X-Forwarded", http.Header{"X-Forwarded-For": {"198.51.100.9"}}, false, 204, "198.51.100.9", "http", "app.example"},
		{"Forwarded chain", "10.0.0.3:1234", "Forwarded", http.Header{"Forwarded": {`for=6.6.6.6;host=evil.example, for=198.51.100.9;proto=https;host=app.example, for=10.0.0.2;proto=http;host=internal.example`}}, false, 204, "198.51.100.9", "https", "app.example"},
		{"quoted IPv6", "[2001:db8:ffff::1]:1234", "Forwarded", http.Header{"Forwarded": {`for="[2001:db8::2]:1234";proto=https;host="[2001:db8::1]:8443"`}}, false, 204, "2001:db8::2", "https", "[2001:db8::1]:8443"},
		{"no family merging", "10.0.0.1:1234", "Forwarded", http.Header{"Forwarded": {`for=198.51.100.9;proto=https`}, "X-Forwarded-Host": {"evil.example"}}, false, 204, "198.51.100.9", "https", "app.example"},
		{"metadata bound to selected hop", "10.0.0.3:1234", "Forwarded", http.Header{"Forwarded": {`for=198.51.100.9, for=10.0.0.2;proto=https;host=evil.example`}}, false, 204, "198.51.100.9", "http", "app.example"},
		{"invalid forwarded IP", "10.0.0.1:1234", "Forwarded", http.Header{"Forwarded": {`for=unknown`}}, false, 400, "", "", ""},
		{"duplicate parameter", "10.0.0.1:1234", "Forwarded", http.Header{"Forwarded": {`for=198.51.100.9;for=6.6.6.6`}}, false, 400, "", "", ""},
		{"broken quote", "10.0.0.1:1234", "Forwarded", http.Header{"Forwarded": {`for="198.51.100.9`}}, false, 400, "", "", ""},
		{"Go escapes are not RFC escapes", "10.0.0.1:1234", "Forwarded", http.Header{"Forwarded": {`for="198\x2e51.100.9"`}}, false, 400, "", "", ""},
		{"bad scheme", "10.0.0.1:1234", "Forwarded", http.Header{"Forwarded": {`for=198.51.100.9;proto=ftp`}}, false, 400, "", "", ""},
		{"bad host", "10.0.0.1:1234", "Forwarded", http.Header{"Forwarded": {`for=198.51.100.9;host=evil.example`}}, false, 400, "", "", ""},
		{"X host list", "10.0.0.1:1234", "X-Forwarded", http.Header{"X-Forwarded-Host": {"app.example, evil.example"}}, false, 400, "", "", ""},
		{"X proto repeated", "10.0.0.1:1234", "X-Forwarded", http.Header{"X-Forwarded-Proto": {"https", "http"}}, false, 400, "", "", ""},
		{"X invalid IP", "10.0.0.1:1234", "X-Forwarded", http.Header{"X-Forwarded-For": {"unknown"}}, false, 400, "", "", ""},
		{"long chain", "10.0.0.1:1234", "X-Forwarded", http.Header{"X-Forwarded-For": {strings.Repeat("10.0.0.2,", 32) + "198.51.100.9"}}, false, 400, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := web.NewRouter()
			must(t, router.SetProxyPolicy(web.ProxyConfig{
				TrustedProxies: []string{"10.0.0.0/24", "2001:db8:ffff::/48"}, ForwardedHeader: tc.family,
				AllowedHosts: []string{"app.example", "app.example:8443", "[2001:db8::1]:8443"},
			}))
			called := false
			must(t, router.GET("/", func(ctx *web.Context) error {
				called = true
				if ctx.ClientIP() != tc.ip || ctx.Scheme() != tc.scheme || ctx.Host() != tc.host || ctx.Request.Host != tc.host {
					t.Errorf("got IP=%q scheme=%q host=%q, want %q %q %q", ctx.ClientIP(), ctx.Scheme(), ctx.Host(), tc.ip, tc.scheme, tc.host)
				}
				return ctx.NoContent(204)
			}))
			request := httptest.NewRequest("GET", "http://app.example/", nil)
			request.RemoteAddr, request.Header = tc.peer, tc.headers.Clone()
			if tc.tls {
				request.TLS = &tls.ConnectionState{}
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status || called != (tc.status == 204) {
				t.Fatalf("status=%d called=%v: %s", response.Code, called, response.Body.String())
			}
		})
	}
}

func TestHostPolicyAndHostRouting(t *testing.T) {
	for _, host := range []string{"evil.example", "app.example:0", "app.example:65536", "app.example:", "user@app.example", "app.example/evil", "app.example?x=1", "app.example#x", "app.example%2f.evil", "app..example", "bad_host", "[fe80::1%25eth0]", "[app.example]", "[127.0.0.1]"} {
		t.Run(host, func(t *testing.T) {
			router := web.NewRouter()
			must(t, router.SetProxyPolicy(web.ProxyConfig{AllowedHosts: []string{"app.example"}}))
			must(t, router.Use(func(web.Handler) web.Handler {
				return func(*web.Context) error { t.Error("bad host reached middleware"); return nil }
			}))
			must(t, router.GET("/", func(ctx *web.Context) error { return ctx.NoContent(204) }))
			request := httptest.NewRequest("GET", "/", nil)
			request.Host = host
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != 400 {
				t.Fatalf("status=%d", response.Code)
			}
		})
	}
	router := web.NewRouter()
	must(t, router.SetProxyPolicy(web.ProxyConfig{TrustedProxies: []string{"10.0.0.0/24"}, ForwardedHeader: "X-Forwarded", AllowedHosts: []string{"app.example"}}))
	must(t, router.GET("/", func(ctx *web.Context) error { return ctx.NoContent(204) }, web.WithHost("app.example")))
	request := httptest.NewRequest("GET", "http://internal.example/", nil)
	request.RemoteAddr = "10.0.0.1:1234"
	request.Header.Set("X-Forwarded-Host", "APP.EXAMPLE.")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 204 {
		t.Fatalf("validated host was not used for routing: %d", response.Code)
	}
}

func TestRedirectPolicy(t *testing.T) {
	for _, tc := range []struct {
		location string
		allowed  bool
	}{
		{"/home?return=https%3A%2F%2Fevil.example#section", true},
		{"//evil.example/path", false}, {"/%2fevil.example", false},
		{"/\\evil.example", false}, {"/%5cevil.example", false},
		{"javascript:alert(1)", false}, {"https:evil.example", false},
		{"relative", false}, {"", false}, {"/ok\r\nX-Evil:yes", false}, {"/%0aevil", false},
		{"https://login.example/callback", true}, {"https://LOGIN.EXAMPLE:443/callback", true},
		{"https://login.example.evil/callback", false}, {"http://login.example/callback", false},
		{"https://login.example:8443/callback", false}, {"https://user@login.example/callback", false},
		{"https://login.example./callback", false}, {"https://app.example/", false},
	} {
		t.Run(tc.location, func(t *testing.T) {
			router := web.NewRouter()
			must(t, router.SetProxyPolicy(web.ProxyConfig{AllowedRedirectOrigins: []string{"https://login.example"}}))
			must(t, router.GET("/", func(ctx *web.Context) error { return ctx.Redirect(302, tc.location) }))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("GET", "http://app.example/", nil))
			if tc.allowed {
				if response.Code != 302 || response.Header().Get("Location") == "" {
					t.Fatalf("redirect rejected: %d %s", response.Code, response.Body.String())
				}
			} else if response.Code != 400 || response.Header().Get("Location") != "" {
				t.Fatalf("unsafe redirect accepted: %d %s", response.Code, response.Header())
			}
		})
	}
	ctx := &web.Context{Writer: httptest.NewRecorder(), Request: httptest.NewRequest("GET", "/", nil)}
	if err := ctx.Redirect(302, "https://evil.example"); err == nil || ctx.Written() {
		t.Fatal("manual context allowed external redirect")
	}
}

func TestNestedProxyBoundariesCannotExpandTrust(t *testing.T) {
	router := web.NewRouter()
	must(t, router.SetProxyPolicy(web.ProxyConfig{TrustedProxies: []string{"0.0.0.0/0"}, ForwardedHeader: "X-Forwarded"}))
	must(t, router.GET("/", func(ctx *web.Context) error {
		if ctx.ClientIP() != "198.51.100.9" || ctx.Scheme() != "http" {
			t.Error("inner policy reinterpreted untrusted headers")
		}
		return ctx.Redirect(302, "https://login.example/")
	}))
	handler, err := web.NewProxyHandler(router, web.ProxyConfig{AllowedRedirectOrigins: []string{"https://login.example"}})
	must(t, err)
	request := httptest.NewRequest("GET", "http://app.example/", nil)
	request.RemoteAddr = "198.51.100.9:1234"
	request.Header.Set("X-Forwarded-For", "6.6.6.6")
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 400 {
		t.Fatalf("inner redirect policy was expanded: %d", response.Code)
	}
}

func TestProxyConfigValidationAndManualAccessors(t *testing.T) {
	for _, config := range []web.ProxyConfig{
		{TrustedProxies: []string{"bad"}}, {TrustedProxies: []string{"::ffff:10.0.0.1/128"}}, {ForwardedHeader: "auto"},
		{AllowedHosts: []string{"*"}}, {AllowedRedirectOrigins: []string{"https://app.example/path"}},
		{AllowedRedirectOrigins: []string{"javascript:evil"}},
	} {
		if _, err := web.NewProxyPolicy(config); err == nil {
			t.Fatalf("accepted %+v", config)
		}
		if err := web.NewRouter().SetProxyPolicy(config); err == nil {
			t.Fatal("router accepted invalid config")
		}
	}
	if _, err := web.NewProxyHandler(nil, web.ProxyConfig{}); err == nil {
		t.Fatal("accepted nil handler")
	}
	r := httptest.NewRequest("GET", "https://app.example/", nil)
	r.RemoteAddr = "[2001:db8::1]:1234"
	r.Header.Set("X-Forwarded-For", "6.6.6.6")
	ctx := &web.Context{Request: r}
	if ctx.ClientIP() != "2001:db8::1" || ctx.Scheme() != "https" || ctx.Host() != "app.example" {
		t.Fatal("manual accessors trusted headers")
	}
	r.RemoteAddr, r.Host = "unknown", "bad/host"
	if ctx.ClientIP() != "" || ctx.Host() != "" {
		t.Fatal("manual accessors accepted invalid metadata")
	}
}

func TestProxyBoundaryOverHTTP(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(map[bool]string{false: "untrusted peer", true: "trusted peer"}[trusted], func(t *testing.T) {
			router := web.NewRouter()
			config := web.ProxyConfig{ForwardedHeader: "Forwarded", AllowedHosts: []string{"app.example", "tenant.app.example"}}
			if trusted {
				config.TrustedProxies = []string{"127.0.0.1/32"}
			}
			must(t, router.SetProxyPolicy(config))
			must(t, router.GET("/", func(ctx *web.Context) error {
				return ctx.JSON(200, map[string]string{"ip": ctx.ClientIP(), "scheme": ctx.Scheme(), "host": ctx.Host()})
			}))
			server := startLimitedServer(t, web.ServerConfig{}, router)
			transport := &http.Transport{}
			t.Cleanup(transport.CloseIdleConnections)
			client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
			request, err := http.NewRequestWithContext(t.Context(), "GET", "http://"+server.Address()+"/", nil)
			must(t, err)
			request.Host = "app.example"
			request.Header.Set("Forwarded", "for=198.51.100.9;proto=https;host=tenant.app.example")
			response, err := client.Do(request)
			must(t, err)
			defer func() { _ = response.Body.Close() }()
			var info map[string]string
			must(t, json.NewDecoder(response.Body).Decode(&info))
			if response.StatusCode != 200 {
				t.Fatalf("status=%d", response.StatusCode)
			}
			want := map[string]string{"ip": "127.0.0.1", "scheme": "http", "host": "app.example"}
			if trusted {
				want = map[string]string{"ip": "198.51.100.9", "scheme": "https", "host": "tenant.app.example"}
			}
			for key, value := range want {
				if info[key] != value {
					t.Errorf("%s=%q want %q", key, info[key], value)
				}
			}
		})
	}
}
