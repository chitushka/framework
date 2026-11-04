package web_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chitushka/framework/web"
)

func TestRequestBodyAndMediaLimits(t *testing.T) {
	for _, tc := range []struct {
		name, body, media string
		unknownLength     bool
		status            int
	}{
		{"exact", `{"n":1}`, "application/json", false, 204},
		{"vendor", `{"n":1}`, "application/problem+json; charset=utf-8", false, 204},
		{"known oversized", `{"n":12}`, "application/json", false, 413},
		{"unknown oversized", `{"n":12}`, "application/json", true, 413},
		{"trailing oversized", `{"n":1} `, "application/json", true, 413},
		{"missing media", `{"n":1}`, "", false, 415},
		{"wrong media", `{"n":1}`, "text/plain", false, 415},
		{"malformed media", `{"n":1}`, "application/json;invalid", false, 415},
		{"invalid JSON", `{"n":`, "application/json", false, 400},
		{"unknown field", `{"z":1}`, "application/json", false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := web.NewRouter()
			must(t, router.SetRequestLimits(web.RequestLimits{MaxBodyBytes: 7}))
			called := false
			must(t, router.Use(func(next web.Handler) web.Handler {
				return func(ctx *web.Context) error { called = true; return next(ctx) }
			}))
			must(t, router.POST("/", func(ctx *web.Context) error {
				var value struct {
					N int `json:"n"`
				}
				if err := ctx.BindJSON(&value); err != nil {
					return err
				}
				return ctx.NoContent(204)
			}))
			request := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.media)
			if tc.unknownLength {
				request.ContentLength = -1
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.name == "known oversized" && called {
				t.Fatal("oversized Content-Length reached middleware")
			}
		})
	}
}

func TestFormLimitsBeforeMiddleware(t *testing.T) {
	for _, tc := range []struct {
		body    string
		unknown bool
		limits  web.RequestLimits
	}{
		{"x=12345", false, web.RequestLimits{MaxFormBytes: 6}},
		{"x=12345", true, web.RequestLimits{MaxFormBytes: 6}},
		{"a=1&a=2", true, web.RequestLimits{MaxFormValues: 1}},
	} {
		router := web.NewRouter()
		must(t, router.SetRequestLimits(tc.limits))
		must(t, router.Use(func(next web.Handler) web.Handler {
			return func(*web.Context) error { t.Error("form exceeded limits before middleware"); return nil }
		}))
		must(t, router.POST("/", func(ctx *web.Context) error { return ctx.NoContent(204) }))
		request := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if tc.unknown {
			request.ContentLength = -1
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != 413 {
			t.Fatalf("status=%d", response.Code)
		}
	}
}

func TestRawBodyLimitErrorUsesHTTP413(t *testing.T) {
	router := web.NewRouter()
	must(t, router.SetRequestLimits(web.RequestLimits{MaxBodyBytes: 4}))
	must(t, router.POST("/", func(ctx *web.Context) error {
		_, err := io.ReadAll(ctx.Request.Body)
		return err
	}))
	request := httptest.NewRequest("POST", "/", strings.NewReader("12345"))
	request.ContentLength = -1
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 413 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMultipartLimitsAndCleanup(t *testing.T) {
	directory := t.TempDir()
	for _, env := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(env, directory)
	}
	for _, scenario := range []string{"success", "handler error", "panic", "file size", "file count", "field size", "field count", "malformed", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			var payload bytes.Buffer
			writer := multipart.NewWriter(&payload)
			must(t, writer.WriteField("name", "test"))
			part, err := writer.CreateFormFile("file", "upload.txt")
			must(t, err)
			_, err = io.WriteString(part, strings.Repeat("a", 128))
			must(t, err)
			if scenario == "file count" {
				part, err = writer.CreateFormFile("file", "other.txt")
				must(t, err)
				_, err = io.WriteString(part, "second")
				must(t, err)
			}
			if scenario == "field count" {
				must(t, writer.WriteField("other", "value"))
			}
			must(t, writer.Close())
			limits := web.RequestLimits{MaxMultipartMemory: 1}
			want := 204
			switch scenario {
			case "file size":
				limits.MaxUploadBytes = 127
				want = 413
			case "file count":
				limits.MaxFiles = 1
				want = 413
			case "field size":
				limits.MaxFormBytes = 3
				want = 413
			case "field count":
				limits.MaxFormValues = 1
				want = 413
			case "handler error":
				want = 400
			case "malformed", "cancelled":
				want = 400
			}
			router := web.NewRouter()
			must(t, router.SetRequestLimits(limits))
			called := false
			must(t, router.POST("/", func(ctx *web.Context) error {
				called = true
				entries, err := os.ReadDir(directory)
				must(t, err)
				if len(entries) == 0 {
					t.Fatal("upload was not spooled to disk")
				}
				file, err := ctx.Request.MultipartForm.File["file"][0].Open()
				must(t, err)
				data, err := io.ReadAll(file)
				must(t, err)
				must(t, file.Close())
				if len(data) != 128 {
					t.Fatalf("upload size=%d", len(data))
				}
				if scenario == "panic" {
					panic("handler panic")
				}
				if scenario == "handler error" {
					return web.NewHTTPError(400, "invalid upload", nil)
				}
				return ctx.NoContent(204)
			}))
			body := payload.Bytes()
			if scenario == "malformed" || scenario == "cancelled" {
				body = body[:len(body)-20]
			}
			request := httptest.NewRequest("POST", "/", bytes.NewReader(body))
			request.Header.Set("Content-Type", writer.FormDataContentType())
			if scenario == "cancelled" {
				request.Body = &cancelledBody{Reader: bytes.NewReader(body)}
				request.ContentLength = -1
			}
			response := httptest.NewRecorder()
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				router.ServeHTTP(response, request)
			}()
			if (scenario == "panic") != panicked {
				t.Fatalf("panic=%v", panicked)
			}
			if scenario != "panic" && response.Code != want {
				t.Fatalf("status=%d want %d: %s", response.Code, want, response.Body.String())
			}
			if want == 413 && called {
				t.Fatal("invalid upload reached handler")
			}
			entries, err := os.ReadDir(directory)
			must(t, err)
			if len(entries) != 0 {
				t.Fatalf("temporary uploads leaked: %v", entries)
			}
		})
	}
}

type cancelledBody struct{ *bytes.Reader }

func (body *cancelledBody) Read(p []byte) (int, error) {
	n, err := body.Reader.Read(p)
	if err == io.EOF {
		err = context.Canceled
	}
	return n, err
}
func (*cancelledBody) Close() error { return nil }

func TestLimitsValidationAndPlainHTTPHandler(t *testing.T) {
	for _, limits := range []web.RequestLimits{
		{MaxBodyBytes: -1}, {MaxFormBytes: -1}, {MaxMultipartMemory: -1},
		{MaxUploadBytes: -1}, {MaxFormValues: -1}, {MaxFiles: -1},
	} {
		if err := web.NewRouter().SetRequestLimits(limits); err == nil {
			t.Fatal("accepted negative limit")
		}
		if _, err := web.NewServer(web.ServerConfig{RequestLimits: limits}, http.NotFoundHandler()); err == nil {
			t.Fatal("server accepted negative limit")
		}
	}
	if _, err := web.NewLimitedHandler(nil, web.RequestLimits{}); err == nil {
		t.Fatal("accepted nil handler")
	}
	handler, err := web.NewLimitedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Form.Get("x") != "1" {
			t.Error("form was not parsed")
		}
		w.WriteHeader(204)
	}), web.RequestLimits{})
	must(t, err)
	request := httptest.NewRequest("POST", "/", strings.NewReader("x=1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 204 {
		t.Fatalf("status=%d", response.Code)
	}
}

func startLimitedServer(t *testing.T, config web.ServerConfig, handler http.Handler) *web.Server {
	t.Helper()
	server, err := web.NewServer(config, handler, web.WithListener(localListener(t)))
	must(t, err)
	must(t, server.Boot(t.Context(), nil))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		must(t, server.Shutdown(ctx))
	})
	return server
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func dialServer(t *testing.T, server *web.Server) net.Conn {
	t.Helper()
	connection, err := net.DialTimeout("tcp", server.Address(), time.Second)
	must(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	must(t, connection.SetDeadline(time.Now().Add(3*time.Second)))
	return connection
}

func TestServerNetworkResourceLimits(t *testing.T) {
	var called atomic.Int32
	router := web.NewRouter()
	must(t, router.POST("/", func(ctx *web.Context) error {
		called.Add(1)
		var value any
		if err := ctx.BindJSON(&value); err != nil {
			return err
		}
		return ctx.NoContent(204)
	}))
	server := startLimitedServer(t, web.ServerConfig{
		ReadHeaderTimeout: 100 * time.Millisecond, ReadTimeout: 250 * time.Millisecond,
		WriteTimeout: time.Second, MaxHeaderBytes: 1024, RequestLimits: web.RequestLimits{MaxBodyBytes: 7},
	}, router)
	for _, tc := range []struct {
		name, wire string
		status     int
	}{
		{"chunked oversized", "POST / HTTP/1.1\r\nHost: test\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n8\r\n{\"n\":12}\r\n0\r\n\r\n", 413},
		{"large headers", "GET / HTTP/1.1\r\nHost: test\r\nX-Large: " + strings.Repeat("x", 8192) + "\r\n\r\n", 431},
		{"slow body", "POST / HTTP/1.1\r\nHost: test\r\nContent-Type: application/json\r\nContent-Length: 7\r\n\r\n{", 408},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connection := dialServer(t, server)
			_, err := io.WriteString(connection, tc.wire)
			must(t, err)
			response, err := http.ReadResponse(bufio.NewReader(connection), nil)
			must(t, err)
			must(t, response.Body.Close())
			if response.StatusCode != tc.status {
				t.Fatalf("status=%d want %d", response.StatusCode, tc.status)
			}
		})
	}
	before := called.Load()
	connection := dialServer(t, server)
	_, err := io.WriteString(connection, "POST / HTTP/1.1\r\nHost: test\r\nX-Slow:")
	must(t, err)
	_, err = bufio.NewReader(connection).ReadByte()
	if err == nil {
		t.Fatal("slow partial headers were accepted")
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("client deadline expired instead of server closing connection: %v", err)
	}
	if called.Load() != before {
		t.Fatal("partial headers reached handler")
	}
}

func TestStreamingDisconnectAndWriteDeadline(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	server := startLimitedServer(t, web.ServerConfig{Streaming: true}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if err := controller.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
			return
		}
		_, _ = fmt.Fprint(w, "ready\n")
		if err := controller.Flush(); err != nil {
			return
		}
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	connection := dialServer(t, server)
	_, err := io.WriteString(connection, "GET / HTTP/1.1\r\nHost: test\r\n\r\n")
	must(t, err)
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	must(t, err)
	data := make([]byte, len("ready\n"))
	_, err = io.ReadFull(response.Body, data)
	must(t, err)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not start")
	}
	must(t, connection.Close())
	_ = response.Body.Close() // The transport was deliberately closed first.
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect did not cancel handler")
	}
}

func TestServerWriteAndIdleBudgets(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			writeError := make(chan error, 1)
			config := web.ServerConfig{WriteTimeout: 50 * time.Millisecond}
			if streaming {
				config = web.ServerConfig{Streaming: true}
			}
			server := startLimitedServer(t, config, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				controller := http.NewResponseController(w)
				if streaming {
					if err := controller.SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
						writeError <- err
						return
					}
				}
				timer := time.NewTimer(150 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-r.Context().Done():
					writeError <- r.Context().Err()
					return
				}
				_, _ = io.WriteString(w, "late response")
				writeError <- controller.Flush()
			}))
			connection := dialServer(t, server)
			_, err := io.WriteString(connection, "GET / HTTP/1.1\r\nHost: test\r\n\r\n")
			must(t, err)
			select {
			case err := <-writeError:
				var timeout net.Error
				if !errors.As(err, &timeout) || !timeout.Timeout() {
					t.Fatalf("write error=%v, want timeout", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("write did not finish within budget")
			}
		})
	}
	t.Run("idle", func(t *testing.T) {
		server := startLimitedServer(t, web.ServerConfig{IdleTimeout: 75 * time.Millisecond}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
		connection := dialServer(t, server)
		_, err := io.WriteString(connection, "GET / HTTP/1.1\r\nHost: test\r\n\r\n")
		must(t, err)
		reader := bufio.NewReader(connection)
		response, err := http.ReadResponse(reader, nil)
		must(t, err)
		must(t, response.Body.Close())
		_, err = reader.ReadByte()
		if !errors.Is(err, io.EOF) {
			t.Fatalf("idle connection was not closed: %v", err)
		}
	})
}

func TestDisconnectedMultipartRemovesTemporaryFiles(t *testing.T) {
	directory := t.TempDir()
	for _, env := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(env, directory)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "partial.txt")
	must(t, err)
	_, err = io.WriteString(part, strings.Repeat("x", 4096))
	must(t, err)
	// Do not send the terminating multipart boundary.
	server := startLimitedServer(t, web.ServerConfig{RequestLimits: web.RequestLimits{MaxMultipartMemory: 1}}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("partial upload reached handler") }))
	connection := dialServer(t, server)
	_, err = fmt.Fprintf(connection, "POST / HTTP/1.1\r\nHost: test\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n", writer.FormDataContentType(), body.Len()+100)
	must(t, err)
	_, err = connection.Write(body.Bytes())
	must(t, err)
	waitForFiles := func(present bool) {
		t.Helper()
		deadline := time.NewTimer(2 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			entries, err := os.ReadDir(directory)
			must(t, err)
			if (len(entries) > 0) == present {
				return
			}
			select {
			case <-deadline.C:
				t.Fatalf("temporary files present=%v, want %v", len(entries) > 0, present)
			case <-ticker.C:
			}
		}
	}
	waitForFiles(true)
	must(t, connection.Close())
	waitForFiles(false)
}
