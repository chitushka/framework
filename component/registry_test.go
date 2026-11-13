package component_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chitushka/framework/component"
)

type database struct{ name string }
type service struct{ database *database }

func TestRegistryResolvesDependenciesAndCachesSingleton(t *testing.T) {
	registry := component.New()
	var calls atomic.Int32
	if err := registry.Register("db", func(context.Context, component.Resolver) (any, error) {
		calls.Add(1)
		return &database{name: "main"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("service", func(ctx context.Context, resolver component.Resolver) (any, error) {
		db, err := component.ResolveAs[*database](ctx, resolver, "db")
		if err != nil {
			return nil, err
		}
		return &service{database: db}, nil
	}); err != nil {
		t.Fatal(err)
	}

	first, err := component.GetAs[*service](context.Background(), registry, "service")
	if err != nil {
		t.Fatal(err)
	}
	second, err := component.GetAs[*service](context.Background(), registry, "service")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.database.name != "main" || calls.Load() != 1 {
		t.Fatalf("неверный singleton: %#v %#v, вызовов %d", first, second, calls.Load())
	}
}

func TestRegistryFactoryUsesResolver(t *testing.T) {
	registry := component.New()
	_ = registry.Instance("db", &database{name: "dependency"})
	_ = registry.Register("service", func(ctx context.Context, resolver component.Resolver) (any, error) {
		value, err := resolver.Get(ctx, "db")
		if err != nil {
			return nil, err
		}
		return &service{database: value.(*database)}, nil
	})
	resolved, err := component.GetAs[*service](context.Background(), registry, "service")
	if err != nil || resolved.database.name != "dependency" {
		t.Fatalf("неверная зависимость: %#v, %v", resolved, err)
	}
}

func TestRegistryTransientAndNames(t *testing.T) {
	registry := component.New()
	_ = registry.Instance("alpha", "value")
	_ = registry.Register("temporary", func(context.Context, component.Resolver) (any, error) {
		return &database{}, nil
	}, component.Transient())
	first, _ := registry.Get(context.Background(), "temporary")
	second, _ := registry.Get(context.Background(), "temporary")
	if first == second {
		t.Fatal("transient-компонент был закэширован")
	}
	if !reflect.DeepEqual(registry.Names(), []string{"alpha", "temporary"}) {
		t.Fatalf("неверные имена: %v", registry.Names())
	}
}

func TestRegistryCreatesSingletonOnceConcurrently(t *testing.T) {
	registry := component.New()
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	_ = registry.Register("slow", func(context.Context, component.Resolver) (any, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return &database{}, nil
	})
	var wait sync.WaitGroup
	wait.Add(2)
	results := make([]any, 2)
	for i := range results {
		go func() {
			defer wait.Done()
			results[i], _ = registry.Get(context.Background(), "slow")
		}()
	}
	<-started
	close(release)
	wait.Wait()
	if calls.Load() != 1 || results[0] != results[1] {
		t.Fatalf("singleton создан %d раз", calls.Load())
	}
}

func TestRegistryDetectsCycleAndRetriesFailure(t *testing.T) {
	registry := component.New()
	_ = registry.Register("first", func(ctx context.Context, resolver component.Resolver) (any, error) {
		return resolver.Get(ctx, "second")
	})
	_ = registry.Register("second", func(ctx context.Context, resolver component.Resolver) (any, error) {
		return resolver.Get(ctx, "first")
	})
	if _, err := registry.Get(context.Background(), "first"); err == nil || !strings.Contains(err.Error(), "циклическая") {
		t.Fatalf("ожидалась ошибка цикла, получено %v", err)
	}

	retry := component.New()
	var calls atomic.Int32
	want := errors.New("temporary")
	_ = retry.Register("value", func(context.Context, component.Resolver) (any, error) {
		if calls.Add(1) == 1 {
			return nil, want
		}
		return "ready", nil
	})
	if _, err := retry.Get(context.Background(), "value"); !errors.Is(err, want) {
		t.Fatalf("получена ошибка %v", err)
	}
	if value, err := retry.Get(context.Background(), "value"); err != nil || value != "ready" {
		t.Fatalf("повтор не удался: %v, %v", value, err)
	}
}

func TestRegistryValidatesInputAndType(t *testing.T) {
	registry := component.New()
	var pointer *database
	for _, err := range []error{
		registry.Register("", func(context.Context, component.Resolver) (any, error) { return "", nil }),
		registry.Register("value", nil),
		registry.Instance("nil", pointer),
		registry.Register("option", func(context.Context, component.Resolver) (any, error) { return "", nil }, nil),
	} {
		if err == nil {
			t.Fatal("ожидалась ошибка валидации")
		}
	}
	_ = registry.Instance("value", "text")
	if err := registry.Instance("value", "duplicate"); err == nil {
		t.Fatal("ожидалась ошибка дубликата")
	}
	if _, err := component.GetAs[int](context.Background(), registry, "value"); err == nil {
		t.Fatal("ожидалась ошибка типа")
	}
	if _, err := registry.Get(context.Background(), "missing"); err == nil {
		t.Fatal("ожидалась ошибка отсутствующего компонента")
	}
	if _, err := registry.Get(nil, "value"); err == nil { //nolint:staticcheck // SA1012: intentionally tests rejection of a nil context.
		t.Fatal("ожидалась ошибка nil context")
	}
	if _, err := component.GetAs[string](context.Background(), nil, "value"); err == nil {
		t.Fatal("ожидалась ошибка nil registry")
	}
	if _, err := component.ResolveAs[string](context.Background(), nil, "value"); err == nil {
		t.Fatal("ожидалась ошибка nil resolver")
	}
}

func TestRegistryRecoversFactoryPanicAndHonorsWaitCancellation(t *testing.T) {
	registry := component.New()
	_ = registry.Register("panic", func(context.Context, component.Resolver) (any, error) { panic("boom") })
	if _, err := registry.Get(context.Background(), "panic"); err == nil || !strings.Contains(err.Error(), "паника") {
		t.Fatalf("ожидалась ошибка паники, получено %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	_ = registry.Register("blocked", func(context.Context, component.Resolver) (any, error) {
		close(started)
		<-release
		return "ready", nil
	})
	go func() { _, _ = registry.Get(context.Background(), "blocked") }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := registry.Get(ctx, "blocked"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("получена ошибка %v", err)
	}
	close(release)
}
