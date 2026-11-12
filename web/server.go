package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/chitushka/framework/app"
)

const (
	defaultServerAddress     = ":8080"
	defaultReadHeaderTimeout = 5 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
	defaultReadTimeout       = 15 * time.Second
	defaultWriteTimeout      = 30 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultMaxHeaderBytes    = 16 << 10
	serverComponentName      = "web.server"
)

// ServerConfig задаёт безопасные базовые параметры HTTP-сервера.
type ServerConfig struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
	RequestLimits     RequestLimits
	// Streaming leaves WriteTimeout disabled when zero. Handlers must bound writes
	// with http.ResponseController.SetWriteDeadline and respect request cancellation.
	Streaming bool
}

// ServerOption настраивает HTTP-сервер.
type ServerOption func(*Server) error

// WithListener передаёт заранее открытый listener.
// Опция удобна для socket activation и интеграционных тестов.
func WithListener(listener net.Listener) ServerOption {
	return func(server *Server) error {
		if listener == nil {
			return errors.New("web: listener не может быть nil")
		}
		server.listener = listener
		return nil
	}
}

// WithServerID меняет идентификатор модуля. По умолчанию используется "web".
func WithServerID(id string) ServerOption {
	return func(server *Server) error {
		if id == "" {
			return errors.New("web: ID сервера не может быть пустым")
		}
		server.id = id
		return nil
	}
}

// Server управляет жизненным циклом стандартного http.Server.
type Server struct {
	id      string
	config  ServerConfig
	handler http.Handler

	mu           sync.Mutex
	listener     net.Listener
	httpServer   *http.Server
	errors       chan error
	done         chan struct{}
	started      bool
	booted       bool
	stopping     bool
	shutdownDone chan struct{}
	shutdownErr  error
}

// NewServer создаёт HTTP-сервер, готовый к регистрации как app.Module.
func NewServer(config ServerConfig, handler http.Handler, options ...ServerOption) (*Server, error) {
	if handler == nil {
		return nil, errors.New("web: HTTP handler не может быть nil")
	}
	if config.Address == "" {
		config.Address = defaultServerAddress
	}
	if config.ReadHeaderTimeout == 0 {
		config.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if config.ShutdownTimeout == 0 {
		config.ShutdownTimeout = defaultShutdownTimeout
	}
	if config.ReadTimeout == 0 {
		config.ReadTimeout = defaultReadTimeout
	}
	if config.WriteTimeout == 0 && !config.Streaming {
		config.WriteTimeout = defaultWriteTimeout
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = defaultIdleTimeout
	}
	if config.MaxHeaderBytes == 0 {
		config.MaxHeaderBytes = defaultMaxHeaderBytes
	}
	if err := validateServerConfig(config); err != nil {
		return nil, err
	}
	limited, err := NewLimitedHandler(handler, config.RequestLimits)
	if err != nil {
		return nil, err
	}
	server := &Server{
		id:           "web",
		config:       config,
		handler:      limited,
		errors:       make(chan error, 1),
		done:         make(chan struct{}),
		shutdownDone: make(chan struct{}),
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("web: опция сервера не может быть nil")
		}
		if err := option(server); err != nil {
			return nil, err
		}
	}
	return server, nil
}

// ID возвращает идентификатор HTTP-модуля.
func (s *Server) ID() string { return s.id }

// Register добавляет сервер в реестр компонентов приложения.
func (s *Server) Register(application *app.Application) error {
	name := serverComponentName
	if s.id != "web" {
		name += "." + s.id
	}
	return application.Components.Instance(name, s)
}

// Boot открывает listener и запускает обслуживание запросов.
func (s *Server) Boot(ctx context.Context, _ *app.Application) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.booted {
		return errors.New("web: HTTP-сервер уже запускался")
	}
	listener := s.listener
	if listener == nil {
		var config net.ListenConfig
		opened, err := config.Listen(ctx, "tcp", s.config.Address)
		if err != nil {
			return fmt.Errorf("web: открыть listener %q: %w", s.config.Address, err)
		}
		listener = opened
		s.listener = opened
	}
	s.httpServer = &http.Server{
		Addr:              s.config.Address,
		Handler:           s.handler,
		ReadHeaderTimeout: s.config.ReadHeaderTimeout,
		ReadTimeout:       s.config.ReadTimeout,
		WriteTimeout:      s.config.WriteTimeout,
		IdleTimeout:       s.config.IdleTimeout,
		MaxHeaderBytes:    s.config.MaxHeaderBytes,
	}
	s.started = true
	s.booted = true
	go s.serve(listener, s.httpServer)
	return nil
}

// Shutdown останавливает приём соединений и ожидает активные запросы.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	server := s.httpServer
	done := s.done
	if !s.stopping {
		s.stopping = true
		go func() {
			err := s.shutdown(ctx, server, done)
			s.mu.Lock()
			s.shutdownErr = err
			close(s.shutdownDone)
			s.mu.Unlock()
		}()
	}
	shutdownDone := s.shutdownDone
	s.mu.Unlock()
	select {
	case <-shutdownDone:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) shutdown(ctx context.Context, server *http.Server, done <-chan struct{}) error {
	shutdownContext := ctx
	cancel := func() {}
	if s.config.ShutdownTimeout > 0 {
		shutdownContext, cancel = context.WithTimeout(ctx, s.config.ShutdownTimeout)
	}
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		closeErr := server.Close()
		return errors.Join(fmt.Errorf("web: graceful shutdown: %w", err), closeErr)
	}
	select {
	case <-done:
		return nil
	case <-shutdownContext.Done():
		return fmt.Errorf("web: ожидание остановки: %w", shutdownContext.Err())
	}
}

// Errors возвращает канал критических ошибок обслуживания.
func (s *Server) Errors() <-chan error { return s.errors }

// Address возвращает фактический адрес listener после Boot.
func (s *Server) Address() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) serve(listener net.Listener, server *http.Server) {
	defer close(s.done)
	err := server.Serve(listener)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return
	}
	select {
	case s.errors <- fmt.Errorf("web: обслуживание HTTP: %w", err):
	default:
	}
}

func validateServerConfig(config ServerConfig) error {
	if config.MaxHeaderBytes < 0 {
		return errors.New("web: MaxHeaderBytes cannot be negative")
	}
	values := map[string]time.Duration{
		"ReadHeaderTimeout": config.ReadHeaderTimeout,
		"ReadTimeout":       config.ReadTimeout,
		"WriteTimeout":      config.WriteTimeout,
		"IdleTimeout":       config.IdleTimeout,
		"ShutdownTimeout":   config.ShutdownTimeout,
	}
	for name, value := range values {
		if value < 0 {
			return fmt.Errorf("web: %s не может быть отрицательным", name)
		}
	}
	return nil
}
