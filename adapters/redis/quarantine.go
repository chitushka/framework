package redis

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type InvalidMessage struct {
	Payload []byte `json:"payload"`
	Reason  string `json:"reason"`
}

func (s *Store) Quarantine(ctx context.Context, name, receipt string, payload []byte, reason string) error {
	if len(reason) > 4096 {
		reason = reason[:4096]
	}
	body, err := json.Marshal(InvalidMessage{payload, reason})
	if err != nil {
		return err
	}
	return s.settle(ctx, name, receipt, "quarantine", body, time.Time{})
}
func (s *Store) InvalidMessages(ctx context.Context, name string, limit int) ([]InvalidMessage, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("redis: limit must be 1..1000")
	}
	ctx, cancel := budget(ctx)
	defer cancel()
	keys := s.queueKeys(name)
	var cursor uint64
	records := make([]InvalidMessage, 0, limit)
	for {
		values, next, err := s.client.HScan(ctx, keys[4], cursor, "*", int64(limit)).Result()
		if err != nil {
			return nil, err
		}
		for i := 1; i < len(values) && len(records) < limit; i += 2 {
			var record InvalidMessage
			if err := json.Unmarshal([]byte(values[i]), &record); err != nil {
				return nil, err
			}
			records = append(records, record)
		}
		cursor = next
		if cursor == 0 || len(records) >= limit {
			return records, nil
		}
	}
}
