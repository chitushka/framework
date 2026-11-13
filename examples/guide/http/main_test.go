package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServiceReadinessAndJSON(t *testing.T) {
	application, server, err := service("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := application.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	client := &http.Client{Timeout: time.Second}
	for _, path := range []string{"/hello", "/livez", "/readyz"} {
		resp, err := client.Get("http://" + server.Address() + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		if path == "/hello" && !strings.Contains(string(body), `"message":"Hello, Chitu"`) {
			t.Fatalf("unexpected JSON: %s", body)
		}
	}
}
