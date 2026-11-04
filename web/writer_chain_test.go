package web_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	frameworkdebug "github.com/chitushka/framework/debug"
	"github.com/chitushka/framework/observability"
	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

type logBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}
func (b *logBuffer) text() string { b.Lock(); defer b.Unlock(); return b.String() }

func writerChain(t *testing.T, cookie, debugFirst bool) (*web.Router, http.Handler, *logBuffer) {
	t.Helper()
	router := web.NewRouter()
	toolbar, err := frameworkdebug.New(frameworkdebug.Config{Enabled: true, MaxResponseBytes: 256})
	must(t, err)
	manager, err := session.New(session.NewMemoryBackend(), session.Config{})
	must(t, err)
	var sessions web.Middleware = manager.Middleware
	if cookie {
		store, err := session.NewCookieStore([]byte("01234567890123456789012345678901"), session.Config{})
		must(t, err)
		sessions = store.Middleware
	}
	if debugFirst {
		must(t, router.Use(toolbar.Middleware, sessions))
	} else {
		must(t, router.Use(sessions, toolbar.Middleware))
	}
	must(t, router.GET("/session", func(ctx *web.Context) error {
		current, _ := session.FromContext(ctx.Request.Context())
		var value string
		_, err := current.Get("value", &value)
		if err != nil {
			return err
		}
		return ctx.JSON(200, map[string]string{"value": value})
	}))
	logs := &logBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	return router, observability.HTTP(observability.HTTPConfig{Logger: logger})(router), logs
}

func saveSession(ctx *web.Context) error {
	current, _ := session.FromContext(ctx.Request.Context())
	return current.Set("value", "saved")
}

func TestStreamingSessionDebugLoggingChainOverNetwork(t *testing.T) {
	for _, cookie := range []bool{false, true} {
		for _, debugFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("cookie=%v/debugFirst=%v", cookie, debugFirst), func(t *testing.T) {
				router, handler, logs := writerChain(t, cookie, debugFirst)
				logged := make(chan struct{})
				observed := handler
				handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					observed.ServeHTTP(writer, request)
					if request.URL.Path == "/stream" {
						close(logged)
					}
				})
				release := make(chan struct{})
				var releaseOnce sync.Once
				t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
				done := make(chan error, 1)
				must(t, router.GET("/stream", func(ctx *web.Context) error {
					if err := saveSession(ctx); err != nil {
						return err
					}
					if _, ok := ctx.Writer.(http.Flusher); !ok {
						return errors.New("Flusher was lost")
					}
					controller := http.NewResponseController(ctx.Writer)
					if err := controller.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
						return err
					}
					if err := controller.SetReadDeadline(time.Time{}); err != nil {
						return err
					}
					if err := controller.EnableFullDuplex(); err != nil {
						return err
					}
					reader, writer := io.Pipe()
					defer func() { _ = reader.Close() }()
					go func() {
						defer func() { _ = writer.Close() }()
						if _, err := io.WriteString(writer, "chunk\n"); err != nil {
							return
						}
						select {
						case <-release:
							_, _ = io.WriteString(writer, "tail\n")
						case <-ctx.Request.Context().Done():
							_ = writer.CloseWithError(ctx.Request.Context().Err())
						}
					}()
					err := ctx.Stream(200, "text/html", reader)
					done <- err
					return err
				}))
				server := startLimitedServer(t, web.ServerConfig{Streaming: true}, handler)
				connection := dialServer(t, server)
				_, err := io.WriteString(connection, "GET /stream HTTP/1.1\r\nHost: test\r\n\r\n")
				must(t, err)
				response, err := http.ReadResponse(bufio.NewReader(connection), nil)
				must(t, err)
				first := make([]byte, 6)
				_, err = io.ReadFull(response.Body, first)
				must(t, err)
				if string(first) != "chunk\n" || len(response.Cookies()) != 1 {
					t.Fatalf("stream/cookie unavailable before completion: %q %v", first, response.Cookies())
				}
				client := networkClient(t)
				request, err := http.NewRequestWithContext(t.Context(), "GET", "http://"+server.Address()+"/session", nil)
				must(t, err)
				request.AddCookie(response.Cookies()[0])
				stored, err := client.Do(request)
				must(t, err)
				data, err := io.ReadAll(stored.Body)
				must(t, err)
				must(t, stored.Body.Close())
				if !strings.Contains(string(data), `"value":"saved"`) {
					t.Fatalf("session was not committed at flush: %s", data)
				}
				releaseOnce.Do(func() { close(release) })
				tail, err := io.ReadAll(response.Body)
				must(t, err)
				must(t, response.Body.Close())
				if string(tail) != "tail\n" {
					t.Fatalf("buffering/toolbar changed stream: %q", tail)
				}
				select {
				case err := <-done:
					must(t, err)
				case <-time.After(2 * time.Second):
					t.Fatal("handler did not finish")
				}
				select {
				case <-logged:
				case <-time.After(2 * time.Second):
					t.Fatal("stream logging did not finish")
				}
				found := false
				for _, line := range strings.Split(logs.text(), "\n") {
					if line == "" {
						continue
					}
					var entry struct {
						Path          string `json:"path"`
						Status, Bytes int
					}
					must(t, json.Unmarshal([]byte(line), &entry))
					if entry.Path == "/stream" {
						found = true
						if entry.Status != 200 || entry.Bytes != 11 {
							t.Fatalf("incorrect stream log: %s", line)
						}
					}
				}
				if !found {
					t.Fatal("stream log missing")
				}
			})
		}
	}
}

func TestDisconnectThroughWriterChain(t *testing.T) {
	router, handler, _ := writerChain(t, false, false)
	done := make(chan struct{})
	must(t, router.GET("/disconnect", func(ctx *web.Context) error {
		defer close(done)
		reader, writer := io.Pipe()
		defer func() { _ = reader.Close() }()
		go func() {
			_, _ = io.WriteString(writer, "ready\n")
			<-ctx.Request.Context().Done()
			_ = writer.CloseWithError(ctx.Request.Context().Err())
		}()
		return ctx.Stream(200, "text/html", reader)
	}))
	server := startLimitedServer(t, web.ServerConfig{Streaming: true}, handler)
	connection := dialServer(t, server)
	_, err := io.WriteString(connection, "GET /disconnect HTTP/1.1\r\nHost: test\r\n\r\n")
	must(t, err)
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	must(t, err)
	data := make([]byte, 6)
	_, err = io.ReadFull(response.Body, data)
	must(t, err)
	must(t, connection.Close())
	_ = response.Body.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect did not cancel full chain")
	}
}

func TestHijackThroughWriterChain(t *testing.T) {
	for _, debugFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(debugFirst), func(t *testing.T) {
			router, handler, _ := writerChain(t, false, debugFirst)
			done := make(chan error, 1)
			must(t, router.GET("/upgrade", func(ctx *web.Context) error {
				if err := saveSession(ctx); err != nil {
					return err
				}
				hijacker, ok := ctx.Writer.(http.Hijacker)
				if !ok {
					return errors.New("Hijacker was lost")
				}
				connection, rw, err := hijacker.Hijack()
				if err != nil {
					done <- err
					return err
				}
				defer func() { _ = connection.Close() }()
				if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
					done <- err
					return err
				}
				_, err = io.WriteString(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: test\r\n")
				if err == nil {
					err = ctx.Writer.Header().Write(rw)
				}
				if err == nil {
					_, err = io.WriteString(rw, "\r\nready\n")
				}
				if err == nil {
					err = rw.Flush()
				}
				if err == nil {
					var line string
					line, err = rw.ReadString('\n')
					if err == nil && line != "ping\n" {
						err = fmt.Errorf("unexpected input %q", line)
					}
				}
				if err == nil {
					_, err = io.WriteString(rw, "pong\n")
				}
				if err == nil {
					err = rw.Flush()
				}
				done <- err
				return err
			}))
			server := startLimitedServer(t, web.ServerConfig{Streaming: true}, handler)
			connection := dialServer(t, server)
			_, err := io.WriteString(connection, "GET /upgrade HTTP/1.1\r\nHost: test\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n")
			must(t, err)
			reader := bufio.NewReader(connection)
			response, err := http.ReadResponse(reader, nil)
			must(t, err)
			if response.StatusCode != 101 || len(response.Cookies()) != 1 {
				t.Fatalf("upgrade/session failed: %d %v", response.StatusCode, response.Header)
			}
			line, err := reader.ReadString('\n')
			must(t, err)
			if line != "ready\n" {
				t.Fatalf("upgrade corrupted: %q", line)
			}
			_, err = io.WriteString(connection, "ping\n")
			must(t, err)
			line, err = reader.ReadString('\n')
			must(t, err)
			if line != "pong\n" {
				t.Fatalf("upgrade reply=%q", line)
			}
			select {
			case err := <-done:
				must(t, err)
			case <-time.After(2 * time.Second):
				t.Fatal("hijack did not finish")
			}
		})
	}
}

func TestHTTP2CapabilitiesThroughWriterChain(t *testing.T) {
	router, handler, _ := writerChain(t, true, true)
	must(t, router.GET("/capabilities", func(ctx *web.Context) error {
		_, flusher := ctx.Writer.(http.Flusher)
		_, hijacker := ctx.Writer.(http.Hijacker)
		if !flusher || hijacker {
			return errors.New("HTTP/2 advertised incorrect capabilities")
		}
		if _, _, err := http.NewResponseController(ctx.Writer).Hijack(); !errors.Is(err, http.ErrNotSupported) {
			return fmt.Errorf("unexpected HTTP/2 hijack error: %v", err)
		}
		if err := http.NewResponseController(ctx.Writer).SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
			return err
		}
		return ctx.JSON(200, map[string]bool{"flusher": flusher, "hijacker": hijacker})
	}))
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := server.Client()
	client.Transport.(*http.Transport).ForceAttemptHTTP2 = true
	client.Timeout = 3 * time.Second
	response, body := readNetworkResponse(t, client, "GET", server.URL+"/capabilities", nil)
	if response.ProtoMajor != 2 || response.StatusCode != 200 || !strings.Contains(string(body), `"hijacker":false`) {
		t.Fatalf("HTTP/2 response: %s %d %s", response.Proto, response.StatusCode, body)
	}
}

func TestInformationalAndBufferedResponsesThroughWriterChain(t *testing.T) {
	for _, debugFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(debugFirst), func(t *testing.T) {
			router, handler, logs := writerChain(t, false, debugFirst)
			must(t, router.GET("/early", func(ctx *web.Context) error {
				if err := saveSession(ctx); err != nil {
					return err
				}
				ctx.Writer.Header().Set("Link", "</app.css>; rel=preload")
				if err := ctx.NoContent(103); err != nil {
					return err
				}
				ctx.Writer.Header().Del("Link")
				return ctx.HTML(200, "<body>small</body>")
			}))
			large := strings.Repeat("x", 1024)
			must(t, router.GET("/large", func(ctx *web.Context) error { return ctx.HTML(200, large) }))
			file := filepath.Join(t.TempDir(), "asset.html")
			must(t, os.WriteFile(file, []byte("<body>file</body>"), 0600))
			must(t, router.GET("/file", func(ctx *web.Context) error { ctx.Writer.Header().Set("ETag", `"file-v1"`); return ctx.File(200, file) }))
			must(t, router.GET("/plainfile", func(ctx *web.Context) error { return ctx.File(200, file) }))
			server := startLimitedServer(t, web.ServerConfig{}, handler)
			client := networkClient(t)
			codes := []int{}
			trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
				codes = append(codes, code)
				if len(header.Values("Set-Cookie")) != 0 {
					return errors.New("session committed at informational response")
				}
				return nil
			}}
			request, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), trace), "GET", "http://"+server.Address()+"/early", nil)
			must(t, err)
			response, err := client.Do(request)
			must(t, err)
			body, err := io.ReadAll(response.Body)
			must(t, err)
			must(t, response.Body.Close())
			if len(codes) != 1 || codes[0] != 103 || response.StatusCode != 200 || len(response.Cookies()) != 1 || !strings.Contains(string(body), "chitu-debug-toolbar") {
				t.Fatalf("informational/final response failed: %v %d %s", codes, response.StatusCode, body)
			}
			if strings.Contains(logs.text(), `"status":103`) {
				t.Fatal("logging retained informational status")
			}
			_, body = readNetworkResponse(t, client, "GET", "http://"+server.Address()+"/large", nil)
			if string(body) != large {
				t.Fatal("large response was buffered/injected")
			}
			response, body = readNetworkResponse(t, client, "GET", "http://"+server.Address()+"/file", http.Header{"Range": {"bytes=0-1"}})
			if response.StatusCode != 206 || string(body) != "<b" {
				t.Fatalf("debug corrupted range: %d %s", response.StatusCode, body)
			}
			response, body = readNetworkResponse(t, client, "GET", "http://"+server.Address()+"/file", http.Header{"If-None-Match": {`"file-v1"`}})
			if response.StatusCode != 304 || len(body) != 0 {
				t.Fatal("debug corrupted conditional response")
			}
			_, body = readNetworkResponse(t, client, "GET", "http://"+server.Address()+"/plainfile", nil)
			if string(body) != "<body>file</body>" {
				t.Fatal("debug changed file representation without ETag")
			}
		})
	}
}

type failingSessionBackend struct{}

var errSessionSave = errors.New("private backend failure")

func (failingSessionBackend) Load(context.Context, string) ([]byte, bool, error) {
	return nil, false, nil
}
func (failingSessionBackend) Save(context.Context, string, []byte, time.Duration) error {
	return errSessionSave
}
func (failingSessionBackend) Delete(context.Context, string) error { return nil }

func TestSessionCommitFailuresDoNotBecomeSuccessfulResponses(t *testing.T) {
	for _, cookie := range []bool{false, true} {
		for _, debugFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("cookie=%v/debugFirst=%v", cookie, debugFirst), func(t *testing.T) {
				router := web.NewRouter()
				toolbar, err := frameworkdebug.New(frameworkdebug.Config{Enabled: true})
				must(t, err)
				manager, err := session.New(failingSessionBackend{}, session.Config{})
				must(t, err)
				var sessions web.Middleware = manager.Middleware
				if cookie {
					store, err := session.NewCookieStore([]byte("01234567890123456789012345678901"), session.Config{})
					must(t, err)
					sessions = store.Middleware
				}
				if debugFirst {
					must(t, router.Use(toolbar.Middleware, sessions))
				} else {
					must(t, router.Use(sessions, toolbar.Middleware))
				}
				file := filepath.Join(t.TempDir(), "private.txt")
				must(t, os.WriteFile(file, []byte("private file"), 0600))
				must(t, router.GET("/failure", func(ctx *web.Context) error {
					current, _ := session.FromContext(ctx.Request.Context())
					if err := current.Set("large", strings.Repeat("x", 4096)); err != nil {
						return err
					}
					switch ctx.Request.URL.Query().Get("mode") {
					case "flush":
						return http.NewResponseController(ctx.Writer).Flush()
					case "header":
						return ctx.NoContent(204)
					case "file":
						ctx.Writer.Header().Set("ETag", `"private-file"`)
						return ctx.File(200, file)
					default:
						return ctx.HTML(200, "<body>never sent</body>")
					}
				}))
				server := startLimitedServer(t, web.ServerConfig{}, observability.HTTP(observability.HTTPConfig{})(router))
				for _, mode := range []string{"flush", "header", "body", "file"} {
					response, body := readNetworkResponse(t, networkClient(t), "GET", "http://"+server.Address()+"/failure?mode="+mode, nil)
					if response.StatusCode != 500 || strings.Contains(string(body), "private backend") || strings.Contains(string(body), "never sent") {
						t.Fatalf("commit error masked: mode=%s status=%d body=%s", mode, response.StatusCode, body)
					}
					if response.Header.Get("ETag") != "" {
						t.Fatal("stale file validator leaked into error response")
					}
				}
				response, body := readNetworkResponse(t, networkClient(t), "HEAD", "http://"+server.Address()+"/failure?mode=file", nil)
				if response.StatusCode != 500 || len(body) != 0 || response.Header.Get("ETag") != "" {
					t.Fatal("HEAD error inherited file representation")
				}
			})
		}
	}
}

func TestPanicRestoresWriterBeforeErrorResponse(t *testing.T) {
	router := web.NewRouter()
	toolbar, err := frameworkdebug.New(frameworkdebug.Config{Enabled: true})
	must(t, err)
	must(t, router.Use(web.Recover, toolbar.Middleware))
	must(t, router.GET("/panic", func(ctx *web.Context) error {
		if err := ctx.HTML(200, "buffered private data"); err != nil {
			return err
		}
		panic("private panic")
	}))
	server := startLimitedServer(t, web.ServerConfig{}, router)
	response, body := readNetworkResponse(t, networkClient(t), "GET", "http://"+server.Address()+"/panic", nil)
	if response.StatusCode != 500 || strings.Contains(string(body), "private") {
		t.Fatalf("panic leaked/buffered response: %d %s", response.StatusCode, body)
	}
}
