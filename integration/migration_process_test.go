package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/db/migration"
	mysql "github.com/go-sql-driver/mysql"
)

type processSource struct {
	item migration.Migration
	hold bool
}

func (s processSource) Migrations(context.Context) ([]migration.Migration, error) {
	if s.hold {
		fmt.Println("LOCKED")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			return nil, err
		}
	}
	return []migration.Migration{s.item}, nil
}

// This test is re-executed as a separate process only by the scenarios below.
func TestMigrationProcessHelper(t *testing.T) {
	mode := os.Getenv("CHITU_MIGRATION_CHILD")
	if mode == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var item migration.Migration
	check(t, json.Unmarshal([]byte(os.Getenv("CHITU_MIGRATION_JSON")), &item))
	driver, dialect, dsn := os.Getenv("CHITU_MIGRATION_DRIVER"), os.Getenv("CHITU_MIGRATION_DIALECT"), os.Getenv("CHITU_MIGRATION_DSN")
	if driver == "mysql" {
		config, err := mysql.ParseDSN(dsn)
		check(t, err)
		config.MultiStatements = true
		dsn = config.FormatDSN()
	}
	connection, err := db.Open(ctx, db.Config{Driver: driver, DSN: dsn, Dialect: dialect, MaxOpenConns: 1, MaxIdleConns: 1, PingTimeout: 5 * time.Second}, db.NewDefaultDialectRegistry())
	check(t, err)
	defer func() { check(t, connection.Close()) }()
	query := "SELECT pg_backend_pid()"
	if dialect == db.MySQL {
		query = "SELECT CONNECTION_ID()"
	}
	var session int64
	check(t, connection.QueryRowContext(ctx, query).Scan(&session))
	fmt.Printf("SESSION %d\n", session)
	runner, err := migration.NewRunner(connection, processSource{item: item, hold: mode == "hold"}, migration.Options{Table: os.Getenv("CHITU_MIGRATION_HISTORY"), LockTimeout: 10 * time.Second})
	check(t, err)
	results, err := runner.Up(ctx, 0)
	check(t, err)
	fmt.Printf("RESULT %d\n", len(results))
}

type migrationChild struct {
	command        *exec.Cmd
	lines          chan string
	finished       chan struct{}
	stderr, stdout bytes.Buffer
	err            error
}

func startMigrationChild(t *testing.T, driver, dialect, dsn, history, mode string, item migration.Migration) *migrationChild {
	t.Helper()
	executable, err := os.Executable()
	check(t, err)
	encoded, err := json.Marshal(item)
	check(t, err)
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestMigrationProcessHelper$", "-test.timeout=25s")
	command.Env = append(os.Environ(), "CHITU_MIGRATION_CHILD="+mode, "CHITU_MIGRATION_DRIVER="+driver, "CHITU_MIGRATION_DIALECT="+dialect, "CHITU_MIGRATION_DSN="+dsn, "CHITU_MIGRATION_HISTORY="+history, "CHITU_MIGRATION_JSON="+string(encoded))
	child := &migrationChild{command: command, lines: make(chan string, 8), finished: make(chan struct{})}
	command.Stderr = &child.stderr
	output, err := command.StdoutPipe()
	check(t, err)
	input, err := command.StdinPipe()
	check(t, err)
	check(t, command.Start())
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			line := scanner.Text()
			child.stdout.WriteString(line + "\n")
			if strings.HasPrefix(line, "SESSION ") || strings.HasPrefix(line, "RESULT ") || line == "LOCKED" {
				child.lines <- line
			}
		}
		child.err = errors.Join(scanner.Err(), command.Wait())
		close(child.lines)
		close(child.finished)
	}()
	t.Cleanup(func() {
		_ = input.Close()
		select {
		case <-child.finished:
		default:
			_ = command.Process.Kill()
			<-child.finished
		}
	})
	return child
}

func (child *migrationChild) line(t *testing.T, prefix string) string {
	t.Helper()
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-child.lines:
			if !ok {
				<-child.finished
				t.Fatalf("child exited before %s: %v\n%s%s", prefix, child.err, child.stdout.String(), child.stderr.String())
			}
			if strings.HasPrefix(line, prefix) {
				return strings.TrimPrefix(line, prefix)
			}
		case <-timer.C:
			_ = child.command.Process.Kill()
			<-child.finished
			t.Fatalf("child timed out before %s: %s%s", prefix, child.stdout.String(), child.stderr.String())
		}
	}
}

func (child *migrationChild) wait(t *testing.T) {
	t.Helper()
	select {
	case <-child.finished:
	case <-time.After(15 * time.Second):
		_ = child.command.Process.Kill()
		<-child.finished
		t.Fatal("child did not finish")
	}
	if child.err != nil {
		t.Fatalf("migration child: %v\n%s%s", child.err, child.stdout.String(), child.stderr.String())
	}
}

func TestMigrationProcessesAndCrashRecovery(t *testing.T) {
	for _, backend := range []struct{ dialect, driver, env string }{{db.PostgreSQL, "pgx", "POSTGRES_DSN"}, {db.MySQL, "mysql", "MYSQL_DSN"}} {
		t.Run(backend.dialect, func(t *testing.T) {
			dsn := required(t, backend.env)
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			connection, err := db.Open(ctx, db.Config{Driver: backend.driver, DSN: dsn, Dialect: backend.dialect, MaxOpenConns: 4, MaxIdleConns: 2, PingTimeout: 5 * time.Second}, db.NewDefaultDialectRegistry())
			check(t, err)
			t.Cleanup(func() { check(t, connection.Close()) })
			q := connection.Dialect().QuoteIdentifier
			cleanup := func(names ...string) {
				t.Cleanup(func() {
					cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
					defer stop()
					for _, name := range names {
						_, err := connection.ExecContext(cleanup, "DROP TABLE IF EXISTS "+q(name))
						check(t, err)
					}
				})
			}
			newRunner := func(history string, item migration.Migration) *migration.Runner {
				runner, err := migration.NewRunner(connection, processSource{item: item}, migration.Options{Table: history, LockTimeout: 10 * time.Second})
				check(t, err)
				return runner
			}
			t.Run("two processes apply once", func(t *testing.T) {
				history, table := unique(), unique()
				cleanup(table, history, history+"_state")
				item := migration.Migration{Version: "20261104000000", Name: "once", UpSQL: "CREATE TABLE " + q(table) + " (id INTEGER PRIMARY KEY)", DownSQL: "DROP TABLE " + q(table)}
				first := startMigrationChild(t, backend.driver, backend.dialect, dsn, history, "up", item)
				second := startMigrationChild(t, backend.driver, backend.dialect, dsn, history, "up", item)
				first.wait(t)
				second.wait(t)
				a, err := strconv.Atoi(first.line(t, "RESULT "))
				check(t, err)
				b, err := strconv.Atoi(second.line(t, "RESULT "))
				check(t, err)
				if a+b != 1 {
					t.Fatalf("two processes applied %d migrations", a+b)
				}
				var count int
				check(t, connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+q(history)).Scan(&count))
				if count != 1 {
					t.Fatalf("history contains %d entries", count)
				}
			})
			t.Run("lock timeout and process death", func(t *testing.T) {
				history, table := unique(), unique()
				cleanup(table, history, history+"_state")
				item := migration.Migration{Version: "20261104000000", Name: "hold", UpSQL: "CREATE TABLE " + q(table) + " (id INTEGER PRIMARY KEY)", DownSQL: "DROP TABLE " + q(table)}
				child := startMigrationChild(t, backend.driver, backend.dialect, dsn, history, "hold", item)
				child.line(t, "LOCKED")
				runner := newRunner(history, item)
				wait, stop := context.WithTimeout(ctx, 100*time.Millisecond)
				_, err := runner.Up(wait, 0)
				stop()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("lock wait did not cancel: %v", err)
				}
				check(t, child.command.Process.Kill())
				<-child.finished
				results, err := runner.Up(ctx, 0)
				check(t, err)
				if len(results) != 1 {
					t.Fatal("dead process lock not released")
				}
			})
			t.Run("death during DDL requires recovery", func(t *testing.T) {
				history, table := unique(), unique()
				cleanup(table, history, history+"_state")
				sleep := "SELECT pg_sleep(3)"
				if backend.dialect == db.MySQL {
					sleep = "SELECT SLEEP(3)"
				}
				item := migration.Migration{Version: "20261104000000", Name: "crash", UpSQL: "CREATE TABLE " + q(table) + " (id INTEGER PRIMARY KEY); " + sleep, DownSQL: "DROP TABLE " + q(table)}
				child := startMigrationChild(t, backend.driver, backend.dialect, dsn, history, "up", item)
				session, err := strconv.ParseInt(child.line(t, "SESSION "), 10, 64)
				check(t, err)
				poll, stop := context.WithTimeout(ctx, 10*time.Second)
				defer stop()
				for {
					var sleeping int
					query := "SELECT COUNT(*) FROM pg_stat_activity WHERE pid = $1 AND wait_event = 'PgSleep'"
					if backend.dialect == db.MySQL {
						query = "SELECT COUNT(*) FROM information_schema.processlist WHERE ID = ? AND STATE = 'User sleep'"
					}
					check(t, connection.QueryRowContext(poll, query, session).Scan(&sleeping))
					if sleeping == 1 {
						break
					}
					select {
					case <-poll.Done():
						t.Fatal("child never reached SQL sleep")
					case <-time.After(20 * time.Millisecond):
					}
				}
				check(t, child.command.Process.Kill())
				<-child.finished
				runner := newRunner(history, item)
				if _, err := runner.Up(ctx, 0); !errors.Is(err, migration.ErrDirty) {
					t.Fatalf("crashed migration retried: %v", err)
				}
				statuses, err := runner.Status(ctx)
				check(t, err)
				if len(statuses) != 1 || statuses[0].State != migration.StateDirty || statuses[0].Direction != "up" {
					t.Fatalf("missing dirty marker: %v", statuses)
				}
				var exists int
				query := "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1"
				if backend.dialect == db.MySQL {
					query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?"
				}
				check(t, connection.QueryRowContext(ctx, query, table).Scan(&exists))
				want := 0
				if backend.dialect == db.MySQL {
					want = 1
				}
				if exists != want {
					t.Fatalf("transactional DDL mismatch: table exists=%d, want=%d", exists, want)
				}
				if exists != 0 {
					_, err := connection.ExecContext(ctx, "DROP TABLE "+q(table))
					check(t, err)
				}
				check(t, runner.Resolve(ctx, item.ID(), item.Checksum(), false))
				// MySQL multi-statement execution is explicitly enabled only on this
				// migration connection. Re-running through a new child proves restart.
				restarted := startMigrationChild(t, backend.driver, backend.dialect, dsn, history, "up", item)
				restarted.wait(t)
				if restarted.line(t, "RESULT ") != "1" {
					t.Fatal("recovery did not allow restart")
				}
			})
		})
	}
}

func TestPostgreSQLExplicitNonTransactionalDDL(t *testing.T) {
	ctx := testContext(t)
	connection, err := db.Open(ctx, db.Config{Driver: "pgx", DSN: required(t, "POSTGRES_DSN"), Dialect: db.PostgreSQL, MaxOpenConns: 1, MaxIdleConns: 1}, db.NewDefaultDialectRegistry())
	check(t, err)
	t.Cleanup(func() { check(t, connection.Close()) })
	table, index, history := unique(), unique(), unique()
	q := connection.Dialect().QuoteIdentifier
	_, err = connection.ExecContext(ctx, "CREATE TABLE "+q(table)+" (id INTEGER)")
	check(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, name := range []string{table, history, history + "_state"} {
			_, err := connection.ExecContext(cleanup, "DROP TABLE IF EXISTS "+q(name))
			check(t, err)
		}
	})
	item := migration.Migration{Version: "20261104000000", Name: "index", UpSQL: "CREATE INDEX CONCURRENTLY " + q(index) + " ON " + q(table) + " (id)", DownSQL: "DROP INDEX CONCURRENTLY " + q(index), NonTransactional: true}
	runner, err := migration.NewRunner(connection, processSource{item: item}, migration.Options{Table: history})
	check(t, err)
	_, err = runner.Up(ctx, 0)
	check(t, err)
	_, err = runner.Down(ctx, 1)
	check(t, err)
}
