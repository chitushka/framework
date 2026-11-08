package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RabbitMessage abstracts a delivery from an AMQP client library.
type RabbitMessage interface {
	Body() []byte
	Ack(context.Context) error
	Reject(context.Context, bool) error
}

// RabbitChannel is the minimal adapter required from an AMQP 0-9-1 client.
// Publish is responsible for queue declaration and delayed-message routing.
type RabbitChannel interface {
	Publish(context.Context, string, []byte, time.Duration) error
	Consume(context.Context, string, time.Duration) (RabbitMessage, error)
}
type RabbitMQConfig struct {
	Channel RabbitChannel
	Queue   string
}
type RabbitMQBroker struct {
	channel RabbitChannel
	queue   string
}

func NewRabbitMQBroker(config RabbitMQConfig) (*RabbitMQBroker, error) {
	if config.Channel == nil {
		return nil, errors.New("queue: RabbitMQ channel is required")
	}
	if config.Queue == "" {
		config.Queue = "default"
	}
	return &RabbitMQBroker{channel: config.Channel, queue: config.Queue}, nil
}
func (broker *RabbitMQBroker) Enqueue(ctx context.Context, message Message, delay time.Duration) error {
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
		return err
	}
	if err := broker.channel.Publish(ctx, broker.queue, payload, delay); err != nil {
		return fmt.Errorf("queue: RabbitMQ publish: %w", err)
	}
	return nil
}
func (broker *RabbitMQBroker) Reserve(ctx context.Context, wait time.Duration) (Delivery, error) {
	if ctx == nil {
		return nil, errors.New("queue: context is nil")
	}
	if wait < 0 {
		return nil, errors.New("queue: wait cannot be negative")
	}
	source, err := broker.channel.Consume(ctx, broker.queue, wait)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, ErrEmpty
	}
	var message Message
	if err := json.Unmarshal(source.Body(), &message); err != nil {
		_ = source.Reject(context.WithoutCancel(ctx), false)
		return nil, fmt.Errorf("queue: decode RabbitMQ message: %w", err)
	}
	if err := validateMessage(message); err != nil {
		_ = source.Reject(context.WithoutCancel(ctx), false)
		return nil, err
	}
	return &rabbitDelivery{broker: broker, source: source, message: message}, nil
}

type rabbitDelivery struct {
	broker     *RabbitMQBroker
	source     RabbitMessage
	message    Message
	settlement settlement
}

func (delivery *rabbitDelivery) Message() Message { return cloneMessage(delivery.message) }
func (delivery *rabbitDelivery) Ack(ctx context.Context) error {
	return delivery.settlement.run(func() error {
		if ctx == nil {
			return errors.New("queue: context is nil")
		}
		return delivery.source.Ack(ctx)
	})
}
func (delivery *rabbitDelivery) Nack(ctx context.Context, delay time.Duration) error {
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
		if err := delivery.broker.channel.Publish(ctx, delivery.broker.queue, payload, delay); err != nil {
			return err
		}
		return delivery.source.Ack(ctx)
	})
}
