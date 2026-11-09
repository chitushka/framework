package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RedisStore is the small reliable-list contract expected from a Redis client
// adapter. Reserve must atomically move a due payload into a leased state.
type RedisStore interface {
	Push(context.Context, string, []byte, time.Time) error
	Reserve(context.Context, string, time.Time, time.Duration) ([]byte, string, bool, error)
	Ack(context.Context, string, string) error
	Retry(context.Context, string, string, []byte, time.Time) error
}

type RedisQuarantiner interface {
	Quarantine(context.Context, string, string, []byte, string) error
}

func (broker *RedisBroker) quarantine(ctx context.Context, receipt string, payload []byte, cause error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if store, ok := broker.store.(RedisQuarantiner); ok {
		return errors.Join(cause, store.Quarantine(cleanup, broker.queue, receipt, payload, cause.Error()))
	}
	return errors.Join(cause, errors.New("queue: invalid payload retained under lease; store has no quarantine"))
}

type RedisConfig struct {
	Store              RedisStore
	Queue              string
	ReservationTimeout time.Duration
	PollInterval       time.Duration
}
type RedisBroker struct {
	store                        RedisStore
	queue                        string
	reservationTimeout, timePoll time.Duration
	now                          func() time.Time
}

func NewRedisBroker(config RedisConfig) (*RedisBroker, error) {
	if config.Store == nil {
		return nil, errors.New("queue: Redis store is required")
	}
	if config.Queue == "" {
		config.Queue = "default"
	}
	if config.ReservationTimeout == 0 {
		config.ReservationTimeout = 5 * time.Minute
	}
	if config.ReservationTimeout < 0 {
		return nil, errors.New("queue: Redis ReservationTimeout cannot be negative")
	}
	if config.PollInterval == 0 {
		config.PollInterval = 250 * time.Millisecond
	}
	if config.PollInterval < 0 {
		return nil, errors.New("queue: Redis PollInterval cannot be negative")
	}
	return &RedisBroker{store: config.Store, queue: config.Queue, reservationTimeout: config.ReservationTimeout, timePoll: config.PollInterval, now: time.Now}, nil
}
func (broker *RedisBroker) Enqueue(ctx context.Context, message Message, delay time.Duration) error {
	if ctx == nil {
		return errors.New("queue: context is nil")
	}
	if delay < 0 {
		return errors.New("queue: delay cannot be negative")
	}
	if err := validateMessage(message); err != nil {
		return err
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("queue: encode Redis message: %w", err)
	}
	if err := broker.store.Push(ctx, broker.queue, payload, broker.now().Add(delay)); err != nil {
		return fmt.Errorf("queue: Redis push: %w", err)
	}
	return nil
}
func (broker *RedisBroker) Reserve(ctx context.Context, wait time.Duration) (Delivery, error) {
	if ctx == nil {
		return nil, errors.New("queue: context is nil")
	}
	if wait < 0 {
		return nil, errors.New("queue: wait cannot be negative")
	}
	deadline := broker.now().Add(wait)
	for {
		payload, receipt, found, err := broker.store.Reserve(ctx, broker.queue, broker.now(), broker.reservationTimeout)
		if err != nil {
			return nil, fmt.Errorf("queue: Redis reserve: %w", err)
		}
		if found {
			var message Message
			if err := json.Unmarshal(payload, &message); err != nil {
				return nil, broker.quarantine(ctx, receipt, payload, fmt.Errorf("queue: decode Redis message: %w", err))
			}
			if err := validateMessage(message); err != nil {
				return nil, broker.quarantine(ctx, receipt, payload, err)
			}
			return &redisDelivery{broker: broker, message: message, receipt: receipt}, nil
		}
		remaining := deadline.Sub(broker.now())
		if wait == 0 || remaining <= 0 {
			return nil, ErrEmpty
		}
		pause := broker.timePoll
		if pause > remaining {
			pause = remaining
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

type redisDelivery struct {
	broker     *RedisBroker
	message    Message
	receipt    string
	settlement settlement
}

func (delivery *redisDelivery) Message() Message { return cloneMessage(delivery.message) }
func (delivery *redisDelivery) Ack(ctx context.Context) error {
	return delivery.settlement.run(func() error {
		if ctx == nil {
			return errors.New("queue: context is nil")
		}
		return delivery.broker.store.Ack(ctx, delivery.broker.queue, delivery.receipt)
	})
}
func (delivery *redisDelivery) Nack(ctx context.Context, delay time.Duration) error {
	return delivery.settlement.run(func() error {
		if ctx == nil {
			return errors.New("queue: context is nil")
		}
		if delay < 0 {
			return errors.New("queue: delay cannot be negative")
		}
		message := delivery.message
		message.Attempt++
		payload, err := json.Marshal(message)
		if err != nil {
			return err
		}
		return delivery.broker.store.Retry(ctx, delivery.broker.queue, delivery.receipt, payload, delivery.broker.now().Add(delay))
	})
}
