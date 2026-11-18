// Command operations is the reference application for RC operational acceptance.
// It is test infrastructure; its images are never published as framework starters.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	rabbit "github.com/chitushka/framework/adapters/rabbitmq"
	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/cache"
	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/db/migration"
	"github.com/chitushka/framework/queue"
	"github.com/chitushka/framework/web"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type source []migration.Migration

func (s source) Migrations(context.Context) ([]migration.Migration, error) { return s, nil }

var base = migration.Migration{Version: "20261118000000", Name: "operations", UpSQL: `CREATE TABLE operations (id VARCHAR(96) PRIMARY KEY, enqueued BOOLEAN NOT NULL DEFAULT FALSE, effects INTEGER NOT NULL DEFAULT 0 CHECK (effects BETWEEN 0 AND 1))`, DownSQL: `DROP TABLE operations`}
var expand = migration.Migration{Version: "20261118000001", Name: "expand", UpSQL: `ALTER TABLE operations ADD COLUMN note TEXT NOT NULL DEFAULT ''`, DownSQL: `ALTER TABLE operations DROP COLUMN note`}
var interrupted = migration.Migration{Version: "20261118000002", Name: "recovery", NonTransactional: true, UpSQL: `BEGIN; CREATE TABLE recovery_evidence (id INTEGER PRIMARY KEY); COMMIT; SELECT pg_sleep(60)`, DownSQL: `DROP TABLE recovery_evidence`}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	mode := "serve"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if mode == "certs" {
		return certificates(os.Args[2])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	connection, err := db.Open(ctx, db.Config{Driver: "pgx", DSN: os.Getenv("DB_DSN"), Dialect: db.PostgreSQL, MaxOpenConns: 8, MaxIdleConns: 4, PingTimeout: 3 * time.Second, ConnMaxIdleTime: 30 * time.Second}, db.NewDefaultDialectRegistry())
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	if mode != "serve" {
		return migrate(ctx, connection, mode)
	}
	return serve(connection)
}

func migrate(ctx context.Context, connection *db.Connection, mode string) error {
	migrations, table := source{base}, "ops_migration"
	if mode == "expand" {
		migrations = append(migrations, expand)
	}
	if mode == "crash-migration" || mode == "recover" {
		migrations, table = source{interrupted}, "ops_recovery"
	}
	runner, err := migration.NewRunner(connection, migrations, migration.Options{Table: table, LockTimeout: 3 * time.Second})
	if err != nil {
		return err
	}
	if mode == "recover" {
		if _, err := runner.Up(ctx, 0); !errors.Is(err, migration.ErrDirty) {
			return fmt.Errorf("expected dirty migration: %v", err)
		}
		var exists bool
		if err := connection.QueryRowContext(ctx, "SELECT to_regclass('recovery_evidence') IS NOT NULL").Scan(&exists); err != nil || !exists {
			return fmt.Errorf("inspect DDL: %v, exists=%v", err, exists)
		}
		var count int
		if err := connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM recovery_evidence").Scan(&count); err != nil || count != 0 {
			return fmt.Errorf("inspect rows: %v, count=%d", err, count)
		}
		if err := runner.Resolve(ctx, interrupted.ID(), interrupted.Checksum(), true); err != nil {
			return err
		}
		results, err := runner.Up(ctx, 0)
		if err != nil || len(results) != 0 {
			return fmt.Errorf("recovery not idempotent: %v", err)
		}
		fmt.Println("PASS dirty blocks Up; inspected DDL; exact-checksum Resolve; idempotent Up")
		return nil
	}
	_, err = runner.Up(ctx, 0)
	return err
}

type application struct {
	db             *db.Connection
	cache          *cache.RedisBackend
	verifier       *auth.JWTVerifier
	broker         *queue.RabbitMQBroker
	jobs           *queue.Queue
	dead           *queue.FileDeadLetters
	workerRestarts atomic.Int64
	jobFailures    atomic.Int64
}

func serve(connection *db.Connection) error {
	backend, err := cache.NewRedisBackend(cache.RedisConfig{Address: "cache:6379", Password: os.Getenv("SERVICE_PASSWORD"), DialTimeout: 300 * time.Millisecond, IOTimeout: 300 * time.Millisecond, MaxValueBytes: 1024})
	if err != nil {
		return err
	}
	verifier, err := auth.NewJWTVerifier(auth.JWTConfig{Issuer: "ops-issuer", Audience: "ops-api", Algorithms: []string{"EdDSA"}}, mustKeys())
	if err != nil {
		return err
	}
	channel, err := rabbit.New(rabbit.Config{URL: os.Getenv("AMQP_URL"), OperationTimeout: 750 * time.Millisecond})
	if err != nil {
		return err
	}
	defer func() { _ = channel.Close() }()
	broker, err := queue.NewRabbitMQBroker(queue.RabbitMQConfig{Channel: channel, Queue: "operations"})
	if err != nil {
		return err
	}
	jobs, err := queue.New(broker)
	if err != nil {
		return err
	}
	dead, err := queue.NewFileDeadLetters("/data/deadletters")
	if err != nil {
		return err
	}
	defer func() { _ = dead.Close() }()
	a := &application{db: connection, cache: backend, verifier: verifier, broker: broker, jobs: jobs, dead: dead}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	background, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for _, loop := range []func(context.Context){a.dispatch, a.supervise} {
		wg.Add(1)
		go func() { defer wg.Done(); loop(background) }()
	}
	router := web.NewRouter()
	if err := router.Use(auth.Bearer(verifier)); err != nil {
		return err
	}
	if err := router.POST("/work", a.work); err != nil {
		return err
	}
	if err := router.GET("/status", a.status); err != nil {
		return err
	}
	handler, err := web.NewProxyHandler(router, web.ProxyConfig{TrustedProxies: []string{os.Getenv("TRUSTED_PROXY")}, ForwardedHeader: "X-Forwarded", AllowedHosts: []string{"rc.test"}})
	if err != nil {
		return err
	}
	handler, err = web.NewProductionHandler(handler)
	if err != nil {
		return err
	}
	server, err := web.NewServer(web.ServerConfig{Address: ":8080", ShutdownTimeout: 10 * time.Second}, handler)
	if err != nil {
		return err
	}
	if err := server.Boot(ctx, nil); err != nil {
		return err
	}
	// A key-ring file is atomically replaced by the operator; incomplete/invalid
	// updates preserve the last valid snapshot, as required by JWTVerifier.
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-background.Done():
				return
			case <-ticker.C:
				if keys, err := readKeys(); err == nil {
					_ = verifier.ReplaceKeys(keys)
				}
			}
		}
	}()
	select {
	case <-ctx.Done():
	case err := <-server.Errors():
		return err
	}
	cleanup, done := context.WithTimeout(context.Background(), 12*time.Second)
	defer done()
	err = server.Shutdown(cleanup)
	cancel()
	wg.Wait()
	return err
}

type keyEntry struct {
	ID    string    `json:"id"`
	Key   string    `json:"key"`
	Until time.Time `json:"until"`
}

func readKeys() ([]auth.JWTKey, error) {
	data, err := os.ReadFile("/run/config/keys.json")
	if err != nil {
		return nil, err
	}
	var entries []keyEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	keys := make([]auth.JWTKey, 0, len(entries))
	for _, entry := range entries {
		key, err := base64.StdEncoding.DecodeString(entry.Key)
		if err != nil {
			return nil, err
		}
		keys = append(keys, auth.JWTKey{ID: entry.ID, Algorithm: "EdDSA", PublicKey: ed25519.PublicKey(key), AcceptUntil: entry.Until})
	}
	return keys, nil
}
func mustKeys() []auth.JWTKey {
	keys, err := readKeys()
	if err != nil {
		panic(err)
	}
	return keys
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9-]{1,96}$`)

type operation struct {
	ID string `json:"id"`
}
type batch struct {
	IDs []string `json:"ids"`
}

func (a *application) work(c *web.Context) error {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 750*time.Millisecond)
	defer cancel()
	var input operation
	if err := c.BindJSON(&input); err != nil {
		return err
	}
	if !validID.MatchString(input.ID) {
		return web.NewHTTPError(400, "invalid ID", nil)
	}
	if _, _, err := a.cache.Get(ctx, "catalog"); err != nil {
		return web.NewHTTPError(503, "dependency unavailable", nil)
	}
	if err := a.cache.Set(ctx, "catalog", []byte("reference"), time.Minute); err != nil {
		return web.NewHTTPError(503, "dependency unavailable", nil)
	}
	query := "INSERT INTO operations (id) VALUES ($1) ON CONFLICT (id) DO NOTHING"
	if os.Getenv("APP_REVISION") == "2" {
		query = "INSERT INTO operations (id, note) VALUES ($1, 'expanded') ON CONFLICT (id) DO NOTHING"
	}
	if _, err := a.db.ExecContext(ctx, query, input.ID); err != nil {
		return web.NewHTTPError(503, "dependency unavailable", nil)
	}
	return c.JSON(202, map[string]any{"id": input.ID, "replica": os.Getenv("REPLICA"), "revision": os.Getenv("APP_REVISION"), "scheme": c.Scheme(), "host": c.Host()})
}

func (a *application) status(c *web.Context) error {
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
	defer cancel()
	var total, processed, pending int
	if err := a.db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(effects),0), COUNT(*) FILTER (WHERE NOT enqueued) FROM operations").Scan(&total, &processed, &pending); err != nil {
		return web.NewHTTPError(503, "dependency unavailable", nil)
	}
	if _, _, err := a.cache.Get(ctx, "catalog"); err != nil {
		return web.NewHTTPError(503, "dependency unavailable", nil)
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	stats := a.db.DB().Stats()
	var rss uint64
	if data, err := os.ReadFile("/proc/self/statm"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 1 {
			pages, _ := strconv.ParseUint(fields[1], 10, 64)
			rss = pages * uint64(os.Getpagesize())
		}
	}
	return c.JSON(200, map[string]any{"total": total, "processed": processed, "pending": pending, "goroutines": runtime.NumGoroutine(), "heapBytes": memory.HeapAlloc, "rssBytes": rss, "sqlOpen": stats.OpenConnections, "sqlInUse": stats.InUse, "workerRestarts": a.workerRestarts.Load(), "jobFailures": a.jobFailures.Load(), "replica": os.Getenv("REPLICA"), "revision": os.Getenv("APP_REVISION"), "scheme": c.Scheme(), "host": c.Host()})
}

// Lock a bounded batch with SKIP LOCKED so replicas do not publish the same
// pending rows concurrently. A crash between confirm and SQL commit may still
// duplicate delivery; the effect is guarded by the operation primary key.
func (a *application) dispatch(ctx context.Context) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.publishBatch(ctx)
		}
	}
}
func (a *application) publishBatch(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	_ = a.db.Transaction(ctx, nil, func(tx *db.Transaction) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM operations WHERE NOT enqueued ORDER BY id LIMIT 32 FOR UPDATE SKIP LOCKED")
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil || len(ids) == 0 {
			return err
		}
		if _, err := a.jobs.Push(ctx, "effect", batch{IDs: ids}, queue.Options{MaxAttempts: 100}); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, "UPDATE operations SET enqueued=TRUE WHERE id=$1", id); err != nil {
				return err
			}
		}
		return nil
	})
}
func (a *application) supervise(ctx context.Context) {
	for ctx.Err() == nil {
		worker, err := queue.NewWorker(a.broker, queue.WorkerConfig{Concurrency: 2, PollInterval: 50 * time.Millisecond, JobTimeout: time.Second, SettlementTimeout: time.Second, ShutdownTimeout: 5 * time.Second, DeadLetters: a.dead, RetryDelay: func(int) time.Duration { return time.Second }, OnFailure: func(f queue.Failure) {
			if f.Final {
				a.jobFailures.Add(1)
			}
		}})
		if err != nil {
			panic(err)
		}
		if err := worker.RegisterHandler("effect", queue.JSONHandler(func(ctx context.Context, job batch) error {
			if len(job.IDs) == 0 || len(job.IDs) > 32 {
				return errors.New("invalid batch size")
			}
			return a.db.Transaction(ctx, &sql.TxOptions{}, func(tx *db.Transaction) error {
				for _, id := range job.IDs {
					if _, err := tx.ExecContext(ctx, "UPDATE operations SET effects=1 WHERE id=$1 AND effects=0", id); err != nil {
						return err
					}
				}
				return nil
			})
		})); err != nil {
			panic(err)
		}
		if err := worker.Start(ctx); err != nil {
			return
		}
		select {
		case <-ctx.Done():
		case <-worker.Errors():
			a.workerRestarts.Add(1)
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		_ = worker.Shutdown(cleanup)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func certificates(directory string) error {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "RC acceptance ephemeral CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPublic, caPrivate)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0644); err != nil {
		return err
	}
	for i := 1; i <= 2; i++ {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 1)), DNSNames: []string{"rc.test"}, Subject: pkix.Name{CommonName: "rc.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, public, caPrivate)
		if err != nil {
			return err
		}
		encoded, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("tls%d.crt", i)), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("tls%d.key", i)), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0600); err != nil {
			return err
		}
	}
	return nil
}
