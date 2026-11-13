package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitushka/framework/auth"
)

func testAPI(t *testing.T) (http.Handler, string) {
	t.Helper()
	v, k := exampleTokens(t)
	c := defaultConfig()
	c.ClientRate, c.SubjectRate = 100000, 100000
	h, err := configuredHandler(v, c)
	if err != nil {
		t.Fatal(err)
	}
	return h, exampleToken(t, k, time.Now().Add(time.Minute))
}

func call(h http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func requireStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("got %d, want %d: %s", w.Code, status, w.Body.String())
	}
	if status >= 400 {
		var body errorBody
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Status != status || body.Error.Code != errorCode(status) || body.Error.Message != http.StatusText(status) {
			t.Fatalf("invalid error: %s", w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable error")
		}
	}
}

func TestCRUDPaginationAndIdempotentState(t *testing.T) {
	h, token := testAPI(t)
	for _, title := range []string{"Zebra", "Alpha", "Alpha"} {
		requireStatus(t, call(h, token, "POST", "/v1/articles", `{"title":"`+title+`"}`), 201)
	}
	w := call(h, token, "GET", "/v1/articles?title=ALP&sort=title&limit=1&offset=1", "")
	requireStatus(t, w, 200)
	var result page
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 1 || result.Data[0].ID != 4 || result.Meta.Total != 2 || result.Meta.Limit != 1 || result.Meta.Offset != 1 {
		t.Fatalf("wrong page: %s", w.Body.String())
	}
	for range 2 {
		requireStatus(t, call(h, token, "PUT", "/v1/articles/2", `{"title":" Replaced "}`), 200)
	}
	w = call(h, token, "GET", "/v1/articles/2", "")
	if !strings.Contains(w.Body.String(), `"title":"Replaced"`) {
		t.Fatal(w.Body.String())
	}
	requireStatus(t, call(h, token, "DELETE", "/v1/articles/2", ""), 204)
	requireStatus(t, call(h, token, "DELETE", "/v1/articles/2", ""), 404)
	requireStatus(t, call(h, token, "PUT", "/v1/articles/2", `{"title":"No upsert"}`), 404)
	w = call(h, token, "GET", "/v1/articles?offset=1000000", "")
	if !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatal(w.Body.String())
	}
}

func TestErrorsAcrossAllBoundaries(t *testing.T) {
	h, token := testAPI(t)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/missing", "", 404}, {"POST", "/v1/token", "", 404}, {"PATCH", "/v1/articles/1", "", 405},
		{"GET", "/debug/pprof", "", 404}, {"GET", "/v1/articles/0", "", 404}, {"GET", "/v1/articles/nope", "", 404},
		{"GET", "/v1/articles/999999999999999999999", "", 404},
		{"POST", "/v1/articles", `{"id":99,"title":"injected"}`, 400}, {"POST", "/v1/articles", `{"title":12}`, 400},
		{"POST", "/v1/articles", `{"title":"ok"} {}`, 400}, {"POST", "/v1/articles", `{`, 400},
		{"POST", "/v1/articles", `null`, 422}, {"POST", "/v1/articles", `{}`, 422}, {"POST", "/v1/articles", `{"title":null}`, 422},
		{"POST", "/v1/articles", `{"title":"  "}`, 422}, {"POST", "/v1/articles", `{"title":"` + strings.Repeat("я", 101) + `"}`, 422},
		{"POST", "/v1/articles", `{"title":"` + strings.Repeat("x", 5000) + `"}`, 413},
		{"GET", "/v1/articles/1?limit=1", "", 400}, {"DELETE", "/v1/articles/1?force=1", "", 400},
	} {
		t.Run(tc.method+tc.path+strconv.Itoa(tc.status)+tc.body[:min(len(tc.body), 30)], func(t *testing.T) { requireStatus(t, call(h, token, tc.method, tc.path, tc.body), tc.status) })
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=-1", "limit=", "offset=-1", "offset=1000001", "limit=1&limit=2", "sort=owner", "unknown=1", "sort=", "title=%zz", "title=a;b", "title=" + strings.Repeat("x", 201), "x=" + strings.Repeat("x", 2050)} {
		requireStatus(t, call(h, token, "GET", "/v1/articles?"+query, ""), 400)
	}
	for _, header := range []string{"Content-Type", "Idempotency-Key", "Content-Encoding"} {
		r := httptest.NewRequest("POST", "/v1/articles", strings.NewReader(`{"title":"valid"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(header, "unsupported")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		status := 400
		if header == "Content-Type" {
			status = 415
		}
		requireStatus(t, w, status)
	}
	r := httptest.NewRequest("POST", "/v1/articles", strings.NewReader(`{"title":"`+strings.Repeat("x", 5000)+`"}`))
	r.ContentLength = -1
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	requireStatus(t, w, 413)
	w = call(h, token, "OPTIONS", "/v1/articles/1", "")
	requireStatus(t, w, 204)
	if w.Header().Get("Allow") != "DELETE, GET, HEAD, OPTIONS, PUT" {
		t.Fatal(w.Header())
	}
	w = call(h, token, "HEAD", "/missing", "")
	if w.Code != 404 || w.Body.Len() != 0 {
		t.Fatal("HEAD body/status")
	}
}

type verifierFunc func(context.Context, string) (auth.Principal, error)

func (f verifierFunc) Verify(ctx context.Context, token string) (auth.Principal, error) {
	return f(ctx, token)
}

func TestUnavailableAndPanicAreGeneric(t *testing.T) {
	for _, verify := range []verifierFunc{
		func(context.Context, string) (auth.Principal, error) {
			return auth.Principal{}, errors.New("secret dependency URL")
		},
		func(context.Context, string) (auth.Principal, error) { panic("private key") },
	} {
		h, err := newHandler(verify)
		if err != nil {
			t.Fatal(err)
		}
		w := call(h, "token", "GET", "/v1/articles", "")
		if w.Code != 500 && w.Code != 503 {
			t.Fatal(w.Code)
		}
		requireStatus(t, w, w.Code)
	}
}

func TestRatesOriginTrustAndCapacity(t *testing.T) {
	v, k := exampleTokens(t)
	token := exampleToken(t, k, time.Now().Add(time.Minute))
	c := defaultConfig()
	c.ClientRate = 1
	h, err := configuredHandler(v, c)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, call(h, "", "GET", "/v1/articles", ""), 401)
	r := httptest.NewRequest("GET", "/v1/articles", nil)
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	requireStatus(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing retry")
	}
	c.ClientRate = 100
	c.SubjectRate = 1
	c.Proxy.TrustedProxies = []string{"192.0.2.0/24"}
	c.Proxy.ForwardedHeader = "X-Forwarded"
	h, err = configuredHandler(v, c)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		r := httptest.NewRequest("GET", "/v1/articles", nil)
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("10.0.0.%d", i))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		status := 200
		if i == 2 {
			status = 429
		}
		requireStatus(t, w, status)
	}
	c = defaultConfig()
	c.MaxArticles = 1
	c.Proxy.AllowedHosts = []string{"api.example"}
	h, err = configuredHandler(v, c)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, call(h, token, "GET", "http://bad.example/v1/articles", ""), 400)
	requireStatus(t, call(h, token, "POST", "http://api.example/v1/articles", `{"title":"full"}`), 503)
	l := newRateLimiter(1)
	l.capacity = 1
	now := time.Now()
	l.now = func() time.Time { return now }
	if l.allow("one") != 0 || l.allow("two") == 0 || l.allow("one") == 0 {
		t.Fatal("limits evicted")
	}
	now = now.Add(time.Minute)
	if l.allow("two") != 0 || len(l.entries) != 1 {
		t.Fatal("expired capacity not reclaimed")
	}
}

func TestConcurrentCRUDAndRateAdmission(t *testing.T) {
	h, token := testAPI(t)
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			w := call(h, token, "POST", "/v1/articles", `{"title":"parallel"}`)
			if w.Code != 201 {
				t.Errorf("create: %d", w.Code)
				return
			}
			path := w.Header().Get("Location")
			for _, method := range []string{"GET", "PUT", "DELETE"} {
				w = call(h, token, method, path, `{"title":"changed"}`)
				if w.Code != 200 && w.Code != 204 {
					t.Errorf("%s: %d", method, w.Code)
				}
			}
		})
	}
	wg.Wait()
	l := newRateLimiter(5)
	var mu sync.Mutex
	accepted := 0
	for range 50 {
		wg.Go(func() {
			if l.allow("shared") == 0 {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if accepted != 5 {
		t.Fatalf("admitted %d", accepted)
	}
}
