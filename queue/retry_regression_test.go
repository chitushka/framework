package queue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestMemoryReserveCancelledContextPreservesMessage(t *testing.T) {
	for _, wait := range []time.Duration{0, time.Second} {
		t.Run(wait.String(), func(t *testing.T) {
			broker := NewMemoryBroker()
			message := Message{ID: "job", Name: "job", Payload: json.RawMessage(`null`), MaxAttempts: 3}
			if err := broker.Enqueue(t.Context(), message, 0); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if delivery, err := broker.Reserve(ctx, wait); !errors.Is(err, context.Canceled) || delivery != nil {
				t.Fatalf("cancelled reserve = %v, %v", delivery, err)
			}
			delivery, err := broker.Reserve(t.Context(), 0)
			if err != nil || delivery.Message().ID != message.ID {
				t.Fatalf("message was lost on cancellation: %v, %v", delivery, err)
			}
			if err := delivery.Ack(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type retryErrorStore struct {
	RedisStore
	attempts []int
	err      error
}

func (store *retryErrorStore) Retry(_ context.Context, _, _ string, payload []byte, _ time.Time) error {
	var message Message
	if err := json.Unmarshal(payload, &message); err != nil {
		return err
	}
	store.attempts = append(store.attempts, message.Attempt)
	if len(store.attempts) == 1 {
		return store.err
	}
	return nil
}

type retryErrorChannel struct {
	RabbitChannel
	attempts []int
	err      error
}

func (channel *retryErrorChannel) Publish(_ context.Context, _ string, payload []byte, _ time.Duration) error {
	var message Message
	if err := json.Unmarshal(payload, &message); err != nil {
		return err
	}
	channel.attempts = append(channel.attempts, message.Attempt)
	if len(channel.attempts) == 1 {
		return channel.err
	}
	return nil
}

type retryErrorMessage struct {
	RabbitMessage
	calls int
	err   error
}

func (message *retryErrorMessage) Ack(context.Context) error {
	message.calls++
	if message.calls == 1 {
		return message.err
	}
	return nil
}

func TestNackFailureDoesNotConsumeExtraAttempts(t *testing.T) {
	for _, scenario := range []string{"redis retry", "rabbit publish", "rabbit ack"} {
		t.Run(scenario, func(t *testing.T) {
			message := Message{ID: "job", Name: "job", Payload: json.RawMessage(`null`), MaxAttempts: 3}
			unavailable := errors.New("temporarily unavailable")
			var delivery Delivery
			var attempts *[]int
			switch scenario {
			case "redis retry":
				store := &retryErrorStore{err: unavailable}
				delivery = &redisDelivery{broker: &RedisBroker{store: store, now: time.Now}, message: message}
				attempts = &store.attempts
			case "rabbit publish", "rabbit ack":
				channel := &retryErrorChannel{}
				source := &retryErrorMessage{}
				if scenario == "rabbit publish" {
					channel.err = unavailable
				} else {
					source.err = unavailable
				}
				delivery = &rabbitDelivery{broker: &RabbitMQBroker{channel: channel}, source: source, message: message}
				attempts = &channel.attempts
			}
			if err := delivery.Nack(t.Context(), 0); !errors.Is(err, unavailable) {
				t.Fatalf("first Nack = %v", err)
			}
			if got := delivery.Message().Attempt; got != 0 {
				t.Fatalf("failed Nack changed original attempt to %d", got)
			}
			if err := delivery.Nack(t.Context(), 0); err != nil {
				t.Fatalf("retry Nack = %v", err)
			}
			if len(*attempts) != 2 || (*attempts)[0] != 1 || (*attempts)[1] != 1 {
				t.Fatalf("published retry attempts = %v, want [1 1]", *attempts)
			}
			if err := delivery.Nack(t.Context(), 0); !errors.Is(err, ErrSettled) {
				t.Fatalf("settled Nack = %v", err)
			}
		})
	}
}
