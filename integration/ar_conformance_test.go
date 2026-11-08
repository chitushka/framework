package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/db/ar"
)

type arRow struct {
	ID      int64  `db:"id,primary,auto"`
	Name    string `db:"name"`
	Version int64  `db:"version,version"`
}

func (m *arRow) Validate(context.Context) error {
	if m.Name == "invalid" {
		return errors.New("invalid name")
	}
	return nil
}

type arChild struct {
	Tenant   int64  `db:"tenant,primary"`
	ID       int64  `db:"id,primary"`
	ParentID int64  `db:"parent_id"`
	Value    string `db:"value"`
}

type countingExecutor struct {
	ar.Executor
	queries atomic.Int32
}

func (e *countingExecutor) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	e.queries.Add(1)
	return e.Executor.QueryContext(ctx, q, args...)
}

func TestActiveRecordConformance(t *testing.T) {
	sqlBaseline(t, func(t *testing.T, c *db.Connection) {
		ctx := testContext(t)
		id := "BIGSERIAL PRIMARY KEY"
		if c.Dialect().Name() == db.MySQL {
			id = "BIGINT AUTO_INCREMENT PRIMARY KEY"
		}
		table := sqlTable(t, c, "id "+id+", name VARCHAR(255) NOT NULL, version BIGINT NOT NULL")
		repo, err := ar.NewRepository[arRow](c, ar.Options{Table: table})
		check(t, err)
		first := &arRow{Name: "first"}
		second := &arRow{Name: "second"}
		check(t, repo.Insert(ctx, first))
		check(t, repo.Insert(ctx, second))
		if first.ID < 1 || second.ID <= first.ID || first.Version != 1 {
			t.Fatalf("IDs/version: %+v %+v", first, second)
		}
		a, err := repo.FindByPK(ctx, first.ID)
		check(t, err)
		b, err := repo.FindByPK(ctx, first.ID)
		check(t, err)
		a.Name, b.Name = "winner-a", "winner-b"
		gate := make(chan struct{})
		outcomes := make(chan error, 2)
		var group sync.WaitGroup
		for _, model := range []*arRow{a, b} {
			group.Add(1)
			go func() { defer group.Done(); <-gate; outcomes <- repo.Update(ctx, model) }()
		}
		close(gate)
		group.Wait()
		close(outcomes)
		successes, conflicts := 0, 0
		for err := range outcomes {
			if err == nil {
				successes++
			} else if errors.Is(err, ar.ErrStaleModel) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("optimistic results: %d/%d", successes, conflicts)
		}
		fresh, err := repo.FindByPK(ctx, first.ID)
		check(t, err)
		if fresh.Version != 2 {
			t.Fatalf("version = %d", fresh.Version)
		}
		invalid := &arRow{Name: "invalid"}
		if err := repo.Insert(ctx, invalid); err == nil || invalid.ID != 0 || invalid.Version != 0 {
			t.Fatalf("invalid insert mutated model: %+v %v", invalid, err)
		}
		abort := errors.New("rollback service")
		err = c.Transaction(ctx, nil, func(tx *db.Transaction) error {
			transactional, err := repo.WithExecutor(tx)
			if err != nil {
				return err
			}
			if err := transactional.Insert(ctx, &arRow{Name: "rolled back"}); err != nil {
				return err
			}
			return abort
		})
		if !errors.Is(err, abort) {
			t.Fatal(err)
		}
		parents, err := repo.Find().OrderBy("id", db.SortAscending).All(ctx)
		check(t, err)
		if len(parents) != 2 {
			t.Fatalf("partial transaction remained: %d", len(parents))
		}
		page, err := repo.Find().OrderBy("id", db.SortAscending).Limit(1).Offset(1).All(ctx)
		check(t, err)
		if len(page) != 1 || page[0].ID != second.ID {
			t.Fatalf("pagination: %+v", page)
		}
		childTable := sqlTable(t, c, "tenant BIGINT, id BIGINT, parent_id BIGINT, value VARCHAR(255), PRIMARY KEY (tenant,id)")
		children, err := ar.NewRepository[arChild](c, ar.Options{Table: childTable})
		check(t, err)
		for _, child := range []*arChild{{Tenant: 1, ID: 1, ParentID: first.ID, Value: "a"}, {Tenant: 1, ID: 2, ParentID: first.ID, Value: "b"}, {Tenant: 2, ID: 1, ParentID: second.ID, Value: "c"}} {
			check(t, children.Insert(ctx, child))
		}
		composite, err := children.FindByPK(ctx, 2, 1)
		check(t, err)
		if composite.Value != "c" {
			t.Fatalf("composite lookup: %+v", composite)
		}
		counter := &countingExecutor{Executor: c}
		counted, err := children.WithExecutor(counter)
		check(t, err)
		groups, err := ar.LoadHasMany(ctx, parents, counted, ar.Relation{LocalColumn: "id", ForeignColumn: "parent_id"})
		check(t, err)
		if counter.queries.Load() != 1 || len(groups[0]) != 2 || len(groups[1]) != 1 {
			t.Fatalf("eager query count/groups: %d %v", counter.queries.Load(), groups)
		}
		one, err := ar.HasOne(ctx, composite, repo, ar.Relation{LocalColumn: "parent_id", ForeignColumn: "id"})
		check(t, err)
		if one.ID != second.ID {
			t.Fatal("lazy relation mismatch")
		}
	})
}
