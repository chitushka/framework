package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var ErrDeadLetterRequired = errors.New("queue: final failure requires durable dead-letter storage")

type FailedJob struct {
	Message  Message   `json:"message"`
	Error    string    `json:"error"`
	FailedAt time.Time `json:"failedAt"`
}

// Save must not return nil until the record is durably retained. Repeated Save
// for the same message ID must be safe after a crash between Save and Ack.
type DeadLetterStore interface {
	Save(context.Context, FailedJob) error
	Get(context.Context, string) (FailedJob, error)
	List(context.Context, int) ([]FailedJob, error)
	Delete(context.Context, string) error
}

// FileDeadLetters persists atomic JSON records on an application-owned persistent
// volume. Call Close at shutdown. Replicas need shared storage or another Store.
type FileDeadLetters struct {
	root      *os.Root
	directory string
}

func NewFileDeadLetters(directory string) (*FileDeadLetters, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("queue: dead-letter directory required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	return &FileDeadLetters{root, absolute}, nil
}
func (s *FileDeadLetters) Close() error { return s.root.Close() }
func recordPath(id string) string {
	hash := sha256.Sum256([]byte(id))
	return hex.EncodeToString(hash[:]) + ".json"
}
func (s *FileDeadLetters) Save(ctx context.Context, record FailedJob) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateMessage(record.Message); err != nil {
		return err
	}
	if len(record.Error) > 4096 {
		record.Error = record.Error[:4096]
	}
	if record.FailedAt.IsZero() {
		record.FailedAt = time.Now().UTC()
	}
	if previous, err := s.Get(ctx, record.Message.ID); err == nil && previous.Message.ID == record.Message.ID {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(content) > 24<<20 {
		return errors.New("queue: dead-letter record exceeds 24 MiB")
	}
	id, err := messageID()
	if err != nil {
		return err
	}
	temporary := ".pending-" + id
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = s.root.Remove(temporary) }()
	_, writeErr := file.Write(content)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.root.Rename(temporary, recordPath(record.Message.ID)); err != nil {
		return err
	}
	// File data is synced on every platform. Unix also syncs the directory entry;
	// Windows rename durability follows the filesystem/volume's own guarantees.
	if runtime.GOOS != "windows" {
		directory, err := s.root.Open(".")
		if err != nil {
			return err
		}
		defer func() { _ = directory.Close() }()
		if err := directory.Sync(); err != nil {
			return err
		}
	}
	return nil
}
func (s *FileDeadLetters) Get(ctx context.Context, id string) (FailedJob, error) {
	var record FailedJob
	if err := ctx.Err(); err != nil {
		return record, err
	}
	file, err := s.root.Open(recordPath(id))
	if err != nil {
		return record, err
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(io.LimitReader(file, 24<<20))
	if err := decoder.Decode(&record); err != nil {
		return record, err
	}
	if record.Message.ID != id {
		return FailedJob{}, errors.New("queue: dead-letter identity mismatch")
	}
	return record, nil
}
func (s *FileDeadLetters) List(ctx context.Context, limit int) ([]FailedJob, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("queue: list limit must be 1..1000")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory, err := s.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	records := make([]FailedJob, 0, limit)
	for len(records) < limit {
		entries, err := directory.ReadDir(min(100, limit-len(records)))
		if err != nil && err != io.EOF {
			return nil, err
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			file, openErr := s.root.Open(entry.Name())
			if openErr != nil {
				return nil, openErr
			}
			var record FailedJob
			decodeErr := json.NewDecoder(io.LimitReader(file, 24<<20)).Decode(&record)
			_ = file.Close()
			if decodeErr != nil {
				return nil, decodeErr
			}
			if recordPath(record.Message.ID) != entry.Name() {
				return nil, errors.New("queue: invalid dead-letter file")
			}
			records = append(records, record)
		}
		if err == io.EOF {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return records, nil
}
func (s *FileDeadLetters) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.root.Remove(recordPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Replay enqueues a fresh ID/attempt budget and retains the original audit record.
// A network-ambiguous enqueue can duplicate work; idempotency is mandatory.
func Replay(ctx context.Context, store DeadLetterStore, id string, broker Broker) (string, error) {
	if store == nil || broker == nil {
		return "", errors.New("queue: store and broker required")
	}
	record, err := store.Get(ctx, id)
	if err != nil {
		return "", err
	}
	next, err := messageID()
	if err != nil {
		return "", err
	}
	message := cloneMessage(record.Message)
	message.ID = next
	message.Attempt = 0
	message.CreatedAt = time.Now().UTC()
	if err := broker.Enqueue(ctx, message, 0); err != nil {
		return "", fmt.Errorf("queue: replay enqueue: %w", err)
	}
	return next, nil
}
