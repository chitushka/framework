package integration_test

import (
	"bytes"
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"

	rabbitadapter "github.com/chitushka/framework/adapters/rabbitmq"
	redisadapter "github.com/chitushka/framework/adapters/redis"
	"github.com/chitushka/framework/queue"
	amqp "github.com/rabbitmq/amqp091-go"
	sdk "github.com/redis/go-redis/v9"
)

func TestRedisQueueReconnectAndMalformedQuarantine(t *testing.T) {
	ctx := testContext(t)
	options := &sdk.Options{Addr: required(t, "REDIS_ADDRESS"), Password: required(t, "REDIS_PASSWORD"), DB: 1, PoolSize: 1, MaxActiveConns: 1}
	client := redisadapter.NewClient(options)
	defer func() { _ = client.Close() }()
	store, err := redisadapter.New(client, unique())
	check(t, err)
	name := unique()
	defer func() { _ = store.Purge(context.Background(), name) }()
	broker, err := queue.NewRedisBroker(queue.RedisConfig{Store: store, Queue: name, PollInterval: time.Millisecond})
	check(t, err)
	producer, err := queue.New(broker)
	check(t, err)
	id, err := producer.Push(ctx, "job", nil, queue.Options{})
	check(t, err)
	connectionID, err := client.ClientID(ctx).Result()
	check(t, err)
	killer := redisadapter.NewClient(options)
	defer func() { _ = killer.Close() }()
	_, err = killer.ClientKillByFilter(ctx, "ID", strconv.FormatInt(connectionID, 10)).Result()
	check(t, err)
	delivery, err := broker.Reserve(ctx, time.Second)
	if err != nil {
		delivery, err = broker.Reserve(ctx, time.Second)
	}
	check(t, err)
	if delivery.Message().ID != id {
		t.Fatal("accepted message lost after disconnect")
	}
	check(t, delivery.Ack(ctx))
	payload := []byte("{malformed")
	check(t, store.Push(ctx, name, payload, time.Now()))
	if _, err := broker.Reserve(ctx, time.Second); err == nil {
		t.Fatal("malformed job accepted")
	}
	records, err := store.InvalidMessages(ctx, name, 10)
	check(t, err)
	if len(records) != 1 || !bytes.Equal(records[0].Payload, payload) {
		t.Fatalf("poison discarded: %+v", records)
	}
}

func TestRabbitPublishOutageReconnectAndMalformedDLQ(t *testing.T) {
	ctx := testContext(t)
	original, err := url.Parse(required(t, "AMQP_URL"))
	check(t, err)
	address, _, reset := serviceRelay(t, original.Host, false)
	original.Host = address
	adapter, err := rabbitadapter.New(rabbitadapter.Config{URL: original.String(), OperationTimeout: 3 * time.Second})
	check(t, err)
	defer func() { _ = adapter.Close() }()
	name := unique()
	t.Cleanup(func() { cleanupRabbitQueue(t, name) })
	reset.Store(true)
	if err := adapter.Publish(ctx, name, []byte("{}"), 0); err == nil {
		t.Fatal("unconfirmed outage publish reported success")
	}
	reset.Store(false)
	broker, err := queue.NewRabbitMQBroker(queue.RabbitMQConfig{Channel: adapter, Queue: name})
	check(t, err)
	queueLifecycle(t, broker)
	payload := []byte("{malformed")
	check(t, adapter.Publish(ctx, name, payload, 0))
	if _, err := broker.Reserve(ctx, time.Second); err == nil {
		t.Fatal("malformed message accepted")
	}
	connection, err := amqp.Dial(required(t, "AMQP_URL"))
	check(t, err)
	defer func() { _ = connection.Close() }()
	ch, err := connection.Channel()
	check(t, err)
	defer func() { _ = ch.Close() }()
	var delivery amqp.Delivery
	var found bool
	for !found {
		delivery, found, err = ch.Get(name+".invalid", false)
		check(t, err)
		if !found {
			select {
			case <-ctx.Done():
				t.Fatal("malformed DLQ did not receive message")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	if !found || !bytes.Equal(delivery.Body, payload) {
		t.Fatal("malformed message lost instead of quarantined")
	}
	check(t, delivery.Ack(false))
}
