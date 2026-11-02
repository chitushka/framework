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
	ErrEmpty   = errors.New("queue: no message available")
	ErrSettled = errors.New("queue: delivery already settled")
)

// Message is the broker-independent serialized job envelope.
type Message struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Payload     json.RawMessage `json:"payload"`
	Attempt     int             `json:"attempt"`
	MaxAttempts int             `json:"maxAttempts"`
	CreatedAt   time.Time       `json:"createdAt"`
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
	broker Broker
	now    func() time.Time
}

func New(broker Broker) (*Queue, error) {
	if broker == nil {
		return nil, errors.New("queue: broker is required")
	}
	return &Queue{broker: broker, now: time.Now}, nil
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
	if err := queue.broker.Enqueue(ctx, message, options.Delay); err != nil {
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
