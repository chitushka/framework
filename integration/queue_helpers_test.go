package integration_test

import (
	"context"
	rabbitadapter "github.com/chitushka/framework/adapters/rabbitmq"
	redisadapter "github.com/chitushka/framework/adapters/redis"
	"github.com/chitushka/framework/queue"
	sdk "github.com/redis/go-redis/v9"
	"os"
	"testing"
	"time"
)

func persistentBroker(t *testing.T, kind, name, namespace string) (queue.Broker, func()) {
	t.Helper()
	if kind == "redis" {
		client := redisadapter.NewClient(&sdk.Options{Addr: required(t, "REDIS_ADDRESS"), Password: required(t, "REDIS_PASSWORD"), DB: 1})
		store, err := redisadapter.New(client, namespace)
		check(t, err)
		broker, err := queue.NewRedisBroker(queue.RedisConfig{Store: store, Queue: name, ReservationTimeout: 5 * time.Second, PollInterval: 5 * time.Millisecond})
		check(t, err)
		return broker, func() { _ = store.Purge(context.Background(), name); _ = client.Close() }
	}
	adapter, err := rabbitadapter.New(rabbitadapter.Config{URL: required(t, "AMQP_URL")})
	check(t, err)
	broker, err := queue.NewRabbitMQBroker(queue.RabbitMQConfig{Channel: adapter, Queue: name})
	check(t, err)
	return broker, func() { _ = adapter.Close() }
}

func persistentQueueChild(t *testing.T, mode string) {
	if mode != "die" {
		t.Fatal("unexpected queue helper mode")
	}
	broker, closeBroker := persistentBroker(t, required(t, "CHITU_QUEUE_KIND"), required(t, "CHITU_QUEUE_TABLE"), required(t, "CHITU_QUEUE_NAMESPACE"))
	defer closeBroker()
	delivery, err := broker.Reserve(testContext(t), time.Second)
	check(t, err)
	check(t, os.WriteFile(required(t, "CHITU_QUEUE_READY"), []byte(delivery.Message().ID), 0o600))
	select {}
}
