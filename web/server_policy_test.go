package web

import (
	"net/http"
	"testing"
	"time"
)

func TestServerDefaultResourcePolicy(t *testing.T) {
	ordinary, err := NewServer(ServerConfig{}, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	config := ordinary.config
	if config.ReadHeaderTimeout != 5*time.Second || config.ReadTimeout != 15*time.Second ||
		config.WriteTimeout != 30*time.Second || config.IdleTimeout != 60*time.Second ||
		config.MaxHeaderBytes != 16<<10 || config.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected default policy: %+v", config)
	}
	streaming, err := NewServer(ServerConfig{Streaming: true}, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	if streaming.config.WriteTimeout != 0 || streaming.config.ReadTimeout == 0 || streaming.config.IdleTimeout == 0 {
		t.Fatalf("unexpected streaming policy: %+v", streaming.config)
	}
	bounded, err := NewServer(ServerConfig{Streaming: true, WriteTimeout: time.Second}, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	if bounded.config.WriteTimeout != time.Second {
		t.Fatal("explicit streaming timeout was lost")
	}
	if _, err := NewServer(ServerConfig{MaxHeaderBytes: -1}, http.NotFoundHandler()); err == nil {
		t.Fatal("accepted negative header limit")
	}
}
