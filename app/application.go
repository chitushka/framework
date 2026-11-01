// Package app управляет жизненным циклом приложения и его модулей.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

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
	Register(*di.Container) error
	Boot(context.Context, *Application) error
	Shutdown(context.Context) error
}

// Application объединяет общие компоненты и модули.
type Application struct {
	Container *di.Container
	Events    *event.Bus

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
	return &Application{Container: container, Events: events}
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
		if err := module.Register(a.Container); err != nil {
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
	<-ctx.Done()
	return a.Shutdown(context.WithoutCancel(ctx))
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
