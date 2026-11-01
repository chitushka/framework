// Package app управляет жизненным циклом приложения и его модулей.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

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

// Application объединяет общие компоненты и модули.
type Application struct {
	Container  *di.Container
	Events     *event.Bus
	Components *component.Registry

	mu      sync.Mutex
	modules []Module
	started []Module
	state   lifecycleState
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
	application := &Application{Container: container, Events: events, Components: components}
	_ = components.Instance("app", application)
	_ = components.Instance("components", components)
	_ = components.Instance("container", container)
	_ = components.Instance("events", events)
	return application
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
	a.mu.Lock()
	if a.state != stateInitial {
		a.mu.Unlock()
		return errors.New("app: приложение уже запущено или запускается")
	}
	a.state = stateStarting
	modules := append([]Module(nil), a.modules...)
	a.mu.Unlock()

	for _, module := range modules {
		if err := module.Register(a); err != nil {
			a.setStopped()
			return fmt.Errorf("app: регистрация модуля %q: %w", module.ID(), err)
		}
	}
	if err := a.Events.Publish(ctx, event.Event{Name: EventBeforeStart, Payload: a}); err != nil {
		a.setStopped()
		return err
	}
	started := make([]Module, 0, len(modules))
	for _, module := range modules {
		if err := module.Boot(ctx, a); err != nil {
			rollbackErr := shutdownModules(ctx, started)
			a.setStopped()
			return errors.Join(fmt.Errorf("app: запуск модуля %q: %w", module.ID(), err), rollbackErr)
		}
		started = append(started, module)
	}
	if err := a.Events.Publish(ctx, event.Event{Name: EventAfterStart, Payload: a}); err != nil {
		rollbackErr := shutdownModules(ctx, started)
		a.setStopped()
		return errors.Join(err, rollbackErr)
	}
	a.mu.Lock()
	a.started = started
	a.state = stateRunning
	a.mu.Unlock()
	return nil
}

// Shutdown останавливает модули в обратном порядке.
func (a *Application) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	if a.state != stateRunning {
		a.mu.Unlock()
		return nil
	}
	a.state = stateStopping
	started := append([]Module(nil), a.started...)
	a.mu.Unlock()

	beforeErr := a.Events.Publish(ctx, event.Event{Name: EventBeforeStop, Payload: a})
	shutdownErr := shutdownModules(ctx, started)
	afterErr := a.Events.Publish(ctx, event.Event{Name: EventAfterStop, Payload: a})
	a.setStopped()
	return errors.Join(beforeErr, shutdownErr, afterErr)
}

// Run запускает приложение и ожидает отмены контекста.
func (a *Application) Run(ctx context.Context) error {
	if err := a.Start(ctx); err != nil {
		return err
	}
	runtimeContext, cancelRuntimeWatchers := context.WithCancel(ctx)
	defer cancelRuntimeWatchers()
	runtimeErrors := a.runtimeErrors(runtimeContext)
	select {
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

func (a *Application) setStopped() {
	a.mu.Lock()
	a.started = nil
	a.state = stateStopped
	a.mu.Unlock()
}

func shutdownModules(ctx context.Context, modules []Module) error {
	var errs []error
	for i := len(modules) - 1; i >= 0; i-- {
		module := modules[i]
		if err := module.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("app: остановка модуля %q: %w", module.ID(), err))
		}
	}
	return errors.Join(errs...)
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
