package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chitushka/framework/app"
)

type Handler func(context.Context, json.RawMessage) error

// JSONHandler decodes a typed payload before invoking the application handler.
func JSONHandler[T any](handler func(context.Context, T) error) Handler {
	return func(ctx context.Context, payload json.RawMessage) error {
		var value T
		if err := json.Unmarshal(payload, &value); err != nil {
			return fmt.Errorf("queue: decode job payload: %w", err)
		}
		return handler(ctx, value)
	}
}

type Failure struct {
	Message Message
	Err     error
	Final   bool
}

type WorkerConfig struct {
	Concurrency     int
	PollInterval    time.Duration
	ShutdownTimeout time.Duration
	RetryDelay      func(attempt int) time.Duration
	OnFailure       func(Failure)
}

// Worker consumes jobs concurrently and can be registered as app.Module.
type Worker struct {
	broker   Broker
	config   WorkerConfig
	mu       sync.Mutex
	handlers map[string]Handler
	cancel   context.CancelFunc
	done     chan struct{}
	errors   chan error
	started  bool
}

func NewWorker(broker Broker, config WorkerConfig) (*Worker, error) {
	if broker == nil {
		return nil, errors.New("queue: broker is required")
	}
	if config.Concurrency == 0 {
		config.Concurrency = 1
	}
	if config.Concurrency < 1 {
		return nil, errors.New("queue: concurrency must be positive")
	}
	if config.PollInterval == 0 {
		config.PollInterval = 250 * time.Millisecond
	}
	if config.PollInterval < 0 {
		return nil, errors.New("queue: PollInterval cannot be negative")
	}
	if config.ShutdownTimeout == 0 {
		config.ShutdownTimeout = 10 * time.Second
	}
	if config.ShutdownTimeout < 0 {
		return nil, errors.New("queue: ShutdownTimeout cannot be negative")
	}
	if config.RetryDelay == nil {
		config.RetryDelay = func(attempt int) time.Duration { delay := time.Second << min(attempt-1, 6); return delay }
	}
	return &Worker{broker: broker, config: config, handlers: make(map[string]Handler), errors: make(chan error, 1)}, nil
}

func (worker *Worker) RegisterHandler(name string, handler Handler) error {
	if name == "" {
		return errors.New("queue: handler name is required")
	}
	if handler == nil {
		return errors.New("queue: handler is required")
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.started {
		return errors.New("queue: handlers cannot be changed after start")
	}
	if _, exists := worker.handlers[name]; exists {
		return fmt.Errorf("queue: handler %q already registered", name)
	}
	worker.handlers[name] = handler
	return nil
}

func (worker *Worker) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("queue: context is nil")
	}
	worker.mu.Lock()
	if worker.started {
		worker.mu.Unlock()
		return errors.New("queue: worker already started")
	}
	if len(worker.handlers) == 0 {
		worker.mu.Unlock()
		return errors.New("queue: no handlers registered")
	}
	runtime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	worker.cancel = cancel
	worker.done = make(chan struct{})
	worker.started = true
	handlers := make(map[string]Handler, len(worker.handlers))
	for name, handler := range worker.handlers {
		handlers[name] = handler
	}
	done := worker.done
	worker.mu.Unlock()
	go func() {
		var group sync.WaitGroup
		for range worker.config.Concurrency {
			group.Add(1)
			go func() { defer group.Done(); worker.consume(runtime, handlers) }()
		}
		group.Wait()
		close(done)
	}()
	return nil
}

func (worker *Worker) consume(ctx context.Context, handlers map[string]Handler) {
	for {
		delivery, err := worker.broker.Reserve(ctx, worker.config.PollInterval)
		if errors.Is(err, ErrEmpty) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			worker.failFatal(fmt.Errorf("queue: reserve: %w", err))
			return
		}
		message := delivery.Message()
		handler := handlers[message.Name]
		var handleErr error
		if handler == nil {
			handleErr = fmt.Errorf("queue: no handler for job %q", message.Name)
		} else {
			handleErr = handler(ctx, message.Payload)
		}
		settleCtx := context.WithoutCancel(ctx)
		if handleErr == nil {
			if err := delivery.Ack(settleCtx); err != nil {
				worker.failFatal(fmt.Errorf("queue: acknowledge %q: %w", message.ID, err))
				return
			}
			continue
		}
		final := message.Attempt+1 >= message.MaxAttempts
		if final {
			if err := delivery.Ack(settleCtx); err != nil {
				worker.failFatal(fmt.Errorf("queue: discard %q: %w", message.ID, err))
				return
			}
		} else {
			delay := worker.config.RetryDelay(message.Attempt + 1)
			if delay < 0 {
				delay = 0
			}
			if err := delivery.Nack(settleCtx, delay); err != nil {
				worker.failFatal(fmt.Errorf("queue: retry %q: %w", message.ID, err))
				return
			}
		}
		if worker.config.OnFailure != nil {
			worker.config.OnFailure(Failure{Message: message, Err: handleErr, Final: final})
		}
	}
}

func (worker *Worker) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("queue: context is nil")
	}
	worker.mu.Lock()
	if !worker.started {
		worker.mu.Unlock()
		return nil
	}
	cancel, done := worker.cancel, worker.done
	worker.started = false
	worker.mu.Unlock()
	cancel()
	shutdown := ctx
	stop := func() {}
	if worker.config.ShutdownTimeout > 0 {
		shutdown, stop = context.WithTimeout(ctx, worker.config.ShutdownTimeout)
	}
	defer stop()
	select {
	case <-done:
		return nil
	case <-shutdown.Done():
		return fmt.Errorf("queue: worker shutdown: %w", shutdown.Err())
	}
}
func (worker *Worker) failFatal(err error) {
	select {
	case worker.errors <- err:
	default:
	}
}
func (worker *Worker) Errors() <-chan error { return worker.errors }
func (worker *Worker) ID() string           { return "queue.worker" }
func (worker *Worker) Register(application *app.Application) error {
	return application.Components.Instance("queue.worker", worker)
}
func (worker *Worker) Boot(ctx context.Context, _ *app.Application) error { return worker.Start(ctx) }
