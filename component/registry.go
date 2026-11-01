// Package component предоставляет именованный реестр компонентов приложения.
package component

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Resolver разрешает зависимости фабрики по имени.
type Resolver interface {
	Get(context.Context, string) (any, error)
}

// Factory создаёт компонент и может запросить другие компоненты через Resolver.
type Factory func(context.Context, Resolver) (any, error)

// Option настраивает определение компонента.
type Option func(*entry)

// Transient отключает кэширование компонента. По умолчанию компоненты являются singleton.
func Transient() Option {
	return func(item *entry) { item.singleton = false }
}

type entry struct {
	factory   Factory
	singleton bool

	mu       sync.Mutex
	creating bool
	ready    bool
	wait     chan struct{}
	value    any
}

// Registry хранит лениво создаваемые компоненты приложения по имени.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*entry
}

// New создаёт пустой реестр.
func New() *Registry {
	return &Registry{entries: make(map[string]*entry)}
}

// Register регистрирует ленивую фабрику singleton-компонента.
func (r *Registry) Register(name string, factory Factory, options ...Option) error {
	if err := validateName(name); err != nil {
		return err
	}
	if factory == nil {
		return errors.New("component: фабрика не может быть nil")
	}
	item := &entry{factory: factory, singleton: true}
	for _, option := range options {
		if option == nil {
			return errors.New("component: опция не может быть nil")
		}
		option(item)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[name]; exists {
		return fmt.Errorf("component: имя %q уже зарегистрировано", name)
	}
	r.entries[name] = item
	return nil
}

// Instance регистрирует готовый singleton-компонент.
func (r *Registry) Instance(name string, value any) error {
	if nilValue(value) {
		return errors.New("component: значение не может быть nil")
	}
	return r.Register(name, func(context.Context, Resolver) (any, error) {
		return value, nil
	})
}

// Get возвращает компонент, создавая его при первом обращении.
func (r *Registry) Get(ctx context.Context, name string) (any, error) {
	if ctx == nil {
		return nil, errors.New("component: context не может быть nil")
	}
	return (&resolver{registry: r}).get(ctx, name)
}

// Names возвращает отсортированный снимок зарегистрированных имён.
func (r *Registry) Names() []string {
	r.mu.RLock()
	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	r.mu.RUnlock()
	sort.Strings(names)
	return names
}

// GetAs разрешает именованный компонент и проверяет его тип.
func GetAs[T any](ctx context.Context, registry *Registry, name string) (T, error) {
	var zero T
	if registry == nil {
		return zero, errors.New("component: registry не может быть nil")
	}
	return ResolveAs[T](ctx, registry, name)
}

// ResolveAs разрешает компонент через переданный Resolver и проверяет его тип.
// Функция удобна внутри Factory, где доступен ограниченный интерфейс Resolver.
func ResolveAs[T any](ctx context.Context, resolver Resolver, name string) (T, error) {
	var zero T
	if resolver == nil {
		return zero, errors.New("component: resolver не может быть nil")
	}
	value, err := resolver.Get(ctx, name)
	if err != nil {
		return zero, err
	}
	typed, ok := value.(T)
	if !ok {
		return zero, fmt.Errorf("component: %q имеет тип %T, ожидался %T", name, value, zero)
	}
	return typed, nil
}

type resolver struct {
	registry *Registry
	path     []string
}

func (r *resolver) Get(ctx context.Context, name string) (any, error) {
	return r.get(ctx, name)
}

func (r *resolver) get(ctx context.Context, name string) (any, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	for _, dependency := range r.path {
		if dependency == name {
			path := append(append([]string(nil), r.path...), name)
			return nil, fmt.Errorf("component: циклическая зависимость: %s", strings.Join(path, " -> "))
		}
	}

	r.registry.mu.RLock()
	item := r.registry.entries[name]
	r.registry.mu.RUnlock()
	if item == nil {
		return nil, fmt.Errorf("component: компонент %q не зарегистрирован", name)
	}
	child := &resolver{
		registry: r.registry,
		path:     append(append([]string(nil), r.path...), name),
	}
	if !item.singleton {
		return callFactory(ctx, item.factory, child, name)
	}
	return item.get(ctx, child, name)
}

func (e *entry) get(ctx context.Context, resolver Resolver, name string) (any, error) {
	for {
		e.mu.Lock()
		if e.ready {
			value := e.value
			e.mu.Unlock()
			return value, nil
		}
		if e.creating {
			wait := e.wait
			e.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("component: ожидание %q: %w", name, ctx.Err())
			}
		}
		e.creating = true
		e.wait = make(chan struct{})
		e.mu.Unlock()

		value, err := callFactory(ctx, e.factory, resolver, name)

		e.mu.Lock()
		if err == nil {
			e.value = value
			e.ready = true
		}
		e.creating = false
		close(e.wait)
		e.mu.Unlock()
		return value, err
	}
}

func callFactory(ctx context.Context, factory Factory, resolver Resolver, name string) (value any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("component: паника фабрики %q: %v", name, recovered)
		}
	}()
	value, err = factory(ctx, resolver)
	if err != nil {
		return nil, fmt.Errorf("component: создать %q: %w", name, err)
	}
	if nilValue(value) {
		return nil, fmt.Errorf("component: фабрика %q вернула nil", name)
	}
	return value, nil
}

func validateName(name string) error {
	if name == "" || strings.TrimSpace(name) != name {
		return errors.New("component: имя должно быть непустым и не содержать крайних пробелов")
	}
	return nil
}

func nilValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
