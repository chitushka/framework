// Package event реализует локальную шину событий приложения.
package event

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Event описывает событие, передаваемое обработчикам.
type Event struct {
	Name    string
	Payload any
	Time    time.Time
}

// Handler обрабатывает одно событие.
type Handler func(context.Context, Event) error

type subscription struct {
	id       uint64
	priority int
	handler  Handler
}

// Bus безопасно хранит подписки для конкурентного использования.
type Bus struct {
	mu       sync.RWMutex
	nextID   uint64
	handlers map[string][]subscription
}

// NewBus создаёт пустую шину.
func NewBus() *Bus { return &Bus{handlers: make(map[string][]subscription)} }

// Subscribe добавляет обработчик. Больший priority выполняется раньше.
// Возвращаемая функция отменяет подписку и безопасна при повторном вызове.
func (b *Bus) Subscribe(name string, priority int, handler Handler) (func(), error) {
	if name == "" {
		return nil, errors.New("event: имя события обязательно")
	}
	if handler == nil {
		return nil, errors.New("event: обработчик обязателен")
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.handlers[name] = append(b.handlers[name], subscription{id: id, priority: priority, handler: handler})
	sort.SliceStable(b.handlers[name], func(i, j int) bool {
		return b.handlers[name][i].priority > b.handlers[name][j].priority
	})
	b.mu.Unlock()

	var once sync.Once
	return func() { once.Do(func() { b.unsubscribe(name, id) }) }, nil
}

// Publish последовательно вызывает обработчики в порядке приоритета.
func (b *Bus) Publish(ctx context.Context, event Event) error {
	handlers := b.snapshot(event.Name)
	event = normalize(event)
	var errs []error
	for _, item := range handlers {
		if err := callSafely(ctx, item.handler, event); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// PublishConcurrent параллельно вызывает все обработчики события.
func (b *Bus) PublishConcurrent(ctx context.Context, event Event) error {
	handlers := b.snapshot(event.Name)
	event = normalize(event)
	errorsByHandler := make([]error, len(handlers))
	var wait sync.WaitGroup
	wait.Add(len(handlers))
	for i, item := range handlers {
		go func() {
			defer wait.Done()
			errorsByHandler[i] = callSafely(ctx, item.handler, event)
		}()
	}
	wait.Wait()
	return errors.Join(errorsByHandler...)
}

func (b *Bus) snapshot(name string) []subscription {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]subscription(nil), b.handlers[name]...)
}

func (b *Bus) unsubscribe(name string, id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	items := b.handlers[name]
	for i, item := range items {
		if item.id == id {
			b.handlers[name] = append(items[:i], items[i+1:]...)
			break
		}
	}
}

func normalize(event Event) Event {
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	return event
}

func callSafely(ctx context.Context, handler Handler, event Event) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("event: паника обработчика %q: %v", event.Name, recovered)
		}
	}()
	return handler(ctx, event)
}
