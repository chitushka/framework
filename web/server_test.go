package web_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/component"
	"github.com/chitushka/framework/web"
)

func TestServerLifecycleServesHTTPAndRegistersComponent(t *testing.T) {
	listener := localListener(t)
	router := web.NewRouter()
	_ = router.GET("/health", func(context *web.Context) error {
		return context.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	server, err := web.NewServer(web.ServerConfig{}, router, web.WithListener(listener))
	if err != nil {
		t.Fatal(err)
	}
	application := app.New(nil, nil)
	if err := application.AddModule(server); err != nil {
		t.Fatal(err)
	}
	if err := application.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	response, err := http.Get("http://" + server.Address() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("неверный ответ: %d %s", response.StatusCode, body)
	}
	registered, err := component.GetAs[*web.Server](context.Background(), application.Components, "web.server")
	if err != nil || registered != server {
		t.Fatalf("сервер не зарегистрирован: %v", err)
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestServerGracefulShutdownWaitsForRequest(t *testing.T) {
	listener := localListener(t)
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseRequest
		writer.WriteHeader(http.StatusNoContent)
	})
	server, _ := web.NewServer(web.ServerConfig{ShutdownTimeout: time.Second}, handler, web.WithListener(listener))
	application := app.New(nil, nil)
	_ = application.AddModule(server)
	if err := application.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + server.Address())
		if err == nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	<-requestStarted
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- application.Shutdown(context.Background()) }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown завершился до запроса: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(releaseRequest)
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
}

func TestApplicationRunReturnsServerRuntimeError(t *testing.T) {
	want := errors.New("accept failed")
	listener := &failingListener{err: want}
	server, _ := web.NewServer(web.ServerConfig{}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), web.WithListener(listener))
	application := app.New(nil, nil)
	_ = application.AddModule(server)
	err := application.Run(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("получена ошибка %v", err)
	}
}

func TestServerValidatesConfigurationAndOptions(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if _, err := web.NewServer(web.ServerConfig{}, nil); err == nil {
		t.Fatal("ожидалась ошибка nil handler")
	}
	if _, err := web.NewServer(web.ServerConfig{IdleTimeout: -1}, handler); err == nil {
		t.Fatal("ожидалась ошибка timeout")
	}
	if _, err := web.NewServer(web.ServerConfig{}, handler, nil); err == nil {
		t.Fatal("ожидалась ошибка nil option")
	}
	if _, err := web.NewServer(web.ServerConfig{}, handler, web.WithListener(nil)); err == nil {
		t.Fatal("ожидалась ошибка nil listener")
	}
	if _, err := web.NewServer(web.ServerConfig{}, handler, web.WithServerID("")); err == nil {
		t.Fatal("ожидалась ошибка ID")
	}
}

func localListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

type failingListener struct {
	err  error
	once sync.Once
}

func (l *failingListener) Accept() (net.Conn, error) { return nil, l.err }
func (l *failingListener) Close() error {
	l.once.Do(func() {})
	return nil
}
func (l *failingListener) Addr() net.Addr { return stringAddress("failing") }

type stringAddress string

func (a stringAddress) Network() string { return "test" }
func (a stringAddress) String() string  { return string(a) }
