package queue

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chitushka/framework/app"
)

func TestMemoryBrokerLifecycleAndDelay(t *testing.T) {
	broker := NewMemoryBroker()
	producer, _ := New(broker)
	ctx := context.Background()
	id, err := producer.Push(ctx, "email.send", map[string]string{"to": "a@example.com"}, Options{MaxAttempts: 2})
	if err != nil || len(id) != 32 {
		t.Fatalf("push = %q, %v", id, err)
	}
	delivery, err := broker.Reserve(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	message := delivery.Message()
	if message.Name != "email.send" || message.Attempt != 0 {
		t.Fatalf("message = %#v", message)
	}
	message.Payload[0] = 'x'
	if !json.Valid(delivery.Message().Payload) {
		t.Fatal("delivery payload was not copied")
	}
	if err := delivery.Nack(ctx, 0); err != nil {
		t.Fatal(err)
	}
	retry, err := broker.Reserve(ctx, time.Second)
	if err != nil || retry.Message().Attempt != 1 {
		t.Fatalf("retry = %#v, %v", retry, err)
	}
	if err := retry.Ack(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Reserve(ctx, 0); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty error = %v", err)
	}
	if _, err := producer.Push(ctx, "delayed", struct{}{}, Options{Delay: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	delayed, err := broker.Reserve(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < 10*time.Millisecond {
		t.Fatal("delayed message reserved too early")
	}
	_ = delayed.Ack(ctx)
	_, _ = producer.Push(ctx, "ordered", 1, Options{})
	_, _ = producer.Push(ctx, "ordered", 2, Options{})
	one, _ := broker.Reserve(ctx, 0)
	two, _ := broker.Reserve(ctx, 0)
	_ = one.Ack(ctx)
	_ = two.Ack(ctx)
}

func TestWorkerRetriesTypedHandlerAndLifecycleModule(t *testing.T) {
	broker := NewMemoryBroker()
	producer, _ := New(broker)
	var calls atomic.Int32
	processed := make(chan string, 1)
	failures := make(chan Failure, 1)
	worker, err := NewWorker(broker, WorkerConfig{Concurrency: 2, PollInterval: time.Millisecond, RetryDelay: func(int) time.Duration { return 0 }, OnFailure: func(failure Failure) { failures <- failure }})
	if err != nil {
		t.Fatal(err)
	}
	type payload struct {
		Name string `json:"name"`
	}
	if err := worker.RegisterHandler("report", JSONHandler(func(_ context.Context, value payload) error {
		if calls.Add(1) == 1 {
			return errors.New("temporary")
		}
		processed <- value.Name
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	application := app.New(nil, nil)
	if err := application.AddModule(worker); err != nil {
		t.Fatal(err)
	}
	if _, err := producer.Push(context.Background(), "report", payload{Name: "daily"}, Options{MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	if err := application.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-processed:
		if value != "daily" {
			t.Fatalf("value = %q", value)
		}
	case <-time.After(time.Second):
		t.Fatal("job was not processed")
	}
	failure := <-failures
	if failure.Final || failure.Message.Attempt != 0 {
		t.Fatalf("failure = %#v", failure)
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if _, err := application.Components.Get(context.Background(), "queue.worker"); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerDiscardsAfterMaxAttempts(t *testing.T) {
	broker := NewMemoryBroker()
	producer, _ := New(broker)
	final := make(chan Failure, 1)
	worker, _ := NewWorker(broker, WorkerConfig{PollInterval: time.Millisecond, RetryDelay: func(int) time.Duration { return 0 }, OnFailure: func(f Failure) {
		if f.Final {
			final <- f
		}
	}})
	_ = worker.RegisterHandler("broken", func(context.Context, json.RawMessage) error { return errors.New("broken") })
	_, _ = producer.Push(context.Background(), "broken", nil, Options{MaxAttempts: 2})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case failure := <-final:
		if failure.Message.Attempt != 1 {
			t.Fatalf("attempt = %d", failure.Message.Attempt)
		}
	case <-time.After(time.Second):
		t.Fatal("final failure not reported")
	}
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Reserve(context.Background(), 0); !errors.Is(err, ErrEmpty) {
		t.Fatalf("message not discarded: %v", err)
	}
}

func TestValidation(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("nil broker accepted")
	}
	broker := NewMemoryBroker()
	producer, _ := New(broker)
	if _, err := producer.Push(nil, "job", nil, Options{}); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, err := producer.Push(context.Background(), "", nil, Options{}); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := producer.Push(context.Background(), "job", nil, Options{MaxAttempts: -1}); err == nil {
		t.Fatal("attempts accepted")
	}
	if _, err := producer.Push(context.Background(), "job", make(chan int), Options{}); err == nil {
		t.Fatal("unencodable payload accepted")
	}
	if err := broker.Enqueue(context.Background(), Message{}, 0); err == nil {
		t.Fatal("invalid message accepted")
	}
	if _, err := broker.Reserve(context.Background(), -1); err == nil {
		t.Fatal("negative wait accepted")
	}
	if _, err := broker.Reserve(nil, 0); err == nil {
		t.Fatal("nil reserve context accepted")
	}
	if err := broker.Enqueue(nil, Message{}, 0); err == nil {
		t.Fatal("nil enqueue context accepted")
	}
	if err := broker.Enqueue(context.Background(), Message{}, -1); err == nil {
		t.Fatal("negative enqueue delay accepted")
	}
	if _, err := NewWorker(nil, WorkerConfig{}); err == nil {
		t.Fatal("nil worker broker accepted")
	}
	worker, _ := NewWorker(broker, WorkerConfig{})
	if err := worker.Start(context.Background()); err == nil {
		t.Fatal("worker without handlers started")
	}
	if err := worker.RegisterHandler("", func(context.Context, json.RawMessage) error { return nil }); err == nil {
		t.Fatal("empty handler accepted")
	}
	if err := worker.RegisterHandler("job", func(context.Context, json.RawMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := worker.RegisterHandler("job", func(context.Context, json.RawMessage) error { return nil }); err == nil {
		t.Fatal("duplicate handler accepted")
	}
	if err := worker.Shutdown(nil); err == nil {
		t.Fatal("nil shutdown context accepted")
	}
	if _, err := NewWorker(broker, WorkerConfig{Concurrency: -1}); err == nil {
		t.Fatal("negative concurrency accepted")
	}
	if _, err := NewWorker(broker, WorkerConfig{PollInterval: -1}); err == nil {
		t.Fatal("negative polling accepted")
	}
	if _, err := NewWorker(broker, WorkerConfig{ShutdownTimeout: -1}); err == nil {
		t.Fatal("negative shutdown accepted")
	}
	typed := JSONHandler(func(context.Context, struct {
		Value int `json:"value"`
	}) error {
		return nil
	})
	if err := typed(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

type fakeRedisStore struct {
	mu        sync.Mutex
	payload   []byte
	receipt   string
	available time.Time
}

func (store *fakeRedisStore) Push(_ context.Context, _ string, payload []byte, available time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.payload = append([]byte(nil), payload...)
	store.available = available
	return nil
}
func (store *fakeRedisStore) Reserve(_ context.Context, _ string, now time.Time, _ time.Duration) ([]byte, string, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.payload == nil || store.available.After(now) {
		return nil, "", false, nil
	}
	payload := store.payload
	store.payload = nil
	store.receipt = "receipt"
	return payload, store.receipt, true, nil
}
func (store *fakeRedisStore) Ack(context.Context, string, string) error {
	store.mu.Lock()
	store.receipt = ""
	store.mu.Unlock()
	return nil
}
func (store *fakeRedisStore) Retry(_ context.Context, _ string, _ string, payload []byte, available time.Time) error {
	return store.Push(context.Background(), "", payload, available)
}

type fakeRabbitMessage struct {
	body            []byte
	acked, rejected bool
}

func (message *fakeRabbitMessage) Body() []byte              { return append([]byte(nil), message.body...) }
func (message *fakeRabbitMessage) Ack(context.Context) error { message.acked = true; return nil }
func (message *fakeRabbitMessage) Reject(context.Context, bool) error {
	message.rejected = true
	return nil
}

type fakeRabbitChannel struct{ message *fakeRabbitMessage }

func (channel *fakeRabbitChannel) Publish(_ context.Context, _ string, payload []byte, _ time.Duration) error {
	channel.message = &fakeRabbitMessage{body: append([]byte(nil), payload...)}
	return nil
}
func (channel *fakeRabbitChannel) Consume(context.Context, string, time.Duration) (RabbitMessage, error) {
	if channel.message == nil {
		return nil, nil
	}
	message := channel.message
	channel.message = nil
	return message, nil
}

func TestRedisAndRabbitMQAdapters(t *testing.T) {
	ctx := context.Background()
	message := Message{ID: "1", Name: "job", Payload: json.RawMessage(`{"x":1}`), MaxAttempts: 2, CreatedAt: time.Now()}
	redisStore := &fakeRedisStore{}
	redis, err := NewRedisBroker(RedisConfig{Store: redisStore, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := redis.Enqueue(ctx, message, 0); err != nil {
		t.Fatal(err)
	}
	delivery, err := redis.Reserve(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := delivery.Nack(ctx, 0); err != nil {
		t.Fatal(err)
	}
	retry, err := redis.Reserve(ctx, time.Second)
	if err != nil || retry.Message().Attempt != 1 {
		t.Fatalf("Redis retry = %#v, %v", retry, err)
	}
	_ = retry.Ack(ctx)
	rabbitChannel := &fakeRabbitChannel{}
	rabbit, err := NewRabbitMQBroker(RabbitMQConfig{Channel: rabbitChannel})
	if err != nil {
		t.Fatal(err)
	}
	if err := rabbit.Enqueue(ctx, message, 0); err != nil {
		t.Fatal(err)
	}
	rabbitDelivery, err := rabbit.Reserve(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := rabbitDelivery.Nack(ctx, 0); err != nil {
		t.Fatal(err)
	}
	rabbitRetry, err := rabbit.Reserve(ctx, 0)
	if err != nil || rabbitRetry.Message().Attempt != 1 {
		t.Fatalf("Rabbit retry = %#v, %v", rabbitRetry, err)
	}
	_ = rabbitRetry.Ack(ctx)
}

func TestAdapterValidationAndCorruptMessages(t *testing.T) {
	if _, err := NewRedisBroker(RedisConfig{}); err == nil {
		t.Fatal("nil Redis store accepted")
	}
	if _, err := NewRedisBroker(RedisConfig{Store: &fakeRedisStore{}, ReservationTimeout: -1}); err == nil {
		t.Fatal("negative Redis reservation accepted")
	}
	if _, err := NewRedisBroker(RedisConfig{Store: &fakeRedisStore{}, PollInterval: -1}); err == nil {
		t.Fatal("negative Redis poll accepted")
	}
	store := &fakeRedisStore{payload: []byte("broken"), available: time.Now()}
	redis, _ := NewRedisBroker(RedisConfig{Store: store, PollInterval: time.Millisecond})
	if _, err := redis.Reserve(context.Background(), 0); err == nil {
		t.Fatal("corrupt Redis message accepted")
	}
	if _, err := redis.Reserve(context.Background(), -1); err == nil {
		t.Fatal("negative Redis wait accepted")
	}
	if _, err := redis.Reserve(nil, 0); err == nil {
		t.Fatal("nil Redis context accepted")
	}
	if _, err := NewRabbitMQBroker(RabbitMQConfig{}); err == nil {
		t.Fatal("nil RabbitMQ channel accepted")
	}
	channel := &fakeRabbitChannel{message: &fakeRabbitMessage{body: []byte("broken")}}
	rabbit, _ := NewRabbitMQBroker(RabbitMQConfig{Channel: channel})
	if _, err := rabbit.Reserve(context.Background(), 0); err == nil {
		t.Fatal("corrupt RabbitMQ message accepted")
	}
	if _, err := rabbit.Reserve(context.Background(), -1); err == nil {
		t.Fatal("negative RabbitMQ wait accepted")
	}
	if _, err := rabbit.Reserve(nil, 0); err == nil {
		t.Fatal("nil Rabbit context accepted")
	}
	if err := rabbit.Enqueue(context.Background(), Message{}, 0); err == nil {
		t.Fatal("invalid Rabbit message accepted")
	}
	if _, err := rabbit.Reserve(context.Background(), 0); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty RabbitMQ = %v", err)
	}
	valid := Message{ID: "valid", Name: "job", Payload: json.RawMessage(`null`), MaxAttempts: 2, CreatedAt: time.Now()}
	if err := redis.Enqueue(nil, valid, 0); err == nil {
		t.Fatal("nil Redis context accepted")
	}
	if err := redis.Enqueue(context.Background(), valid, -1); err == nil {
		t.Fatal("negative Redis delay accepted")
	}
	if err := redis.Enqueue(context.Background(), valid, 0); err != nil {
		t.Fatal(err)
	}
	redisDelivery, _ := redis.Reserve(context.Background(), time.Second)
	if err := redisDelivery.Nack(context.Background(), -1); err == nil {
		t.Fatal("negative Redis retry accepted")
	}
	if err := rabbit.Enqueue(nil, valid, 0); err == nil {
		t.Fatal("nil Rabbit context accepted")
	}
	if err := rabbit.Enqueue(context.Background(), valid, -1); err == nil {
		t.Fatal("negative Rabbit delay accepted")
	}
	if err := rabbit.Enqueue(context.Background(), valid, 0); err != nil {
		t.Fatal(err)
	}
	rabbitDelivery, _ := rabbit.Reserve(context.Background(), 0)
	if err := rabbitDelivery.Nack(context.Background(), -1); err == nil {
		t.Fatal("negative Rabbit retry accepted")
	}
}

type failingBroker struct{ err error }

func (broker failingBroker) Enqueue(context.Context, Message, time.Duration) error { return broker.err }
func (broker failingBroker) Reserve(context.Context, time.Duration) (Delivery, error) {
	return nil, broker.err
}

func TestWorkerReportsFatalBrokerError(t *testing.T) {
	broken := errors.New("broker unavailable")
	worker, _ := NewWorker(failingBroker{err: broken}, WorkerConfig{PollInterval: time.Millisecond})
	_ = worker.RegisterHandler("job", func(context.Context, json.RawMessage) error { return nil })
	if worker.ID() != "queue.worker" {
		t.Fatalf("ID = %q", worker.ID())
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-worker.Errors():
		if !errors.Is(err, broken) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fatal error not reported")
	}
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
