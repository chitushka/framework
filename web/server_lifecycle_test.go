package web_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/web"
)

type shutdownListener struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func (l *shutdownListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

func TestServerRepeatedShutdownWaitsForActiveRequest(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	listener := &shutdownListener{Listener: localListener(t), closed: make(chan struct{})}
	s, err := web.NewServer(web.ServerConfig{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { close(entered); <-release; w.WriteHeader(204) }), web.WithListener(listener))
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(nil, nil)
	if err := s.Boot(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan error, 1)
	go func() {
		r, err := http.Get("http://" + s.Address())
		if err == nil {
			err = r.Body.Close()
		}
		requestDone <- err
	}()
	<-entered
	first := make(chan error, 1)
	go func() { first <- s.Shutdown(t.Context()) }()
	<-listener.closed
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := s.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("repeat didn't wait: %v", err)
	}
	close(release)
	err = <-first
	if err != nil {
		t.Fatal(err)
	}
	<-requestDone
}

func TestServerShutdownDeadlineClosesRequestAndCancelsContext(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan struct{})
	s, err := web.NewServer(web.ServerConfig{ShutdownTimeout: 20 * time.Millisecond}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(cancelled) }), web.WithListener(localListener(t)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Boot(t.Context(), app.New(nil, nil)); err != nil {
		t.Fatal(err)
	}
	go func() {
		r, err := http.Get("http://" + s.Address())
		if err == nil {
			_ = r.Body.Close()
		}
	}()
	<-entered
	if err := s.Shutdown(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("missing deadline: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("request was not cancelled")
	}
	if err := s.Shutdown(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lost shared error")
	}
}

func TestSeparateManagementAndTrafficServers(t *testing.T) {
	a := app.New(nil, nil)
	for _, id := range []string{"web", "management"} {
		s, err := web.NewServer(web.ServerConfig{Address: "127.0.0.1:0"}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), web.WithServerID(id))
		if err != nil {
			t.Fatal(err)
		}
		if err := a.AddModule(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}
