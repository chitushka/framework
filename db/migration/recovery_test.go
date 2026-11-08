package migration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	frameworkdb "github.com/chitushka/framework/db"
)

func TestDirtyBlocksRetriesAndRequiresExactRecovery(t *testing.T) {
	for _, dialect := range []string{frameworkdb.PostgreSQL, frameworkdb.MySQL} {
		t.Run(dialect, func(t *testing.T) {
			connection, recorder := openMigrationConnection(t, dialect)
			t.Cleanup(func() { _ = connection.Close() })
			connection.DB().SetMaxOpenConns(1)
			m := Migration{Version: "20261104000000", Name: "test", UpSQL: "BROKEN", DownSQL: "BROKEN DOWN"}
			runner, err := NewRunner(connection, memorySource{m}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			recorder.failQuery = m.UpSQL
			if _, err := runner.Up(t.Context(), 0); err == nil {
				t.Fatal("failed SQL accepted")
			}
			statuses, err := runner.Status(t.Context())
			if err != nil || len(statuses) != 1 || statuses[0].State != StateDirty || statuses[0].Direction != "up" || statuses[0].Checksum != m.Checksum() {
				t.Fatalf("dirty status: %v %v", statuses, err)
			}
			if _, err := runner.Up(t.Context(), 0); !errors.Is(err, ErrDirty) {
				t.Fatal("dirty retry accepted", err)
			}
			if _, err := runner.Down(t.Context(), 1); !errors.Is(err, ErrDirty) {
				t.Fatal("dirty down accepted", err)
			}
			if err := runner.Resolve(t.Context(), m.ID(), strings.Repeat("0", 64), false); err == nil {
				t.Fatal("wrong checksum accepted")
			}
			if err := runner.Resolve(t.Context(), m.ID(), m.Checksum(), false); err != nil {
				t.Fatal(err)
			}
			recorder.failQuery = ""
			if _, err := runner.Up(t.Context(), 0); err != nil {
				t.Fatal(err)
			}
			recorder.failQuery = m.DownSQL
			if _, err := runner.Down(t.Context(), 1); err == nil {
				t.Fatal("failed down accepted")
			}
			if err := runner.Resolve(t.Context(), m.ID(), m.Checksum(), true); err != nil {
				t.Fatal(err)
			}
			recorder.failQuery = ""
			if _, err := runner.Down(t.Context(), 1); err != nil {
				t.Fatal(err)
			}
			if len(recorder.historySnapshot()) != 0 || len(recorder.dirty) != 0 || recorder.owner != nil {
				t.Fatal("cleanup/history mismatch")
			}
		})
	}
}

func TestLockDeadlineAndUncertainUnlockDiscardConnection(t *testing.T) {
	for _, dialect := range []string{frameworkdb.PostgreSQL, frameworkdb.MySQL} {
		t.Run(dialect, func(t *testing.T) {
			connection, recorder := openMigrationConnection(t, dialect)
			t.Cleanup(func() { _ = connection.Close() })
			runner, err := NewRunner(connection, memorySource{}, Options{LockTimeout: 40 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			recorder.busy = true
			if _, err := runner.Up(t.Context(), 0); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if len(recorder.executed) != 0 || connection.Stats().InUse != 0 {
				t.Fatal("lock timeout executed SQL/leaked pinned connection")
			}
			recorder.busy = false
			recorder.failRelease = true
			if _, err := runner.Status(t.Context()); err == nil {
				t.Fatal("unlock failure ignored")
			}
			if recorder.owner != nil || connection.Stats().InUse != 0 {
				t.Fatal("uncertain lock returned to pool")
			}
			recorder.failRelease = false
			if _, err := runner.Status(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type cancelSource struct{ cancel context.CancelFunc }

func (s cancelSource) Migrations(context.Context) ([]Migration, error) {
	s.cancel()
	return nil, context.Canceled
}

func TestCancelledOperationStillReleasesLock(t *testing.T) {
	connection, recorder := openMigrationConnection(t, frameworkdb.PostgreSQL)
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runner, err := NewRunner(connection, cancelSource{cancel}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if recorder.owner != nil {
		t.Fatal("cancelled context prevented unlock")
	}
}

func TestRunnerRejectsUnsupportedLockingAndInvalidOptions(t *testing.T) {
	for _, dialect := range []string{frameworkdb.Oracle, frameworkdb.SQLServer} {
		connection, _ := openMigrationConnection(t, dialect)
		if _, err := NewRunner(connection, memorySource{}, Options{}); err == nil {
			t.Fatal("unsupported migration dialect accepted")
		}
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
	}
	connection, _ := openMigrationConnection(t, frameworkdb.PostgreSQL)
	t.Cleanup(func() { _ = connection.Close() })
	for _, options := range []Options{{Table: "one.two.three"}, {Table: strings.Repeat("x", 58)}, {LockTimeout: -1}, {ReleaseTimeout: -1}} {
		if _, err := NewRunner(connection, memorySource{}, options); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
}
