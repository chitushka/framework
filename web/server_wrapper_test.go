package web_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chitushka/framework/web"
)

func TestServerWrapperIncludesResourceErrors(t *testing.T) {
	var reached atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached.Add(1); w.WriteHeader(204) })
	for _, wrap := range []func(http.Handler) http.Handler{nil, func(http.Handler) http.Handler { return nil }} {
		if _, err := web.NewServer(web.ServerConfig{}, handler, web.WithServerHandlerWrapper(wrap)); err == nil {
			t.Fatal("invalid wrapper accepted")
		}
	}
	wrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Outer-Contract", "yes")
			next.ServeHTTP(w, r)
		})
	}
	server, err := web.NewServer(web.ServerConfig{Address: "127.0.0.1:0", RequestLimits: web.RequestLimits{MaxBodyBytes: 8}}, handler, web.WithServerHandlerWrapper(wrap))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Boot(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	client := &http.Client{Timeout: time.Second}
	for _, tc := range []struct {
		body   string
		status int
	}{{"ok", 204}, {strings.Repeat("x", 9), 413}} {
		r, err := http.NewRequest("POST", "http://"+server.Address(), strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.status || resp.Header.Get("X-Outer-Contract") != "yes" {
			t.Fatalf("outer wrapper missed response: %d", resp.StatusCode)
		}
	}
	if reached.Load() != 1 {
		t.Fatal("oversized request reached application")
	}
}
