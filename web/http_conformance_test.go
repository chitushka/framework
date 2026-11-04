package web_test

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chitushka/framework/web"
)

func networkClient(t *testing.T) *http.Client {
	t.Helper()
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 3 * time.Second}
}

func readNetworkResponse(t *testing.T, client *http.Client, method, target string, headers http.Header) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, target, nil)
	must(t, err)
	request.Header = headers.Clone()
	response, err := client.Do(request)
	must(t, err)
	body, err := io.ReadAll(response.Body)
	must(t, err)
	must(t, response.Body.Close())
	return response, body
}

func TestHTTPMethodsAndPathsOverNetwork(t *testing.T) {
	router := web.NewRouter()
	must(t, router.GET("/items/{id}", func(ctx *web.Context) error {
		ctx.Writer.Header().Set("X-Method", ctx.Request.Method)
		return ctx.HTML(200, ctx.Param("id"))
	}, web.WithName("item")))
	must(t, router.POST("/items/{id}", func(ctx *web.Context) error { return ctx.NoContent(201) }))
	must(t, router.GET("/explicit", func(ctx *web.Context) error { return ctx.HTML(200, "GET") }))
	must(t, router.Handle("HEAD", "/explicit", func(ctx *web.Context) error { ctx.Writer.Header().Set("X-Explicit", "yes"); return ctx.NoContent(204) }))
	must(t, router.Handle("OPTIONS", "/explicit", func(ctx *web.Context) error { return ctx.NoContent(202) }))
	must(t, router.GET("/literal/100% café", func(ctx *web.Context) error { return ctx.NoContent(204) }, web.WithName("literal")))
	must(t, router.GET("/raw-error", func(ctx *web.Context) error {
		_, err := io.WriteString(ctx.Writer, "partial")
		if err != nil {
			return err
		}
		return errors.New("late error")
	}))
	server := startLimitedServer(t, web.ServerConfig{}, router)
	base, client := "http://"+server.Address(), networkClient(t)
	for _, tc := range []struct {
		method, path string
		status       int
		body, allow  string
	}{
		{"GET", "/items/a%2Fb", 200, "a/b", ""},
		{"GET", "/items/a%252Fb", 200, "a%2Fb", ""},
		{"GET", "/items/%D0%98%D0%B2%D0%B0%D0%BD", 200, "Иван", ""},
		{"GET", "/items/last/", 200, "last", ""},
		{"GET", "/items/a/b", 404, "", ""},
		{"GET", "/items//bad", 400, "", ""}, {"GET", "//items/bad", 400, "", ""},
		{"GET", "/items/%2e%2e", 400, "", ""}, {"GET", "/items/..", 400, "", ""},
		{"GET", "/items/%00", 400, "", ""}, {"GET", "/items/%FF", 400, "", ""}, {"GET", "/items/%5Cbad", 400, "", ""},
		{"HEAD", "/items/value", 200, "", ""},
		{"HEAD", "/missing", 404, "", ""},
		{"OPTIONS", "/items/value", 204, "", "GET, HEAD, OPTIONS, POST"},
		{"PUT", "/items/value", 405, "", "GET, HEAD, OPTIONS, POST"},
		{"OPTIONS", "/missing", 404, "", ""},
		{"HEAD", "/explicit", 204, "", ""}, {"OPTIONS", "/explicit", 202, "", ""},
		{"GET", "/raw-error", 200, "partial", ""},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			response, body := readNetworkResponse(t, client, tc.method, base+tc.path, nil)
			if response.StatusCode != tc.status || response.Header.Get("Allow") != tc.allow {
				t.Fatalf("status=%d Allow=%q", response.StatusCode, response.Header.Get("Allow"))
			}
			if tc.status < 400 || tc.method == "HEAD" {
				if string(body) != tc.body {
					t.Fatalf("body=%q want %q", body, tc.body)
				}
			}
			if tc.method == "HEAD" && tc.path == "/items/value" && (response.ContentLength != 5 || response.Header.Get("X-Method") != "HEAD") {
				t.Fatal("HEAD fallback changed method or lost representation length")
			}
			if tc.method == "HEAD" && tc.path == "/explicit" && response.Header.Get("X-Explicit") != "yes" {
				t.Fatal("explicit HEAD was shadowed by GET")
			}
		})
	}
	generated, err := router.URL("item", map[string]string{"id": "a/b?c#% café"})
	must(t, err)
	response, body := readNetworkResponse(t, client, "GET", base+generated, nil)
	if response.StatusCode != 200 || string(body) != "a/b?c#% café" {
		t.Fatalf("escaped parameter did not round trip: %s %q", generated, body)
	}
	generated, err = router.URL("literal", nil)
	must(t, err)
	response, _ = readNetworkResponse(t, client, "GET", base+generated, nil)
	if response.StatusCode != 204 || !strings.Contains(generated, "%25") {
		t.Fatalf("literal URL=%q status=%d", generated, response.StatusCode)
	}
	for _, pattern := range []string{"/items//x", "/items/..", "/items/.", "/x?query", "/x#fragment"} {
		if err := router.GET(pattern, func(*web.Context) error { return nil }); err == nil {
			t.Fatalf("accepted ambiguous pattern %q", pattern)
		}
	}
	if err := router.GET("/items/{id}/", func(*web.Context) error { return nil }); err == nil {
		t.Fatal("duplicate trailing-slash alias accepted")
	}
	if _, err := router.URL("item", map[string]string{"id": ".."}); err == nil {
		t.Fatal("generated unroutable dot parameter")
	}
}

func TestFileRangesAndConditionsOverNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	content := []byte("0123456789")
	must(t, os.WriteFile(path, content, 0600))
	modified := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	must(t, os.Chtimes(path, modified, modified))
	router := web.NewRouter()
	must(t, router.GET("/file", func(ctx *web.Context) error { ctx.Writer.Header().Set("ETag", `"v1"`); return ctx.File(200, path) }))
	must(t, router.GET("/download", func(ctx *web.Context) error { return ctx.Download(200, path, "отчёт.txt") }))
	server := startLimitedServer(t, web.ServerConfig{}, router)
	base, client := "http://"+server.Address(), networkClient(t)
	for _, tc := range []struct {
		name, method       string
		headers            http.Header
		status             int
		body, contentRange string
	}{
		{"full", "GET", nil, 200, "0123456789", ""},
		{"head", "HEAD", nil, 200, "", ""},
		{"head ignores range", "HEAD", http.Header{"Range": {"bytes=2-5"}}, 200, "", ""},
		{"range", "GET", http.Header{"Range": {"bytes=2-5"}}, 206, "2345", "bytes 2-5/10"},
		{"suffix", "GET", http.Header{"Range": {"bytes=-3"}}, 206, "789", "bytes 7-9/10"},
		{"unsatisfiable", "GET", http.Header{"Range": {"bytes=20-30"}}, 416, "", "bytes */10"},
		{"modified", "GET", http.Header{"If-Modified-Since": {modified.Format(http.TimeFormat)}}, 304, "", ""},
		{"etag", "GET", http.Header{"If-None-Match": {`"v1"`}}, 304, "", ""},
		{"weak etag", "HEAD", http.Header{"If-None-Match": {`W/"v1"`}}, 304, "", ""},
		{"failed match", "GET", http.Header{"If-Match": {`"other"`}}, 412, "", ""},
		{"failed unmodified", "GET", http.Header{"If-Unmodified-Since": {modified.Add(-time.Hour).Format(http.TimeFormat)}}, 412, "", ""},
		{"if-range matching", "GET", http.Header{"If-Range": {`"v1"`}, "Range": {"bytes=0-1"}}, 206, "01", "bytes 0-1/10"},
		{"if-range stale", "GET", http.Header{"If-Range": {`"old"`}, "Range": {"bytes=0-1"}}, 200, "0123456789", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, body := readNetworkResponse(t, client, tc.method, base+"/file", tc.headers)
			if response.StatusCode != tc.status || response.Header.Get("Content-Range") != tc.contentRange {
				t.Fatalf("status=%d headers=%v", response.StatusCode, response.Header)
			}
			if tc.status < 400 && string(body) != tc.body {
				t.Fatalf("body=%q want %q", body, tc.body)
			}
			if tc.status == 200 && response.ContentLength != int64(len(content)) {
				t.Fatal("full representation size was lost")
			}
			if tc.status == 304 && len(body) != 0 {
				t.Fatal("304 carried body")
			}
		})
	}
	response, body := readNetworkResponse(t, client, "GET", base+"/file", http.Header{"Range": {"bytes=0-1,8-9"}})
	media, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	must(t, err)
	if response.StatusCode != 206 || media != "multipart/byteranges" {
		t.Fatalf("multipart range status=%d type=%s", response.StatusCode, media)
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for _, want := range []string{"01", "89"} {
		part, err := reader.NextPart()
		must(t, err)
		data, err := io.ReadAll(part)
		must(t, err)
		must(t, part.Close())
		if string(data) != want {
			t.Fatalf("range part=%q", data)
		}
	}
	if _, err := reader.NextPart(); err != io.EOF {
		t.Fatalf("extra range part: %v", err)
	}
	response, body = readNetworkResponse(t, client, "GET", base+"/download", nil)
	_, params, err = mime.ParseMediaType(response.Header.Get("Content-Disposition"))
	must(t, err)
	if params["filename"] != "отчёт.txt" || string(body) != string(content) {
		t.Fatal("download changed file or filename")
	}
}

func TestHeadHelpersDoNotReadStreams(t *testing.T) {
	response := httptest.NewRecorder()
	ctx := &web.Context{Writer: response, Request: httptest.NewRequest("HEAD", "/", nil)}
	if err := ctx.Stream(200, "text/plain", errorReader{err: errors.New("must not read")}); err != nil {
		t.Fatal(err)
	}
	if response.Body.Len() != 0 {
		t.Fatal("HEAD stream had body")
	}
	response = httptest.NewRecorder()
	ctx = &web.Context{Writer: response, Request: httptest.NewRequest("HEAD", "/", nil)}
	must(t, ctx.JSON(200, map[string]int{"n": 1}))
	if response.Body.Len() != 0 || response.Header().Get("Content-Length") != strconv.Itoa(len("{\"n\":1}\n")) {
		t.Fatal("HEAD JSON representation headers/body mismatch")
	}
}
