//go:build performance

package performance_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitushka/framework/db"
	perf "github.com/chitushka/framework/internal/performance"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type sqlFixture struct {
	app, control              *db.Connection
	table, query, version     string
	serverBefore, serverAfter int
	once                      sync.Once
	closeError                error
}

func protocol(tb testing.TB) perf.Protocol {
	tb.Helper()
	p, err := perf.LoadProtocol()
	if err != nil {
		tb.Fatal(err)
	}
	return p
}

func newSQL(tb testing.TB, dialect string) *sqlFixture {
	tb.Helper()
	p := protocol(tb)
	driver, env := "pgx", "POSTGRES_DSN"
	if dialect == db.MySQL {
		driver, env = "mysql", "MYSQL_DSN"
	}
	dsn := os.Getenv(env)
	if dsn == "" {
		tb.Fatalf("performance: %s is required; no silent skip", env)
	}
	ctx, cancel := context.WithTimeout(tb.Context(), 10*time.Second)
	defer cancel()
	open := func(pool int) *db.Connection {
		c, err := db.Open(ctx, db.Config{Driver: driver, DSN: dsn, Dialect: dialect, MaxOpenConns: pool, MaxIdleConns: pool, PingTimeout: 5 * time.Second}, db.NewDefaultDialectRegistry())
		if err != nil {
			tb.Fatal(err)
		}
		return c
	}
	f := &sqlFixture{control: open(1)}
	tb.Cleanup(func() {
		if err := f.close(); err != nil {
			tb.Error(err)
		}
	})
	versionSQL := "SELECT version()"
	if err := f.control.QueryRowContext(ctx, versionSQL).Scan(&f.version); err != nil {
		tb.Fatal(err)
	}
	var err error
	f.serverBefore, err = f.serverCount(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	f.app = open(p.SQL.Pool)
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		tb.Fatal(err)
	}
	f.table = "perf_" + hex.EncodeToString(nonce[:])
	q := f.app.Dialect().QuoteIdentifier(f.table)
	if _, err := f.control.ExecContext(ctx, "CREATE TABLE "+q+" (id INTEGER PRIMARY KEY, name VARCHAR(128) NOT NULL)"); err != nil {
		tb.Fatal(err)
	}
	tx, err := f.control.DB().BeginTx(ctx, nil)
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	insert := "INSERT INTO " + q + " (id,name) VALUES (" + f.app.Dialect().Placeholder(1) + "," + f.app.Dialect().Placeholder(2) + ")"
	for i := 1; i <= p.SQL.Rows; i++ {
		if _, err := tx.ExecContext(ctx, insert, i, perf.Name()); err != nil {
			tb.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
	f.query = "SELECT id,name FROM " + q + " WHERE id=" + f.app.Dialect().Placeholder(1)
	// Establish every application connection before timings/resource warm-up.
	var pinned []*sql.Conn
	for range p.SQL.Pool {
		c, err := f.app.DB().Conn(ctx)
		if err != nil {
			for _, old := range pinned {
				_ = old.Close()
			}
			tb.Fatal(err)
		}
		pinned = append(pinned, c)
		var id int
		var name string
		if err := c.QueryRowContext(ctx, f.query, 42).Scan(&id, &name); err != nil || id != 42 || name != perf.Name() {
			for _, old := range pinned {
				_ = old.Close()
			}
			tb.Fatal("SQL warm-up/result check failed", err)
		}
	}
	for _, c := range pinned {
		if err := c.Close(); err != nil {
			tb.Fatal(err)
		}
	}
	return f
}

func (f *sqlFixture) serverCount(ctx context.Context) (int, error) {
	query := "SELECT COUNT(*) FROM pg_stat_activity WHERE usename=current_user AND datname=current_database()"
	if f.control.Dialect().Name() == db.MySQL {
		query = "SELECT COUNT(*) FROM information_schema.processlist WHERE USER=SUBSTRING_INDEX(CURRENT_USER(),'@',1) AND DB=DATABASE()"
	}
	var count int
	err := f.control.QueryRowContext(ctx, query).Scan(&count)
	return count, err
}

func (f *sqlFixture) close() error {
	f.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var failures []error
		if f.app != nil {
			failures = append(failures, f.app.Close())
		}
		if f.table != "" {
			_, err := f.control.ExecContext(ctx, "DROP TABLE IF EXISTS "+f.control.Dialect().QuoteIdentifier(f.table))
			failures = append(failures, err)
		}
		for {
			count, err := f.serverCount(ctx)
			f.serverAfter = count
			if err != nil {
				failures = append(failures, err)
				break
			}
			if count <= f.serverBefore {
				break
			}
			select {
			case <-ctx.Done():
				failures = append(failures, ctx.Err())
			case <-time.After(20 * time.Millisecond):
				continue
			}
			break
		}
		failures = append(failures, f.control.Close())
		f.closeError = errors.Join(failures...)
	})
	return f.closeError
}

func writeJSON(tb testing.TB, name string, value any) {
	tb.Helper()
	dir := os.Getenv("PERF_OUTPUT_DIR")
	if dir == "" {
		tb.Fatal("performance: PERF_OUTPUT_DIR is required")
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), append(encoded, '\n'), 0o600); err != nil {
		tb.Fatal(err)
	}
}

func TestPerformanceMetadata(t *testing.T) {
	read := func(path string) string {
		value, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(value))
	}
	cpuInfo := read("/proc/cpuinfo")
	model := ""
	for _, line := range strings.Split(cpuInfo, "\n") {
		if strings.HasPrefix(line, "model name") {
			_, model, _ = strings.Cut(line, ":")
			model = strings.TrimSpace(model)
			break
		}
	}
	versions := make(map[string]string)
	for _, dialect := range []string{db.PostgreSQL, db.MySQL} {
		f := newSQL(t, dialect)
		versions[dialect] = f.version
		if err := f.close(); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(t, "runtime.json", map[string]any{
		"go": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH, "cpus": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0),
		"cpuModel": model, "kernel": read("/proc/sys/kernel/osrelease"), "cpuQuota": read("/sys/fs/cgroup/cpu.max"), "memoryLimit": read("/sys/fs/cgroup/memory.max"),
		"services": versions, "protocolHash": perf.ProtocolHash(),
	})
}

func BenchmarkSQL(b *testing.B) {
	for _, dialect := range []string{db.PostgreSQL, db.MySQL} {
		b.Run(dialect, func(b *testing.B) {
			for _, variant := range []string{"NetSQL", "Chitu"} {
				b.Run(variant, func(b *testing.B) {
					f := newSQL(b, dialect)
					expectedName := perf.Name()
					ctx, cancel := context.WithTimeout(b.Context(), 15*time.Second)
					defer cancel()
					b.ReportAllocs()
					b.ResetTimer()
					b.RunParallel(func(pb *testing.PB) {
						id := 1
						for pb.Next() {
							var got int
							var name string
							var row *sql.Row
							if variant == "NetSQL" {
								row = f.app.DB().QueryRowContext(ctx, f.query, id)
							} else {
								row = f.app.QueryRowContext(ctx, f.query, id)
							}
							if err := row.Scan(&got, &name); err != nil {
								b.Error(err)
								return
							}
							if got != id || name != expectedName {
								b.Error(fmt.Errorf("SQL result differs"))
								return
							}
							id = id%256 + 1
						}
					})
				})
			}
		})
	}
}
