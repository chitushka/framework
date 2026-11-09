package integration_test

import (
	"context"
	"encoding/json"
	rabbitadapter "github.com/chitushka/framework/adapters/rabbitmq"
	"github.com/chitushka/framework/queue"
	amqp "github.com/rabbitmq/amqp091-go"
	"testing"
	"time"
)

// A classic/default DLX would lose the expired message while the target is absent.
// Quorum at-least-once DLX must retain it until the recreated target confirms it.
func TestRabbitDelayRetainsMessageWhileTargetUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	name := unique()
	adapter, err := rabbitadapter.New(rabbitadapter.Config{URL: required(t, "AMQP_URL")})
	check(t, err)
	defer func() { _ = adapter.Close() }()
	t.Cleanup(func() { cleanupRabbitQueue(t, name) })
	connection, err := amqp.Dial(required(t, "AMQP_URL"))
	check(t, err)
	defer func() { _ = connection.Close() }()
	ch, err := connection.Channel()
	check(t, err)
	defer func() { _ = ch.Close() }()
	message := queue.Message{ID: unique(), Name: "job", Payload: json.RawMessage(`null`), MaxAttempts: 3, CreatedAt: time.Now()}
	body, err := json.Marshal(message)
	check(t, err)
	check(t, adapter.Publish(ctx, name, body, 2*time.Second))
	count, err := ch.QueueDelete(name, false, false, false)
	check(t, err)
	if count != 0 {
		t.Fatal("fixture deleted a message already delivered before target outage")
	}
	timer := time.NewTimer(2100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	broker, err := queue.NewRabbitMQBroker(queue.RabbitMQConfig{Channel: adapter, Queue: name})
	check(t, err)
	delivery, err := broker.Reserve(ctx, 45*time.Second)
	check(t, err)
	if delivery.Message().ID != message.ID {
		t.Fatal("accepted delayed job lost during target outage")
	}
	check(t, delivery.Ack(ctx))
}
