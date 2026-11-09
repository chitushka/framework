package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/chitushka/framework/app"
)

type Handler func(context.Context, json.RawMessage) error

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
type PanicError struct{ Value any }

func (e *PanicError) Error() string { return fmt.Sprintf("queue: handler/callback panic: %v", e.Value) }
func (e *PanicError) Unwrap() error { cause, _ := e.Value.(error); return cause }

type WorkerConfig struct {
	Concurrency     int
	PollInterval    time.Duration
	ShutdownTimeout time.Duration
	// DrainTimeout allows running handlers to finish before cancelling them.
	// Zero defaults to min(one second, half the shutdown budget).
	DrainTimeout      time.Duration
	JobTimeout        time.Duration
	SettlementTimeout time.Duration
	RetryDelay        func(attempt int) time.Duration
	OnFailure         func(Failure)
	DeadLetters       DeadLetterStore
}
type Worker struct {
	broker                    Broker
	config                    WorkerConfig
	mu                        sync.Mutex
	handlers                  map[string]Handler
	cancel, stopReserve       context.CancelFunc
	done                      chan struct{}
	errors                    chan error
	started, stopping, frozen bool
}

func NewWorker(broker Broker, config WorkerConfig) (*Worker, error) {
	if broker == nil {
		return nil, errors.New("queue: broker required")
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
		return nil, errors.New("queue: negative PollInterval")
	}
	if config.ShutdownTimeout == 0 {
		config.ShutdownTimeout = 10 * time.Second
	}
	if config.ShutdownTimeout < 0 {
		return nil, errors.New("queue: negative ShutdownTimeout")
	}
	if config.DrainTimeout == 0 {
		config.DrainTimeout = max(time.Nanosecond, min(time.Second, config.ShutdownTimeout/2))
	}
	if config.DrainTimeout < 0 || config.DrainTimeout >= config.ShutdownTimeout {
		return nil, errors.New("queue: DrainTimeout must be below ShutdownTimeout")
	}
	if config.JobTimeout == 0 {
		config.JobTimeout = time.Minute
	}
	if config.JobTimeout < 0 {
		return nil, errors.New("queue: negative JobTimeout")
	}
	if config.SettlementTimeout == 0 {
		config.SettlementTimeout = 5 * time.Second
	}
	if config.SettlementTimeout < 0 {
		return nil, errors.New("queue: negative SettlementTimeout")
	}
	if config.RetryDelay == nil {
		config.RetryDelay = func(attempt int) time.Duration {
			base := min(time.Minute, time.Second<<min(max(attempt-1, 0), 6))
			return base/2 + time.Duration(rand.Int64N(int64(base/2)+1))
		}
	}
	return &Worker{broker: broker, config: config, handlers: make(map[string]Handler), errors: make(chan error, 1)}, nil
}
func (w *Worker) RegisterHandler(name string, handler Handler) error {
	if name == "" || handler == nil {
		return errors.New("queue: handler name/function required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.frozen {
		return errors.New("queue: handlers cannot change after first start")
	}
	if _, ok := w.handlers[name]; ok {
		return fmt.Errorf("queue: handler %q already registered", name)
	}
	w.handlers[name] = handler
	return nil
}
func (w *Worker) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("queue: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return errors.New("queue: worker running or stopping")
	}
	if len(w.handlers) == 0 {
		return errors.New("queue: no handlers registered")
	}
	for {
		select {
		case <-w.errors:
			continue
		default:
			goto drained
		}
	}
drained:
	reserveCtx, stopReserve := context.WithCancel(context.WithoutCancel(ctx))
	jobsCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	w.cancel, w.stopReserve = cancelJobs, stopReserve
	w.done = make(chan struct{})
	w.started, w.frozen, w.stopping = true, true, false
	done := w.done
	handlers := make(map[string]Handler, len(w.handlers))
	for name, handler := range w.handlers {
		handlers[name] = handler
	}
	go func() {
		var group sync.WaitGroup
		for range w.config.Concurrency {
			group.Add(1)
			go func() { defer group.Done(); w.consume(reserveCtx, jobsCtx, handlers) }()
		}
		group.Wait()
		stopReserve()
		cancelJobs()
		w.mu.Lock()
		w.started, w.stopping = false, false
		close(done)
		w.mu.Unlock()
	}()
	return nil
}
func (w *Worker) consume(reserveCtx, jobsCtx context.Context, handlers map[string]Handler) {
	for reserveCtx.Err() == nil {
		delivery, err := w.broker.Reserve(reserveCtx, w.config.PollInterval)
		if errors.Is(err, ErrEmpty) {
			continue
		}
		if err != nil {
			if reserveCtx.Err() == nil {
				w.failFatal(fmt.Errorf("queue: reserve: %w", err))
			}
			return
		}
		if reserveCtx.Err() != nil {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(jobsCtx), w.config.SettlementTimeout)
			var err error
			if abandoner, ok := delivery.(Abandoner); ok {
				err = abandoner.Abandon(ctx)
			} else {
				err = delivery.Nack(ctx, 0)
			}
			cancel()
			if err != nil {
				w.failFatal(err)
			}
			return
		}
		if err := w.process(jobsCtx, handlers, delivery); err != nil {
			w.failFatal(err)
			return
		}
	}
}
func (w *Worker) process(ctx context.Context, handlers map[string]Handler, delivery Delivery) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = &PanicError{value}
		}
		if err != nil {
			if abandoner, ok := delivery.(Abandoner); ok {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.config.SettlementTimeout)
				defer cancel()
				if releaseErr := abandoner.Abandon(cleanup); releaseErr != nil && !errors.Is(releaseErr, ErrSettled) {
					err = errors.Join(err, fmt.Errorf("queue: abandon: %w", releaseErr))
				}
			}
		}
	}()
	message := delivery.Message()
	handleErr := w.handle(ctx, message, handlers[message.Name])
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.config.SettlementTimeout)
	defer cancel()
	if errors.Is(handleErr, context.Canceled) && ctx.Err() != nil {
		if abandoner, ok := delivery.(Abandoner); ok {
			return abandoner.Abandon(settleCtx)
		}
	}
	if handleErr == nil {
		if err := delivery.Ack(settleCtx); err != nil {
			return fmt.Errorf("queue: acknowledge %q: %w", message.ID, err)
		}
		return nil
	}
	final := message.Attempt >= message.MaxAttempts-1
	if final {
		if w.config.DeadLetters == nil {
			return errors.Join(ErrDeadLetterRequired, handleErr)
		}
		if err := w.config.DeadLetters.Save(settleCtx, FailedJob{Message: message, Error: handleErr.Error(), FailedAt: time.Now().UTC()}); err != nil {
			return fmt.Errorf("queue: dead-letter %q (not acknowledged): %w", message.ID, err)
		}
		if err := delivery.Ack(settleCtx); err != nil {
			return fmt.Errorf("queue: acknowledge dead-letter %q: %w", message.ID, err)
		}
	} else {
		delay := w.config.RetryDelay(message.Attempt + 1)
		delay = min(max(delay, 0), 24*time.Hour)
		if err := delivery.Nack(settleCtx, delay); err != nil {
			return fmt.Errorf("queue: retry %q: %w", message.ID, err)
		}
	}
	if w.config.OnFailure != nil {
		w.config.OnFailure(Failure{Message: message, Err: handleErr, Final: final})
	}
	return nil
}
func (w *Worker) handle(ctx context.Context, message Message, handler Handler) (err error) {
	if handler == nil {
		return fmt.Errorf("queue: no handler for job %q", message.Name)
	}
	jobCtx, cancel := context.WithTimeout(ctx, w.config.JobTimeout)
	defer cancel()
	defer func() {
		if value := recover(); value != nil {
			err = &PanicError{value}
		}
	}()
	if err := handler(jobCtx, message.Payload); err != nil {
		return err
	}
	if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}
func (w *Worker) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("queue: context is nil")
	}
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return nil
	}
	w.stopping = true
	stop, cancel, done := w.stopReserve, w.cancel, w.done
	w.mu.Unlock()
	stop()
	shutdown, end := context.WithTimeout(ctx, w.config.ShutdownTimeout)
	defer end()
	grace, stopGrace := context.WithTimeout(shutdown, w.config.DrainTimeout)
	defer stopGrace()
	select {
	case <-done:
		return nil
	case <-grace.Done():
		cancel()
	}
	select {
	case <-done:
		return nil
	case <-shutdown.Done():
		return fmt.Errorf("queue: worker shutdown: %w", shutdown.Err())
	}
}
func (w *Worker) failFatal(err error) {
	w.mu.Lock()
	if w.started {
		w.stopping = true
		w.stopReserve()
		w.cancel()
	}
	w.mu.Unlock()
	select {
	case w.errors <- err:
	default:
	}
}
func (w *Worker) Errors() <-chan error                               { return w.errors }
func (w *Worker) ID() string                                         { return "queue.worker" }
func (w *Worker) Register(a *app.Application) error                  { return a.Components.Instance("queue.worker", w) }
func (w *Worker) Boot(ctx context.Context, _ *app.Application) error { return w.Start(ctx) }
