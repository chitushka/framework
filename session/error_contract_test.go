package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chitushka/framework/web"
)

func TestCommitFailureBlocksWriterOperations(t *testing.T) {
	failure := errors.New("backend unavailable")
	for _, operation := range []string{"write", "header", "flush", "hijack"} {
		t.Run(operation, func(t *testing.T) {
			response := httptest.NewRecorder()
			calls := 0
			writer := &commitWriter{ResponseWriter: response, commit: func() error { calls++; return failure }}
			switch operation {
			case "write":
				if n, err := writer.Write([]byte("private response")); n != 0 || !errors.Is(err, failure) {
					t.Fatalf("write bypassed commit failure: %d %v", n, err)
				}
			case "header":
				writer.WriteHeader(http.StatusCreated)
			case "flush":
				if err := writer.FlushError(); !errors.Is(err, failure) {
					t.Fatal("flush bypassed commit failure", err)
				}
			case "hijack":
				conn, rw, err := writer.Hijack()
				if conn != nil || rw != nil || !errors.Is(err, failure) {
					t.Fatal("hijack bypassed commit failure", err)
				}
			}
			if err := writer.ensureCommitted(); !errors.Is(err, failure) || calls != 1 || response.Code != 200 || response.Body.Len() != 0 || response.Flushed {
				t.Fatalf("failed commit retried or exposed output: calls=%d response=%+v error=%v", calls, response, err)
			}
		})
	}
}

func TestMiddlewarePreservesHandlerAndCommitErrors(t *testing.T) {
	manager, err := New(NewMemoryBackend(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	cookies, err := NewCookieStore(bytes.Repeat([]byte{1}, 32), Config{})
	if err != nil {
		t.Fatal(err)
	}
	for name, middleware := range map[string]web.Middleware{"server": manager.Middleware, "cookie": cookies.Middleware} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			ctx := &web.Context{Writer: response, Request: httptest.NewRequest(http.MethodGet, "/", nil)}
			handlerFailure := errors.New("handler failed")
			err := middleware(func(ctx *web.Context) error {
				current := Must(ctx.Request.Context())
				// Simulate corrupt server-side state: no response may be committed.
				current.values["broken"] = json.RawMessage(`{`)
				current.dirty = true
				return handlerFailure
			})(ctx)
			if !errors.Is(err, handlerFailure) || !strings.Contains(err.Error(), "encode") || ctx.Writer != response || response.Body.Len() != 0 || response.Header().Get("Set-Cookie") != "" {
				t.Fatalf("middleware lost an error, writer or session boundary: %v", err)
			}
		})
	}
}

func TestCancelledMemoryOperationsPreserveSession(t *testing.T) {
	backend := NewMemoryBackend()
	if err := backend.Save(t.Context(), "existing", []byte("original"), time.Hour); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := backend.Load(ctx, "existing"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := backend.Save(ctx, "existing", []byte("changed"), time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := backend.Delete(ctx, "existing"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if swapped, err := backend.CompareAndSwap(ctx, "existing", []byte("original"), "next", []byte("changed"), time.Hour); swapped || !errors.Is(err, context.Canceled) {
		t.Fatal(swapped, err)
	}
	value, found, err := backend.Load(t.Context(), "existing")
	if err != nil || !found || string(value) != "original" {
		t.Fatal("cancelled operations changed stored session", found, err)
	}
}
