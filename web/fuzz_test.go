package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func FuzzRouterURLRoundTrip(f *testing.F) {
	for _, value := range []string{"alice", "a/b", "%2f", "?x=#", "привет", ".", "..", "\\", "\x00", "", "/a/%2f", "/%2e%2e", "/a//b", "/%FF"} {
		f.Add(value)
	}
	router := NewRouter()
	if err := router.GET("/items/{id}", func(ctx *Context) error {
		return ctx.HTML(http.StatusOK, ctx.Param("id"))
	}, WithName("item")); err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 4096 {
			t.Skip()
		}
		// Also exercise malformed raw paths, before URL generation can reject them.
		if parts, err := requestPath(value); err == nil {
			escaped := make([]string, len(parts))
			for i, part := range parts {
				escaped[i] = url.PathEscape(part)
			}
			again, err := requestPath("/" + strings.Join(escaped, "/"))
			if err != nil || !slices.Equal(parts, again) {
				t.Fatalf("accepted path did not survive canonical escaping: %q", value)
			}
		}
		path, err := router.URL("item", map[string]string{"id": value})
		if err != nil {
			return
		}
		parsed, err := url.ParseRequestURI(path)
		if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			t.Fatalf("generated URL is not a path: %q (%v)", path, err)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Body.String() != value {
			t.Fatalf("URL did not round trip: %q -> %q -> %d %q", value, path, response.Code, response.Body.String())
		}
	})
}

func FuzzBindJSON(f *testing.F) {
	for _, body := range []string{`{"name":"alice","count":2}`, `null`, `{}`, `{"admin":true}`, `{} {}`, `{"count":1e1000}`, `{"name":"\ud800"}`, "", strings.Repeat(" ", 4097)} {
		f.Add(body, "application/json", false)
	}
	f.Add(`{}`, "text/plain", true)
	f.Add(`{}`, "application/problem+json; charset=utf-8", true)
	f.Fuzz(func(t *testing.T, body, contentType string, chunked bool) {
		if len(body) > 8192 || len(contentType) > 256 {
			t.Skip()
		}
		var decoded struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		}
		router := NewRouter()
		if err := router.SetRequestLimits(RequestLimits{MaxBodyBytes: 4096}); err != nil {
			t.Fatal(err)
		}
		if err := router.POST("/", func(ctx *Context) error {
			if err := ctx.BindJSON(&decoded); err != nil {
				return err
			}
			return ctx.NoContent(http.StatusNoContent)
		}); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		request.Header.Set("Content-Type", contentType)
		if chunked {
			request.ContentLength = -1
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code == http.StatusNoContent {
			var expected struct {
				Name  string `json:"name"`
				Count int    `json:"count"`
			}
			if len(body) > 4096 || json.Unmarshal([]byte(body), &expected) != nil || decoded != expected {
				t.Fatalf("accepted oversized/invalid JSON or changed its values: %q", body)
			}
		} else if response.Code != 400 && response.Code != 413 && response.Code != 415 {
			t.Fatalf("unexpected binding status: %d", response.Code)
		}
	})
}
