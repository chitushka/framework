package event_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chitushka/framework/event"
)

func TestBusUsesPriorityAndUnsubscribe(t *testing.T) {
	bus := event.NewBus()
	var order []int
	_, _ = bus.Subscribe("saved", 0, func(context.Context, event.Event) error {
		order = append(order, 2)
		return nil
	})
	unsubscribe, _ := bus.Subscribe("saved", 10, func(context.Context, event.Event) error {
		order = append(order, 1)
		return nil
	})
	if err := bus.Publish(context.Background(), event.Event{Name: "saved"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []int{1, 2}) {
		t.Fatalf("неверный порядок: %v", order)
	}
	unsubscribe()
	order = nil
	_ = bus.Publish(context.Background(), event.Event{Name: "saved"})
	if !reflect.DeepEqual(order, []int{2}) {
		t.Fatalf("подписка не удалена: %v", order)
	}
}

func TestBusJoinsErrorsAndRecoversPanic(t *testing.T) {
	bus := event.NewBus()
	want := errors.New("ошибка")
	_, _ = bus.Subscribe("failed", 0, func(context.Context, event.Event) error { return want })
	_, _ = bus.Subscribe("failed", 0, func(context.Context, event.Event) error { panic("авария") })
	err := bus.Publish(context.Background(), event.Event{Name: "failed"})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "паника") {
		t.Fatalf("неверная объединённая ошибка: %v", err)
	}
}

func TestBusPublishesConcurrently(t *testing.T) {
	bus := event.NewBus()
	var calls atomic.Int32
	for range 4 {
		_, _ = bus.Subscribe("work", 0, func(context.Context, event.Event) error {
			calls.Add(1)
			return nil
		})
	}
	if err := bus.PublishConcurrent(context.Background(), event.Event{Name: "work"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("вызвано обработчиков: %d", calls.Load())
	}
}
