// Package queue provides persistent background jobs and managed workers.
package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrEmpty     = errors.New("queue: no message available")
	ErrSettled   = errors.New("queue: delivery already settled")
	ErrLeaseLost = errors.New("queue: delivery reservation was lost")
)

// Message is the broker-independent serialized job envelope.
type Message struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Payload     json.RawMessage `json:"payload"`
	Attempt     int             `json:"attempt"`
	MaxAttempts int             `json:"maxAttempts"`
	CreatedAt   time.Time       `json:"createdAt"`
	TraceParent string          `json:"traceparent,omitempty"`
	TraceState  string          `json:"tracestate,omitempty"`
}

// Observer связывает producer/consumer spans без зависимости queue от tracing SDK.
// Методы и возвращённые callbacks должны быть быстрыми и безопасными конкурентно.
type Observer interface {
	StartEnqueue(context.Context, *Message) (context.Context, func(error))
	StartProcess(context.Context, Message) (context.Context, func(error))
}

// Delivery must be acknowledged exactly once after Reserve.
type Delivery interface {
	Message() Message
	Ack(context.Context) error
	Nack(context.Context, time.Duration) error
}

// Broker persists messages and reserves them for one worker at a time.
type Broker interface {
	Enqueue(context.Context, Message, time.Duration) error
	Reserve(context.Context, time.Duration) (Delivery, error)
}

type Options struct {
	Delay       time.Duration
	MaxAttempts int
}

// Queue serializes application payloads into broker messages.
type Queue struct {
	broker   Broker
	now      func() time.Time
	observer Observer
}

func New(broker Broker, observers ...Observer) (*Queue, error) {
	if broker == nil {
		return nil, errors.New("queue: broker is required")
	}
	if len(observers) > 1 || len(observers) == 1 && observers[0] == nil {
		return nil, errors.New("queue: expected at most one non-nil observer")
	}
	result := &Queue{broker: broker, now: time.Now}
	if len(observers) == 1 {
		result.observer = observers[0]
	}
	return result, nil
}

func (queue *Queue) Push(ctx context.Context, name string, payload any, options Options) (string, error) {
	if ctx == nil {
		return "", errors.New("queue: context is nil")
	}
	if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) || len(name) > 255 {
		return "", errors.New("queue: invalid job name")
	}
	if options.Delay < 0 {
		return "", errors.New("queue: delay cannot be negative")
	}
	if options.MaxAttempts == 0 {
		options.MaxAttempts = 3
	}
	if options.MaxAttempts < 1 {
		return "", errors.New("queue: MaxAttempts must be positive")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("queue: encode %q payload: %w", name, err)
	}
	id, err := messageID()
	if err != nil {
		return "", err
	}
	message := Message{ID: id, Name: name, Payload: encoded, MaxAttempts: options.MaxAttempts, CreatedAt: queue.now().UTC()}
	var end func(error)
	if queue.observer != nil {
		ctx, end = queue.observer.StartEnqueue(ctx, &message)
	}
	err = queue.broker.Enqueue(ctx, message, options.Delay)
	if end != nil {
		end(err)
	}
	if err != nil {
		return "", fmt.Errorf("queue: enqueue %q: %w", name, err)
	}
	return id, nil
}

func messageID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("queue: generate message ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func validateMessage(message Message) error {
	if len(message.ID) > 64 || len(message.Name) > 255 || len(message.Payload) > 16<<20 || len(message.TraceParent) > 512 || len(message.TraceState) > 512 {
		return errors.New("queue: message identifiers/payload exceed limits")
	}
	if message.ID == "" || message.Name == "" {
		return errors.New("queue: message ID and name are required")
	}
	if message.MaxAttempts < 1 || message.Attempt < 0 {
		return errors.New("queue: invalid message attempts")
	}
	if !json.Valid(message.Payload) {
		return errors.New("queue: payload is not valid JSON")
	}
	return nil
}

type settlement struct {
	mu   sync.Mutex
	done bool
}

func (state *settlement) run(operation func() error) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.done {
		return ErrSettled
	}
	err := operation()
	if err == nil {
		state.done = true
	}
	return err
}
