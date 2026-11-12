// Package app управляет жизненным циклом приложения и его модулей.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chitushka/framework/component"
	"github.com/chitushka/framework/di"
	"github.com/chitushka/framework/event"
)

const (
	EventBeforeStart = "app.before_start"
	EventAfterStart  = "app.after_start"
	EventBeforeStop  = "app.before_stop"
	EventAfterStop   = "app.after_stop"
)

type lifecycleState uint8

const (
	stateInitial lifecycleState = iota
	stateStarting
	stateRunning
	stateStopping
	stateStopped
)

// Module представляет изолированную функциональную часть приложения.
type Module interface {
	ID() string
	Register(*Application) error
	Boot(context.Context, *Application) error
	Shutdown(context.Context) error
}

// ErrorSource позволяет модулю сообщить о критической ошибке после успешного Boot.
// Application.Run остановит приложение и вернёт первую полученную ошибку.
type ErrorSource interface {
	Errors() <-chan error
}

// Quiescer stops admitting new work before modules are shut down in reverse order.
// It must leave resources needed by in-flight work available until Shutdown.
type Quiescer interface {
	Quiesce(context.Context) error
}

const DefaultShutdownTimeout = 15 * time.Second

// Application объединяет общие компоненты и модули.
type Application struct {
	Container  *di.Container
	Events     *event.Bus
	Components *component.Registry

	mu              sync.Mutex
	modules         []Module
	started         []Module
	state           lifecycleState
	shutdownTimeout time.Duration
	startCancel     context.CancelFunc
	startDone       chan struct{}
	done            chan struct{}
	shutdownErr     error
}

// New создаёт приложение. Nil-аргументы заменяются стандартными реализациями.
func New(container *di.Container, events *event.Bus) *Application {
	if container == nil {
		container = di.New()
	}
	if events == nil {
		events = event.NewBus()
	}
	components := component.New()
	application := &Application{Container: container, Events: events, Components: components,
		shutdownTimeout: DefaultShutdownTimeout, done: make(chan struct{})}
	_ = components.Instance("app", application)
	_ = components.Instance("components", components)
	_ = components.Instance("container", container)
	_ = components.Instance("events", events)
	return application
}

// SetShutdownTimeout sets the total budget for hooks, quiesce, drain and finalizers.
// It must be called before Start. Module timeouts cannot extend this budget.
func (a *Application) SetShutdownTimeout(timeout time.Duration) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if timeout <= 0 || a.state != stateInitial {
		return errors.New("app: positive shutdown timeout must be set before Start")
	}
	a.shutdownTimeout = timeout
	return nil
}

// AddModule добавляет модуль до запуска приложения.
func (a *Application) AddModule(module Module) error {
	if module == nil {
		return errors.New("app: модуль не может быть nil")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != stateInitial {
		return errors.New("app: нельзя добавлять модуль после начала запуска")
	}
	for _, existing := range a.modules {
		if existing.ID() == module.ID() {
			return fmt.Errorf("app: модуль %q уже добавлен", module.ID())
		}
	}
	a.modules = append(a.modules, module)
	return nil
}

// Start регистрирует и запускает модули в порядке добавления.
func (a *Application) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("app: context is nil")
	}
	a.mu.Lock()
	if a.state != stateInitial {
		a.mu.Unlock()
		return errors.New("app: приложение уже запущено или запускается")
	}
	a.state = stateStarting
	ctx, a.startCancel = context.WithCancel(ctx)
	a.startDone = make(chan struct{})
	startDone := a.startDone
	modules := append([]Module(nil), a.modules...)
	a.mu.Unlock()
	defer close(startDone)
	var started []Module
	fail := func(err error) error {
		a.mu.Lock()
		a.state = stateStopping
		a.mu.Unlock()
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.shutdownTimeout)
		defer cancel()
		return errors.Join(err, a.stop(cleanup, started))
	}

	for _, module := range modules {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := boundedCall(ctx, func() error { return module.Register(a) }); err != nil {
			return fail(fmt.Errorf("app: регистрация модуля %q: %w", module.ID(), err))
		}
	}
	if err := boundedCall(ctx, func() error { return a.Events.Publish(ctx, event.Event{Name: EventBeforeStart, Payload: a}) }); err != nil {
		return fail(err)
	}
	for _, module := range modules {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := boundedCall(ctx, func() error { return module.Boot(ctx, a) }); err != nil {
			return fail(fmt.Errorf("app: запуск модуля %q: %w", module.ID(), err))
		}
		started = append(started, module)
	}
	if err := boundedCall(ctx, func() error { return a.Events.Publish(ctx, event.Event{Name: EventAfterStart, Payload: a}) }); err != nil {
		return fail(err)
	}
	a.mu.Lock()
	// Serialize the running transition with Shutdown cancelling startup.
	if err := ctx.Err(); err != nil {
		a.mu.Unlock()
		return fail(err)
	}
	a.started = started
	a.state = stateRunning
	a.mu.Unlock()
	return nil
}

// Shutdown останавливает модули в обратном порядке.
func (a *Application) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("app: context is nil")
	}
	a.mu.Lock()
	if a.state == stateStarting {
		a.startCancel()
		started := a.startDone
		a.mu.Unlock()
		select {
		case <-started:
			return a.Shutdown(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if a.state == stateInitial {
		a.mu.Unlock()
		return nil
	}
	if a.state == stateRunning {
		a.state = stateStopping // Readiness is withdrawn before any stop hook.
		started := append([]Module(nil), a.started...)
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.shutdownTimeout)
		if deadline, ok := ctx.Deadline(); ok {
			cancel()
			cleanup, cancel = context.WithDeadline(context.WithoutCancel(ctx), minDeadline(deadline, time.Now().Add(a.shutdownTimeout)))
		}
		go func() { defer cancel(); _ = a.stop(cleanup, started) }()
	}
	done := a.done
	a.mu.Unlock()
	select {
	case <-done:
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run запускает приложение и ожидает отмены контекста.
func (a *Application) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("app: context is nil")
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := a.Start(ctx); err != nil {
		return err
	}
	runtimeContext, cancelRuntimeWatchers := context.WithCancel(ctx)
	defer cancelRuntimeWatchers()
	runtimeErrors := a.runtimeErrors(runtimeContext)
	select {
	case <-a.done:
		return a.Shutdown(context.Background())
	case <-ctx.Done():
		return a.Shutdown(context.WithoutCancel(ctx))
	case err := <-runtimeErrors:
		cancelRuntimeWatchers()
		return errors.Join(err, a.Shutdown(context.WithoutCancel(ctx)))
	}
}

// Running сообщает, завершён ли успешный запуск приложения.
func (a *Application) Running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state == stateRunning
}

// Live reports whether this instance can still make lifecycle progress. It does
// not probe dependencies and stays true while starting or draining.
func (a *Application) Live() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state != stateStopped
}

func (a *Application) stop(ctx context.Context, modules []Module) error {
	var errs []error
	publish := func(name string) {
		if err := boundedCall(ctx, func() error { return a.Events.Publish(ctx, event.Event{Name: name, Payload: a}) }); err != nil {
			errs = append(errs, fmt.Errorf("app: %s: %w", name, err))
		}
	}
	publish(EventBeforeStop)
	for i := len(modules) - 1; i >= 0; i-- {
		if module, ok := modules[i].(Quiescer); ok {
			if err := boundedCall(ctx, func() error { return module.Quiesce(ctx) }); err != nil {
				errs = append(errs, fmt.Errorf("app: quiesce %q: %w", modules[i].ID(), err))
			}
		}
	}
	for i := len(modules) - 1; i >= 0; i-- {
		module := modules[i]
		if err := boundedCall(ctx, func() error { return module.Shutdown(ctx) }); err != nil {
			errs = append(errs, fmt.Errorf("app: остановка модуля %q: %w", module.ID(), err))
		}
	}
	publish(EventAfterStop)
	if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	err := errors.Join(errs...)
	a.mu.Lock()
	a.startCancel()
	a.started = nil
	a.state = stateStopped
	a.shutdownErr = err
	close(a.done)
	a.mu.Unlock()
	return err
}

// A non-cooperative callback cannot hold up the process. Its goroutine can only
// be reclaimed by the callback returning or process exit; never restart this app.
func boundedCall(ctx context.Context, call func() error) error {
	done := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if value := recover(); value != nil {
				err = fmt.Errorf("app: lifecycle panic: %v", value)
			}
			done <- err
		}()
		err = call()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func minDeadline(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (a *Application) runtimeErrors(ctx context.Context) <-chan error {
	result := make(chan error, 1)
	a.mu.Lock()
	modules := append([]Module(nil), a.started...)
	a.mu.Unlock()
	for _, module := range modules {
		source, ok := module.(ErrorSource)
		if !ok {
			continue
		}
		errorsFromModule := source.Errors()
		if errorsFromModule == nil {
			continue
		}
		go func(errorsFromModule <-chan error) {
			select {
			case err, open := <-errorsFromModule:
				if !open || err == nil {
					return
				}
				select {
				case result <- err:
				case <-ctx.Done():
				}
			case <-ctx.Done():
			}
		}(errorsFromModule)
	}
	return result
}
