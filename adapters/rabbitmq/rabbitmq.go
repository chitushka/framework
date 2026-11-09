// Package rabbitmq supplies queue.RabbitChannel using RabbitMQ's maintained SDK.
package rabbitmq

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/chitushka/framework/queue"
	amqp "github.com/rabbitmq/amqp091-go"
)

type Config struct {
	URL              string
	TLSConfig        *tls.Config
	OperationTimeout time.Duration
	MaxQueueBytes    int64
}
type Channel struct {
	config Config
	mu     sync.Mutex
	closed bool
	active map[*wire]struct{}
}
type wire struct {
	connection *amqp.Connection
	channel    *amqp.Channel
	socket     net.Conn
	owner      *Channel
}

func New(config Config) (*Channel, error) {
	if config.URL == "" {
		return nil, errors.New("rabbitmq: URL required")
	}
	if config.OperationTimeout == 0 {
		config.OperationTimeout = 5 * time.Second
	}
	if config.OperationTimeout < 0 {
		return nil, errors.New("rabbitmq: timeout must be positive")
	}
	if config.MaxQueueBytes == 0 {
		config.MaxQueueBytes = 64 << 20
	}
	if config.MaxQueueBytes < 1 {
		return nil, errors.New("rabbitmq: MaxQueueBytes must be positive")
	}
	if _, err := amqp.ParseURI(config.URL); err != nil {
		return nil, errors.New("rabbitmq: invalid URL")
	}
	if config.TLSConfig != nil {
		config.TLSConfig = config.TLSConfig.Clone()
	}
	return &Channel{config: config, active: make(map[*wire]struct{})}, nil
}

func (c *Channel) queueArguments(deadLetterTarget string) amqp.Table {
	arguments := amqp.Table{"x-queue-type": "quorum", "x-overflow": "reject-publish", "x-max-length-bytes": c.config.MaxQueueBytes, "x-delivery-limit": int32(-1)}
	if deadLetterTarget != "" {
		arguments["x-dead-letter-exchange"] = ""
		arguments["x-dead-letter-routing-key"] = deadLetterTarget
		arguments["x-dead-letter-strategy"] = "at-least-once"
	}
	return arguments
}

func (w *wire) close() {
	_ = w.socket.Close()
	w.owner.mu.Lock()
	delete(w.owner.active, w)
	w.owner.mu.Unlock()
}
func (c *Channel) Close() error {
	c.mu.Lock()
	c.closed = true
	wires := make([]*wire, 0, len(c.active))
	for w := range c.active {
		wires = append(wires, w)
	}
	c.mu.Unlock()
	for _, w := range wires {
		w.close()
	}
	return nil
}
func (w *wire) run(ctx context.Context, operation func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, w.owner.config.OperationTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = w.socket.Close() })
	defer stop()
	err := operation(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
func (c *Channel) open(ctx context.Context) (*wire, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, errors.New("rabbitmq: adapter closed")
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.OperationTimeout)
	defer cancel()
	var socket net.Conn
	var stop func() bool
	connection, err := amqp.DialConfig(c.config.URL, amqp.Config{Heartbeat: 5 * time.Second, TLSClientConfig: c.config.TLSConfig, Dial: func(network, addr string) (net.Conn, error) {
		var err error
		socket, err = (&net.Dialer{Timeout: c.config.OperationTimeout}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		stop = context.AfterFunc(ctx, func() { _ = socket.Close() })
		return socket, nil
	}})
	if stop != nil {
		defer stop()
	}
	if err != nil {
		if socket != nil {
			_ = socket.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	w := &wire{connection: connection, socket: socket, owner: c}
	channel, err := connection.Channel()
	if err != nil {
		_ = socket.Close()
		return nil, err
	}
	w.channel = channel
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		_ = socket.Close()
		return nil, errors.New("rabbitmq: adapter closed")
	}
	c.active[w] = struct{}{}
	return w, nil
}

func (w *wire) topology(name string) error {
	if name == "" || len(name) > 200 {
		return errors.New("rabbitmq: queue name must be 1..200 bytes")
	}
	if _, err := w.channel.QueueDeclare(name+".invalid", true, false, false, false, w.owner.queueArguments("")); err != nil {
		return err
	}
	_, err := w.channel.QueueDeclare(name, true, false, false, false, w.owner.queueArguments(name+".invalid"))
	return err
}

func (c *Channel) Publish(ctx context.Context, name string, payload []byte, delay time.Duration) error {
	if delay < 0 || delay > 24*time.Hour || len(payload) > 16<<20 {
		return errors.New("rabbitmq: invalid delay or payload exceeds 16 MiB")
	}
	w, err := c.open(ctx)
	if err != nil {
		return err
	}
	defer w.close()
	return w.run(ctx, func(ctx context.Context) error {
		if err := w.topology(name); err != nil {
			return err
		}
		target := name
		expiration := ""
		if delay > 0 {
			target = name + ".delay"
			expiration = strconv.FormatInt(int64((delay-1)/time.Millisecond)+1, 10)
			if _, err := w.channel.QueueDeclare(target, true, false, false, false, w.owner.queueArguments(name)); err != nil {
				return err
			}
		}
		if err := w.channel.Confirm(false); err != nil {
			return err
		}
		returned := w.channel.NotifyReturn(make(chan amqp.Return, 1))
		confirmation, err := w.channel.PublishWithDeferredConfirmWithContext(ctx, "", target, true, false, amqp.Publishing{DeliveryMode: amqp.Persistent, ContentType: "application/json", Body: payload, Expiration: expiration})
		if err != nil {
			return err
		}
		acked, err := confirmation.WaitContext(ctx)
		if err != nil {
			return err
		}
		if !acked {
			return errors.New("rabbitmq: publish nacked")
		}
		select {
		case <-returned:
			return errors.New("rabbitmq: message unroutable")
		default:
			return nil
		}
	})
}

func (c *Channel) Consume(ctx context.Context, name string, wait time.Duration) (queue.RabbitMessage, error) {
	if wait < 0 {
		return nil, errors.New("rabbitmq: wait cannot be negative")
	}
	w, err := c.open(ctx)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			w.close()
		}
	}()
	if err := w.run(ctx, func(context.Context) error { return w.topology(name) }); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		var delivery amqp.Delivery
		var found bool
		err := w.run(ctx, func(context.Context) error {
			var err error
			delivery, found, err = w.channel.Get(name, false)
			return err
		})
		if err != nil {
			return nil, err
		}
		if found {
			keep = true
			return &message{wire: w, delivery: delivery}, nil
		}
		remaining := time.Until(deadline)
		if wait == 0 || remaining <= 0 {
			return nil, nil
		}
		timer := time.NewTimer(min(remaining, 25*time.Millisecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

type message struct {
	wire     *wire
	delivery amqp.Delivery
	mu       sync.Mutex
	done     bool
}

func (m *message) Body() []byte { return append([]byte(nil), m.delivery.Body...) }
func (m *message) settle(ctx context.Context, reject, requeue bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return queue.ErrSettled
	}
	err := m.wire.run(ctx, func(context.Context) error {
		if reject {
			return m.delivery.Reject(requeue)
		}
		return m.delivery.Ack(false)
	})
	// Closing the connection is also the recovery mechanism if settlement failed.
	m.wire.close()
	if err == nil {
		m.done = true
	}
	return err
}
func (m *message) Ack(ctx context.Context) error { return m.settle(ctx, false, false) }
func (m *message) Reject(ctx context.Context, requeue bool) error {
	return m.settle(ctx, true, requeue)
}

var _ queue.RabbitChannel = (*Channel)(nil)
