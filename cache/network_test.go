package cache

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRESPCodec(t *testing.T) {
	for input, check := range map[string]func(redisResponse) bool{
		"+OK\r\n":       func(response redisResponse) bool { return response.simple == "OK" },
		":42\r\n":       func(response redisResponse) bool { return response.integer == 42 },
		"$3\r\nfoo\r\n": func(response redisResponse) bool { return string(response.bulk) == "foo" },
		"$-1\r\n":       func(response redisResponse) bool { return response.nil },
	} {
		response, err := readRESP(bufio.NewReader(strings.NewReader(input)))
		if err != nil || !check(response) {
			t.Fatalf("readRESP(%q) = %#v, %v", input, response, err)
		}
	}
	for _, input := range []string{"-ERR nope\r\n", "?bad\r\n", "$2\r\nx\r\n", "+bad\n"} {
		if _, err := readRESP(bufio.NewReader(strings.NewReader(input))); err == nil {
			t.Fatalf("readRESP(%q) succeeded", input)
		}
	}
	var output strings.Builder
	if err := writeRESP(&output, []byte("GET"), []byte("key")); err != nil || output.String() != "*2\r\n$3\r\nGET\r\n$3\r\nkey\r\n" {
		t.Fatalf("writeRESP = %q, %v", output.String(), err)
	}
}

func TestRedisBackendProtocol(t *testing.T) {
	address, stop := startRedisTestServer(t)
	defer stop()
	backend, err := NewRedisBackend(RedisConfig{Address: address, Username: "app", Password: "secret", Database: 2, KeyPrefix: "test:"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := backend.Set(ctx, "key", []byte("hello\x00world"), 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	value, found, err := backend.Get(ctx, "key")
	if err != nil || !found || string(value) != "hello\x00world" {
		t.Fatalf("Get() = %q, %v, %v", value, found, err)
	}
	if err := backend.Delete(ctx, "key"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := backend.Get(ctx, "key"); err != nil || found {
		t.Fatalf("deleted found=%v err=%v", found, err)
	}
	if _, err := NewRedisBackend(RedisConfig{}); err == nil {
		t.Fatal("empty Redis address accepted")
	}
	if _, err := NewRedisBackend(RedisConfig{Address: address, Database: -1}); err == nil {
		t.Fatal("negative Redis database accepted")
	}
	unavailable, err := NewRedisBackend(RedisConfig{Address: "127.0.0.1:1", DialTimeout: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := unavailable.Get(context.Background(), "key"); err == nil {
		t.Fatal("unavailable Redis succeeded")
	}
}

func TestMemcachedBackendProtocol(t *testing.T) {
	address, stop := startMemcachedTestServer(t)
	defer stop()
	backend, err := NewMemcachedBackend(MemcachedConfig{Address: address, KeyPrefix: "test:"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := backend.Set(ctx, "key", []byte("hello\r\nworld"), 500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	value, found, err := backend.Get(ctx, "key")
	if err != nil || !found || string(value) != "hello\r\nworld" {
		t.Fatalf("Get() = %q, %v, %v", value, found, err)
	}
	if err := backend.Delete(ctx, "key"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := backend.Get(ctx, "key"); err != nil || found {
		t.Fatalf("deleted found=%v err=%v", found, err)
	}
	if got := memcachedExpiration(31*24*time.Hour, time.Unix(100, 0)); got != time.Unix(100, 0).Add(31*24*time.Hour).Unix() {
		t.Fatalf("absolute expiration = %d", got)
	}
	if _, err := NewMemcachedBackend(MemcachedConfig{}); err == nil {
		t.Fatal("empty Memcached address accepted")
	}
	if _, err := NewMemcachedBackend(MemcachedConfig{Address: address, KeyPrefix: "bad prefix"}); err == nil {
		t.Fatal("invalid Memcached prefix accepted")
	}
	if got := memcachedExpiration(500*time.Millisecond, time.Time{}); got != 1 {
		t.Fatalf("relative expiration = %d", got)
	}
}

func startRedisTestServer(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	values := map[string][]byte{}
	done := make(chan struct{})
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				close(done)
				return
			}
			go func() {
				defer func() { _ = connection.Close() }()
				reader := bufio.NewReader(connection)
				for {
					arguments, err := readRESPRequest(reader)
					if err != nil {
						return
					}
					command := strings.ToUpper(string(arguments[0]))
					switch command {
					case "AUTH", "SELECT":
						_, _ = io.WriteString(connection, "+OK\r\n")
					case "SET":
						mu.Lock()
						values[string(arguments[1])] = append([]byte(nil), arguments[2]...)
						mu.Unlock()
						_, _ = io.WriteString(connection, "+OK\r\n")
					case "GET":
						mu.Lock()
						value, found := values[string(arguments[1])]
						mu.Unlock()
						if !found {
							_, _ = io.WriteString(connection, "$-1\r\n")
						} else {
							_, _ = fmt.Fprintf(connection, "$%d\r\n", len(value))
							_, _ = connection.Write(value)
							_, _ = io.WriteString(connection, "\r\n")
						}
					case "DEL":
						mu.Lock()
						delete(values, string(arguments[1]))
						mu.Unlock()
						_, _ = io.WriteString(connection, ":1\r\n")
					}
				}
			}()
		}
	}()
	return listener.Addr().String(), func() { _ = listener.Close(); <-done }
}

func readRESPRequest(reader *bufio.Reader) ([][]byte, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(line, "*"), "\r\n"))
	if err != nil {
		return nil, err
	}
	result := make([][]byte, count)
	for index := range result {
		line, err = reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(line, "$"), "\r\n"))
		if err != nil {
			return nil, err
		}
		value := make([]byte, length+2)
		if _, err := io.ReadFull(reader, value); err != nil {
			return nil, err
		}
		result[index] = value[:length]
	}
	return result, nil
}

func startMemcachedTestServer(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	values := map[string][]byte{}
	done := make(chan struct{})
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				close(done)
				return
			}
			go func() {
				defer func() { _ = connection.Close() }()
				reader := bufio.NewReader(connection)
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				parts := strings.Fields(line)
				switch parts[0] {
				case "set":
					length, _ := strconv.Atoi(parts[4])
					value := make([]byte, length+2)
					_, _ = io.ReadFull(reader, value)
					mu.Lock()
					values[parts[1]] = append([]byte(nil), value[:length]...)
					mu.Unlock()
					_, _ = io.WriteString(connection, "STORED\r\n")
				case "get":
					mu.Lock()
					value, found := values[parts[1]]
					mu.Unlock()
					if found {
						_, _ = fmt.Fprintf(connection, "VALUE %s 0 %d\r\n", parts[1], len(value))
						_, _ = connection.Write(value)
						_, _ = io.WriteString(connection, "\r\n")
					}
					_, _ = io.WriteString(connection, "END\r\n")
				case "delete":
					mu.Lock()
					_, found := values[parts[1]]
					delete(values, parts[1])
					mu.Unlock()
					if found {
						_, _ = io.WriteString(connection, "DELETED\r\n")
					} else {
						_, _ = io.WriteString(connection, "NOT_FOUND\r\n")
					}
				}
			}()
		}
	}()
	return listener.Addr().String(), func() { _ = listener.Close(); <-done }
}
