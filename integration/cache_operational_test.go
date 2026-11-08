package integration_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	redisadapter "github.com/chitushka/framework/adapters/redis"
	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/cache"
	"github.com/chitushka/framework/session"
	sdk "github.com/redis/go-redis/v9"
)

// A TLS endpoint backed by the real service; reset injects a transport outage.
func serviceRelay(t *testing.T, target string, secure bool) (string, *tls.Config, *atomic.Bool) {
	t.Helper()
	reset := &atomic.Bool{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	check(t, err)
	var clientTLS *tls.Config
	if secure {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		check(t, err)
		template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
		der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
		check(t, err)
		key, err := x509.MarshalPKCS8PrivateKey(priv)
		check(t, err)
		certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
		check(t, err)
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		clientTLS = &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13}
		listener = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13})
	}
	ctx, cancel := context.WithCancel(context.Background())
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		for {
			incoming, err := listener.Accept()
			if err != nil {
				return
			}
			group.Add(1)
			go func() {
				defer group.Done()
				defer func() { _ = incoming.Close() }()
				if reset.Load() {
					return
				}
				outgoing, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", target)
				if err != nil {
					return
				}
				defer func() { _ = outgoing.Close() }()
				stop := context.AfterFunc(ctx, func() { _ = incoming.Close(); _ = outgoing.Close() })
				defer stop()
				done := make(chan struct{})
				go func() { _, _ = io.Copy(outgoing, incoming); _ = outgoing.Close(); close(done) }()
				_, _ = io.Copy(incoming, outgoing)
				_ = incoming.Close()
				<-done
			}()
		}
	}()
	t.Cleanup(func() { cancel(); _ = listener.Close(); group.Wait() })
	return listener.Addr().String(), clientTLS, reset
}

func TestCacheTLSAuthOutageAndReconnect(t *testing.T) {
	for _, kind := range []string{"redis", "memcached"} {
		t.Run(kind, func(t *testing.T) {
			target := required(t, "REDIS_ADDRESS")
			if kind == "memcached" {
				target = required(t, "MEMCACHED_ADDRESS")
			}
			address, tlsConfig, reset := serviceRelay(t, target, true)
			var backend cache.Backend
			var err error
			if kind == "redis" {
				backend, err = cache.NewRedisBackend(cache.RedisConfig{Address: address, Password: required(t, "REDIS_PASSWORD"), TLSConfig: tlsConfig, IOTimeout: time.Second})
			} else {
				backend, err = cache.NewMemcachedBackend(cache.MemcachedConfig{Address: address, TLSConfig: tlsConfig, IOTimeout: time.Second})
			}
			check(t, err)
			backendContract(t, backend)
			key := unique()
			payload := bytes.Repeat([]byte{0, 1, 2, 255}, 32<<10)
			check(t, backend.Set(testContext(t), key, payload, time.Minute))
			actual, found, err := backend.Get(testContext(t), key)
			check(t, err)
			if !found || !bytes.Equal(actual, payload) {
				t.Fatal("large binary value corrupted")
			}
			reset.Store(true)
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			_, _, err = backend.Get(ctx, key)
			cancel()
			if err == nil {
				t.Fatal("outage reported success")
			}
			reset.Store(false)
			actual, found, err = backend.Get(testContext(t), key)
			check(t, err)
			if !found || !bytes.Equal(actual, payload) {
				t.Fatal("reconnect lost value")
			}
			check(t, backend.Delete(testContext(t), key))
			if kind == "redis" {
				wrong, err := cache.NewRedisBackend(cache.RedisConfig{Address: address, Password: "wrong-password", TLSConfig: tlsConfig})
				check(t, err)
				if _, _, err := wrong.Get(testContext(t), key); err == nil {
					t.Fatal("invalid Redis credentials accepted")
				}
			}
		})
	}
}

func TestRedisAtomicSessionAndSharedLimiter(t *testing.T) {
	options := &sdk.Options{Addr: required(t, "REDIS_ADDRESS"), Password: required(t, "REDIS_PASSWORD"), DB: 1}
	clientA, clientB := redisadapter.NewClient(options), redisadapter.NewClient(options)
	t.Cleanup(func() { check(t, clientA.Close()); check(t, clientB.Close()) })
	namespace := unique()
	a, err := redisadapter.New(clientA, namespace)
	check(t, err)
	b, err := redisadapter.New(clientB, namespace)
	check(t, err)
	backendA, err := session.NewBackend(a)
	check(t, err)
	backendB, err := session.NewBackend(b)
	check(t, err)
	first, second := backendA.(session.AtomicBackend), backendB.(session.AtomicBackend)
	ctx := testContext(t)
	check(t, first.Save(ctx, "old", []byte("version1"), time.Minute))
	var wins atomic.Int32
	var group sync.WaitGroup
	for _, backend := range []session.AtomicBackend{first, second} {
		group.Add(1)
		go func() {
			defer group.Done()
			ok, err := backend.CompareAndSwap(ctx, "old", []byte("version1"), "new", []byte("version2"), time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	group.Wait()
	if wins.Load() != 1 {
		t.Fatalf("rotation winners=%d", wins.Load())
	}
	if _, found, err := first.Load(ctx, "old"); err != nil || found {
		t.Fatalf("old ID resurrected: %t %v", found, err)
	}
	if ok, err := second.CompareAndSwap(ctx, "old", []byte("version1"), "old", []byte("replay"), time.Minute); err != nil || ok {
		t.Fatal("stale commit accepted", err)
	}
	check(t, first.Delete(ctx, "new"))
	limiterA, err := redisadapter.NewLimiter(a, auth.AttemptLimitConfig{AccountLimit: 3, ClientLimit: 10, Window: time.Second})
	check(t, err)
	limiterB, err := redisadapter.NewLimiter(b, auth.AttemptLimitConfig{AccountLimit: 3, ClientLimit: 10, Window: time.Second})
	check(t, err)
	allowed := atomic.Int32{}
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			limiter := limiterA
			if i%2 == 1 {
				limiter = limiterB
			}
			retry, err := limiter.Allow(ctx, "account", "client")
			if err != nil {
				t.Error(err)
			}
			if retry == 0 {
				allowed.Add(1)
			}
		}(i)
	}
	group.Wait()
	if allowed.Load() != 3 {
		t.Fatalf("non-atomic shared limiter=%d", allowed.Load())
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := limiterA.Allow(cancelled, "account", "client"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
