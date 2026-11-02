package queue

import (
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

type memoryItem struct {
	message   Message
	available time.Time
	sequence  uint64
	index     int
	reserved  bool
}
type memoryHeap []*memoryItem

func (items memoryHeap) Len() int { return len(items) }
func (items memoryHeap) Less(i, j int) bool {
	if items[i].available.Equal(items[j].available) {
		return items[i].sequence < items[j].sequence
	}
	return items[i].available.Before(items[j].available)
}
func (items memoryHeap) Swap(i, j int) {
	items[i], items[j] = items[j], items[i]
	items[i].index = i
	items[j].index = j
}
func (items *memoryHeap) Push(value any) {
	item := value.(*memoryItem)
	item.index = len(*items)
	*items = append(*items, item)
}
func (items *memoryHeap) Pop() any {
	old := *items
	item := old[len(old)-1]
	item.index = -1
	*items = old[:len(old)-1]
	return item
}

// MemoryBroker is a concurrency-safe delayed queue for one process.
type MemoryBroker struct {
	mu       sync.Mutex
	items    memoryHeap
	sequence uint64
	wake     chan struct{}
	now      func() time.Time
}

func NewMemoryBroker() *MemoryBroker {
	broker := &MemoryBroker{wake: make(chan struct{}, 1), now: time.Now}
	heap.Init(&broker.items)
	return broker
}

func (broker *MemoryBroker) Enqueue(ctx context.Context, message Message, delay time.Duration) error {
	if ctx == nil {
		return errors.New("queue: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay < 0 {
		return errors.New("queue: delay cannot be negative")
	}
	if err := validateMessage(message); err != nil {
		return err
	}
	broker.mu.Lock()
	broker.sequence++
	heap.Push(&broker.items, &memoryItem{message: cloneMessage(message), available: broker.now().Add(delay), sequence: broker.sequence})
	broker.mu.Unlock()
	broker.signal()
	return nil
}
func (broker *MemoryBroker) Reserve(ctx context.Context, wait time.Duration) (Delivery, error) {
	if ctx == nil {
		return nil, errors.New("queue: context is nil")
	}
	if wait < 0 {
		return nil, errors.New("queue: wait cannot be negative")
	}
	deadline := broker.now().Add(wait)
	for {
		broker.mu.Lock()
		now := broker.now()
		if len(broker.items) > 0 && !broker.items[0].available.After(now) {
			item := heap.Pop(&broker.items).(*memoryItem)
			item.reserved = true
			broker.mu.Unlock()
			return &memoryDelivery{broker: broker, item: item}, nil
		}
		var until time.Duration
		if len(broker.items) > 0 {
			until = broker.items[0].available.Sub(now)
		}
		broker.mu.Unlock()
		remaining := deadline.Sub(now)
		if wait == 0 || remaining <= 0 {
			return nil, ErrEmpty
		}
		if until <= 0 || until > remaining {
			until = remaining
		}
		timer := time.NewTimer(until)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-broker.wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}
func (broker *MemoryBroker) signal() {
	select {
	case broker.wake <- struct{}{}:
	default:
	}
}

type memoryDelivery struct {
	broker     *MemoryBroker
	item       *memoryItem
	settlement settlement
}

func (delivery *memoryDelivery) Message() Message { return cloneMessage(delivery.item.message) }
func (delivery *memoryDelivery) Ack(ctx context.Context) error {
	return delivery.settlement.run(func() error {
		if ctx == nil {
			return errors.New("queue: context is nil")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		delivery.broker.mu.Lock()
		delivery.item.reserved = false
		delivery.broker.mu.Unlock()
		return nil
	})
}
func (delivery *memoryDelivery) Nack(ctx context.Context, delay time.Duration) error {
	return delivery.settlement.run(func() error {
		if ctx == nil {
			return errors.New("queue: context is nil")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if delay < 0 {
			return errors.New("queue: delay cannot be negative")
		}
		delivery.broker.mu.Lock()
		delivery.item.message.Attempt++
		delivery.item.reserved = false
		delivery.item.available = delivery.broker.now().Add(delay)
		delivery.broker.sequence++
		delivery.item.sequence = delivery.broker.sequence
		heap.Push(&delivery.broker.items, delivery.item)
		delivery.broker.mu.Unlock()
		delivery.broker.signal()
		return nil
	})
}
func cloneMessage(message Message) Message {
	message.Payload = append(json.RawMessage(nil), message.Payload...)
	return message
}
