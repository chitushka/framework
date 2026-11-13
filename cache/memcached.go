package cache

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// MemcachedConfig configures the built-in memcached text-protocol backend.
type MemcachedConfig struct {
	Address       string
	KeyPrefix     string
	DialTimeout   time.Duration
	IOTimeout     time.Duration
	TLSConfig     *tls.Config
	MaxValueBytes int
}

type MemcachedBackend struct{ config MemcachedConfig }

func NewMemcachedBackend(config MemcachedConfig) (*MemcachedBackend, error) {
	if strings.TrimSpace(config.Address) == "" {
		return nil, errors.New("cache: Memcached address is required")
	}
	if config.DialTimeout < 0 || config.IOTimeout < 0 {
		return nil, errors.New("cache: Memcached timeouts cannot be negative")
	}
	if len(config.KeyPrefix) > 179 || strings.IndexFunc(config.KeyPrefix, func(character rune) bool {
		return character <= ' ' || character == 127
	}) >= 0 {
		return nil, errors.New("cache: Memcached key prefix is invalid")
	}
	if config.DialTimeout == 0 {
		config.DialTimeout = 5 * time.Second
	}
	if config.IOTimeout == 0 {
		config.IOTimeout = 5 * time.Second
	}
	if config.MaxValueBytes == 0 {
		config.MaxValueBytes = 1 << 20
	}
	if config.MaxValueBytes < 1 || config.MaxValueBytes > 64<<20 {
		return nil, errors.New("cache: invalid Memcached value limit")
	}
	if config.TLSConfig != nil {
		config.TLSConfig = config.TLSConfig.Clone()
	}
	return &MemcachedBackend{config: config}, nil
}

func (backend *MemcachedBackend) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if !validWireKey(backend.config.KeyPrefix + key) {
		return nil, false, errors.New("cache: invalid Memcached key")
	}
	connection, reader, err := backend.connect(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = connection.Close() }()
	key = backend.config.KeyPrefix + key
	if _, err := fmt.Fprintf(connection, "get %s\r\n", key); err != nil {
		return nil, false, fmt.Errorf("cache: write Memcached GET: %w", err)
	}
	line, err := readMemcachedLine(reader)
	if err != nil {
		return nil, false, err
	}
	if line == "END" {
		return nil, false, nil
	}
	parts := strings.Fields(line)
	if len(parts) != 4 || parts[0] != "VALUE" || parts[1] != key {
		return nil, false, fmt.Errorf("cache: invalid Memcached GET response %q", line)
	}
	length, err := strconv.Atoi(parts[3])
	if err != nil || length < 0 || length > backend.config.MaxValueBytes {
		return nil, false, fmt.Errorf("cache: invalid Memcached value length %q", parts[3])
	}
	value := make([]byte, length+2)
	if _, err := io.ReadFull(reader, value); err != nil {
		return nil, false, fmt.Errorf("cache: read Memcached value: %w", err)
	}
	if value[length] != '\r' || value[length+1] != '\n' {
		return nil, false, errors.New("cache: invalid Memcached value delimiter")
	}
	if ending, err := readMemcachedLine(reader); err != nil || ending != "END" {
		if err != nil {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("cache: invalid Memcached terminator %q", ending)
	}
	return append([]byte(nil), value[:length]...), true, nil
}

func (backend *MemcachedBackend) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if !validWireKey(backend.config.KeyPrefix+key) || ttl < 0 || len(value) > backend.config.MaxValueBytes {
		return errors.New("cache: invalid Memcached key/TTL/value limit")
	}
	connection, reader, err := backend.connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	expiration := memcachedExpiration(ttl, time.Now())
	if _, err := fmt.Fprintf(connection, "set %s 0 %d %d\r\n", backend.config.KeyPrefix+key, expiration, len(value)); err != nil {
		return fmt.Errorf("cache: write Memcached SET: %w", err)
	}
	if _, err := connection.Write(value); err != nil {
		return fmt.Errorf("cache: write Memcached value: %w", err)
	}
	if _, err := io.WriteString(connection, "\r\n"); err != nil {
		return fmt.Errorf("cache: write Memcached delimiter: %w", err)
	}
	line, err := readMemcachedLine(reader)
	if err != nil {
		return err
	}
	if line != "STORED" {
		return fmt.Errorf("cache: Memcached SET returned %q", line)
	}
	return nil
}

func (backend *MemcachedBackend) Delete(ctx context.Context, key string) error {
	if !validWireKey(backend.config.KeyPrefix + key) {
		return errors.New("cache: invalid Memcached key")
	}
	connection, reader, err := backend.connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	if _, err := fmt.Fprintf(connection, "delete %s\r\n", backend.config.KeyPrefix+key); err != nil {
		return fmt.Errorf("cache: write Memcached DELETE: %w", err)
	}
	line, err := readMemcachedLine(reader)
	if err != nil {
		return err
	}
	if line != "DELETED" && line != "NOT_FOUND" {
		return fmt.Errorf("cache: Memcached DELETE returned %q", line)
	}
	return nil
}

func (backend *MemcachedBackend) connect(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	if ctx == nil {
		return nil, nil, errors.New("cache: context is nil")
	}
	connection, err := dialCache(ctx, backend.config.Address, backend.config.DialTimeout, backend.config.IOTimeout, backend.config.TLSConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("cache: connect to Memcached: %w", err)
	}
	deadline := time.Now().Add(backend.config.IOTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		_ = connection.Close()
		return nil, nil, fmt.Errorf("cache: set Memcached deadline: %w", err)
	}
	return connection, bufio.NewReader(connection), nil
}

func readMemcachedLine(reader *bufio.Reader) (string, error) {
	raw, err := reader.ReadSlice('\n')
	line := string(raw)
	if err != nil {
		return "", fmt.Errorf("cache: read Memcached response: %w", err)
	}
	if len(line) < 2 || line[len(line)-2:] != "\r\n" {
		return "", errors.New("cache: invalid Memcached line ending")
	}
	return line[:len(line)-2], nil
}

func memcachedExpiration(ttl time.Duration, now time.Time) int64 {
	if ttl <= 0 {
		return 0
	}
	seconds := int64((ttl-1)/time.Second) + 1
	if seconds <= 30*24*60*60 {
		return seconds
	}
	return now.Add(ttl).Unix()
}
