package cache

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// RedisConfig configures the built-in RESP2 Redis backend.
type RedisConfig struct {
	Address       string
	Username      string
	Password      string
	Database      int
	KeyPrefix     string
	DialTimeout   time.Duration
	IOTimeout     time.Duration
	TLSConfig     *tls.Config
	MaxValueBytes int
}

// RedisBackend uses short-lived TCP connections and requires no external client library.
type RedisBackend struct{ config RedisConfig }

func NewRedisBackend(config RedisConfig) (*RedisBackend, error) {
	if strings.TrimSpace(config.Address) == "" {
		return nil, errors.New("cache: Redis address is required")
	}
	if config.Database < 0 {
		return nil, errors.New("cache: Redis database cannot be negative")
	}
	if config.DialTimeout < 0 || config.IOTimeout < 0 {
		return nil, errors.New("cache: Redis timeouts cannot be negative")
	}
	if config.DialTimeout == 0 {
		config.DialTimeout = 5 * time.Second
	}
	if config.IOTimeout == 0 {
		config.IOTimeout = 5 * time.Second
	}
	if config.MaxValueBytes == 0 {
		config.MaxValueBytes = 16 << 20
	}
	if config.MaxValueBytes < 1 || config.MaxValueBytes > 64<<20 {
		return nil, errors.New("cache: invalid Redis value limit")
	}
	if config.TLSConfig != nil {
		config.TLSConfig = config.TLSConfig.Clone()
	}
	return &RedisBackend{config: config}, nil
}

func (backend *RedisBackend) Get(ctx context.Context, key string) ([]byte, bool, error) {
	response, err := backend.command(ctx, "GET", backend.config.KeyPrefix+key)
	if err != nil {
		return nil, false, err
	}
	if response.nil {
		return nil, false, nil
	}
	if response.bulk == nil {
		return nil, false, errors.New("cache: Redis GET returned an unexpected response")
	}
	return response.bulk, true, nil
}

func (backend *RedisBackend) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl < 0 || len(value) > backend.config.MaxValueBytes {
		return errors.New("cache: invalid TTL or Redis value too large")
	}
	arguments := [][]byte{[]byte("SET"), []byte(backend.config.KeyPrefix + key), value}
	if ttl > 0 {
		milliseconds := ttl.Milliseconds()
		if milliseconds < 1 {
			milliseconds = 1
		}
		arguments = append(arguments, []byte("PX"), []byte(strconv.FormatInt(milliseconds, 10)))
	}
	response, err := backend.commandBytes(ctx, arguments...)
	if err != nil {
		return err
	}
	if response.simple != "OK" {
		return fmt.Errorf("cache: Redis SET returned %q", response.simple)
	}
	return nil
}

func (backend *RedisBackend) Delete(ctx context.Context, key string) error {
	_, err := backend.command(ctx, "DEL", backend.config.KeyPrefix+key)
	return err
}

type redisResponse struct {
	simple  string
	bulk    []byte
	integer int64
	nil     bool
}

func (backend *RedisBackend) command(ctx context.Context, arguments ...string) (redisResponse, error) {
	bytes := make([][]byte, len(arguments))
	for index := range arguments {
		bytes[index] = []byte(arguments[index])
	}
	return backend.commandBytes(ctx, bytes...)
}

func (backend *RedisBackend) commandBytes(ctx context.Context, arguments ...[]byte) (redisResponse, error) {
	if ctx == nil {
		return redisResponse{}, errors.New("cache: context is nil")
	}
	connection, err := dialCache(ctx, backend.config.Address, backend.config.DialTimeout, backend.config.IOTimeout, backend.config.TLSConfig)
	if err != nil {
		return redisResponse{}, fmt.Errorf("cache: connect to Redis: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(backend.config.IOTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return redisResponse{}, fmt.Errorf("cache: set Redis deadline: %w", err)
	}
	reader := bufio.NewReader(connection)
	if backend.config.Password != "" {
		auth := [][]byte{[]byte("AUTH")}
		if backend.config.Username != "" {
			auth = append(auth, []byte(backend.config.Username))
		}
		auth = append(auth, []byte(backend.config.Password))
		if err := writeRESP(connection, auth...); err != nil {
			return redisResponse{}, err
		}
		if _, err := readRESP(reader); err != nil {
			return redisResponse{}, fmt.Errorf("cache: Redis AUTH: %w", err)
		}
	}
	if backend.config.Database != 0 {
		if err := writeRESP(connection, []byte("SELECT"), []byte(strconv.Itoa(backend.config.Database))); err != nil {
			return redisResponse{}, err
		}
		if _, err := readRESP(reader); err != nil {
			return redisResponse{}, fmt.Errorf("cache: Redis SELECT: %w", err)
		}
	}
	if err := writeRESP(connection, arguments...); err != nil {
		return redisResponse{}, err
	}
	response, err := readRESP(reader)
	if err != nil {
		return redisResponse{}, fmt.Errorf("cache: Redis command: %w", err)
	}
	return response, nil
}

func writeRESP(writer io.Writer, arguments ...[]byte) error {
	if _, err := fmt.Fprintf(writer, "*%d\r\n", len(arguments)); err != nil {
		return fmt.Errorf("cache: write Redis command: %w", err)
	}
	for _, argument := range arguments {
		if _, err := fmt.Fprintf(writer, "$%d\r\n", len(argument)); err != nil {
			return fmt.Errorf("cache: write Redis argument: %w", err)
		}
		if _, err := writer.Write(argument); err != nil {
			return fmt.Errorf("cache: write Redis argument: %w", err)
		}
		if _, err := io.WriteString(writer, "\r\n"); err != nil {
			return fmt.Errorf("cache: write Redis delimiter: %w", err)
		}
	}
	return nil
}

func readRESP(reader *bufio.Reader) (redisResponse, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return redisResponse{}, err
	}
	line, err := readCRLFLine(reader)
	if err != nil {
		return redisResponse{}, err
	}
	switch prefix {
	case '+':
		return redisResponse{simple: string(line)}, nil
	case '-':
		return redisResponse{}, errors.New(string(line))
	case ':':
		value, err := strconv.ParseInt(string(line), 10, 64)
		return redisResponse{integer: value}, err
	case '$':
		length, err := strconv.Atoi(string(line))
		if err != nil {
			return redisResponse{}, err
		}
		if length == -1 {
			return redisResponse{nil: true}, nil
		}
		if length < 0 || length > 64<<20 {
			return redisResponse{}, fmt.Errorf("invalid bulk length %d", length)
		}
		value := make([]byte, length+2)
		if _, err := io.ReadFull(reader, value); err != nil {
			return redisResponse{}, err
		}
		if value[length] != '\r' || value[length+1] != '\n' {
			return redisResponse{}, errors.New("invalid bulk delimiter")
		}
		return redisResponse{bulk: value[:length]}, nil
	default:
		return redisResponse{}, fmt.Errorf("unknown RESP prefix %q", prefix)
	}
}

func readCRLFLine(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return nil, errors.New("invalid RESP line ending")
	}
	return line[:len(line)-2], nil
}
