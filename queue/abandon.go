package queue

import (
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Abandoner returns an unsettled job without consuming its handler retry budget.
// It is used when infrastructure fails or shutdown races with Reserve.
type Abandoner interface{ Abandon(context.Context) error }

func (d *memoryDelivery) Abandon(ctx context.Context) error {
	return d.settlement.run(func() error {
		if ctx == nil {
			return errors.New("queue: context is nil")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		b := d.broker
		b.mu.Lock()
		defer b.mu.Unlock()
		if !d.item.reserved || d.item.generation != d.generation {
			return ErrLeaseLost
		}
		delete(b.leases, d.item)
		d.item.reserved = false
		d.item.available = b.now()
		b.sequence++
		d.item.sequence = b.sequence
		heap.Push(&b.items, d.item)
		b.signal()
		return nil
	})
}
func (d *redisDelivery) Abandon(ctx context.Context) error {
	return d.settlement.run(func() error {
		payload, err := json.Marshal(d.message)
		if err != nil {
			return err
		}
		return d.broker.store.Retry(ctx, d.broker.queue, d.receipt, payload, d.broker.now())
	})
}
func (d *rabbitDelivery) Abandon(ctx context.Context) error {
	return d.settlement.run(func() error { return d.source.Reject(ctx, true) })
}
func (d *databaseDelivery) Abandon(ctx context.Context) error {
	return d.settlement.run(func() error {
		b, c := d.broker, d.broker.columns
		query := "UPDATE " + b.table + " SET " + c.available + " = " + b.dialect.Placeholder(1) + ", " + c.reservation + " = NULL, " + c.reserved + " = NULL WHERE " + c.id + " = " + b.dialect.Placeholder(2) + " AND " + c.reservation + " = " + b.dialect.Placeholder(3)
		result, err := b.database.ExecContext(ctx, query, time.Now().UnixNano(), d.message.ID, d.token)
		if err != nil {
			return err
		}
		return requireQueueAffected(result)
	})
}
