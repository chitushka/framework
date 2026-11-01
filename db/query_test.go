package db

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

func TestQueryBuilderBuildsSelectWithSafeParameters(t *testing.T) {
	injection := "admin' OR 1=1 --"
	query, args, err := NewQueryBuilder(queryTestDialect(PostgreSQL)).
		Select("u.id").
		SelectAs("u.email", "login").
		SelectRawAs("COUNT(*)", "orders").
		Distinct().
		From("users", "u").
		Join(JoinLeft, "orders", "o", RawCondition("o.user_id = u.id AND o.total > ?", 100)).
		Where(Eq("u.email", injection)).
		AndWhere(In("u.status", "active", "pending")).
		GroupBy("u.id", "u.email").
		Having(Gt("u.id", 0)).
		OrderBy("u.email", SortDescending).
		Limit(25).
		Offset(50).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantQuery := `SELECT DISTINCT "u"."id", "u"."email" AS "login", COUNT(*) AS "orders" FROM "users" AS "u" LEFT JOIN "orders" AS "o" ON o.user_id = u.id AND o.total > $1 WHERE ("u"."email" = $2) AND ("u"."status" IN ($3, $4)) GROUP BY "u"."id", "u"."email" HAVING "u"."id" > $5 ORDER BY "u"."email" DESC LIMIT 25 OFFSET 50`
	if query != wantQuery {
		t.Fatalf("Build() query\n got: %s\nwant: %s", query, wantQuery)
	}
	wantArgs := []any{100, injection, "active", "pending", 0}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("Build() args = %#v, want %#v", args, wantArgs)
	}
	if contains(query, injection) {
		t.Fatal("Build() interpolated a parameter into SQL")
	}
}

func TestQueryBuilderIsImmutable(t *testing.T) {
	base := NewQueryBuilder(queryTestDialect(PostgreSQL)).Select("id").From("users")
	active := base.Where(Eq("active", true))
	limited := active.Limit(10)

	tests := []struct {
		name    string
		builder *QueryBuilder
		query   string
		args    []any
	}{
		{"base", base, `SELECT "id" FROM "users"`, nil},
		{"active", active, `SELECT "id" FROM "users" WHERE "active" = $1`, []any{true}},
		{"limited", limited, `SELECT "id" FROM "users" WHERE "active" = $1 LIMIT 10`, []any{true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, args, err := tt.builder.Build()
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if query != tt.query || !reflect.DeepEqual(args, tt.args) {
				t.Fatalf("Build() = %q, %#v; want %q, %#v", query, args, tt.query, tt.args)
			}
		})
	}

}

func TestQueryBuilderUsesDialectPlaceholdersAndPagination(t *testing.T) {
	tests := []struct {
		name    string
		dialect Dialect
		query   string
	}{
		{"postgresql", queryTestDialect(PostgreSQL), `SELECT "id" FROM "users" WHERE "role" = $1 ORDER BY "id" ASC LIMIT 10 OFFSET 5`},
		{"mysql", queryTestDialect(MySQL), "SELECT `id` FROM `users` WHERE `role` = ? ORDER BY `id` ASC LIMIT 10 OFFSET 5"},
		{"oracle", queryTestDialect(Oracle), `SELECT "id" FROM "users" WHERE "role" = :1 ORDER BY "id" ASC OFFSET 5 ROWS FETCH NEXT 10 ROWS ONLY`},
		{"sqlserver", queryTestDialect(SQLServer), `SELECT [id] FROM [users] WHERE [role] = @p1 ORDER BY [id] ASC OFFSET 5 ROWS FETCH NEXT 10 ROWS ONLY`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, args, err := NewQueryBuilder(tt.dialect).
				Select("id").
				From("users").
				Where(Eq("role", "admin")).
				OrderBy("id", SortAscending).
				Limit(10).
				Offset(5).
				Build()
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if query != tt.query {
				t.Fatalf("Build() query = %q, want %q", query, tt.query)
			}
			if !reflect.DeepEqual(args, []any{"admin"}) {
				t.Fatalf("Build() args = %#v", args)
			}
		})
	}

	t.Run("sqlserver limit without offset", func(t *testing.T) {
		query, _, err := NewQueryBuilder(queryTestDialect(SQLServer)).
			Select("id").
			From("users").
			Limit(0).
			Build()
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		if query != `SELECT TOP (0) [id] FROM [users]` {
			t.Fatalf("Build() query = %q", query)
		}
	})
}

func TestQueryBuilderBuildsWritesDeterministically(t *testing.T) {
	t.Run("insert", func(t *testing.T) {
		query, args, err := NewQueryBuilder(queryTestDialect(PostgreSQL)).
			Insert("users", map[string]any{"name": "Ada", "age": 37}).
			Build()
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		if query != `INSERT INTO "users" ("age", "name") VALUES ($1, $2)` {
			t.Fatalf("Build() query = %q", query)
		}
		if !reflect.DeepEqual(args, []any{37, "Ada"}) {
			t.Fatalf("Build() args = %#v", args)
		}
	})

	t.Run("update", func(t *testing.T) {
		query, args, err := NewQueryBuilder(queryTestDialect(MySQL)).
			Update("users", map[string]any{"name": "Grace", "active": true}).
			Where(Eq("id", 7)).
			Build()
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		if query != "UPDATE `users` SET `active` = ?, `name` = ? WHERE `id` = ?" {
			t.Fatalf("Build() query = %q", query)
		}
		if !reflect.DeepEqual(args, []any{true, "Grace", 7}) {
			t.Fatalf("Build() args = %#v", args)
		}
	})

	t.Run("delete", func(t *testing.T) {
		query, args, err := NewQueryBuilder(queryTestDialect(SQLServer)).
			Delete("sessions").
			Where(In("id", 1, 2)).
			Build()
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		if query != `DELETE FROM [sessions] WHERE [id] IN (@p1, @p2)` {
			t.Fatalf("Build() query = %q", query)
		}
		if !reflect.DeepEqual(args, []any{1, 2}) {
			t.Fatalf("Build() args = %#v", args)
		}
	})
}

func TestQueryBuilderRejectsUnsafeOrInvalidQueries(t *testing.T) {
	tests := []struct {
		name    string
		builder *QueryBuilder
	}{
		{"nil dialect", NewQueryBuilder(nil).Select("id")},
		{"negative limit", NewQueryBuilder(queryTestDialect(PostgreSQL)).Select("id").Limit(-1)},
		{"invalid direction", NewQueryBuilder(queryTestDialect(PostgreSQL)).Select("id").OrderBy("id", SortDirection(99))},
		{"raw missing argument", NewQueryBuilder(queryTestDialect(PostgreSQL)).SelectRaw("COALESCE(?, ?)", 1)},
		{"mass update", NewQueryBuilder(queryTestDialect(PostgreSQL)).Update("users", map[string]any{"active": false})},
		{"mass delete", NewQueryBuilder(queryTestDialect(PostgreSQL)).Delete("users")},
		{"sqlserver offset without order", NewQueryBuilder(queryTestDialect(SQLServer)).Select("id").Offset(1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := tt.builder.Build(); err == nil {
				t.Fatal("Build() error = nil")
			}
		})
	}

	query, _, err := NewQueryBuilder(queryTestDialect(PostgreSQL)).
		Update("users", map[string]any{"active": false}).
		AllRows().
		Build()
	if err != nil || query != `UPDATE "users" SET "active" = $1` {
		t.Fatalf("AllRows Build() = %q, %v", query, err)
	}
}

func TestConditionsHandleNullAndEmptyLists(t *testing.T) {
	query, args, err := NewQueryBuilder(queryTestDialect(PostgreSQL)).
		Select().
		From("users").
		Where(And(Eq("deleted_at", nil), Ne("email", nil), In("id"), NotIn("role"))).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	want := `SELECT * FROM "users" WHERE ("deleted_at" IS NULL) AND ("email" IS NOT NULL) AND (1 = 0) AND (1 = 1)`
	if query != want || len(args) != 0 {
		t.Fatalf("Build() = %q, %#v; want %q, no args", query, args, want)
	}
}

func TestQueryBuilderExecutesThroughInterfaces(t *testing.T) {
	executor := &recordingExecutor{}
	rows, err := NewQueryBuilder(queryTestDialect(PostgreSQL)).
		Select("id").
		From("users").
		Where(Eq("id", 3)).
		QueryContext(context.Background(), executor)
	if !errors.Is(err, errRecordedQuery) || rows != nil {
		t.Fatalf("QueryContext() = %v, %v", rows, err)
	}
	if executor.query != `SELECT "id" FROM "users" WHERE "id" = $1` || !reflect.DeepEqual(executor.args, []any{3}) {
		t.Fatalf("recorded query = %q, %#v", executor.query, executor.args)
	}

	executor = &recordingExecutor{}
	result, err := NewQueryBuilder(queryTestDialect(PostgreSQL)).
		Delete("users").
		Where(Eq("id", 4)).
		ExecContext(context.Background(), executor)
	if !errors.Is(err, errRecordedExec) || result != nil {
		t.Fatalf("ExecContext() = %v, %v", result, err)
	}
	if executor.query != `DELETE FROM "users" WHERE "id" = $1` || !reflect.DeepEqual(executor.args, []any{4}) {
		t.Fatalf("recorded exec = %q, %#v", executor.query, executor.args)
	}
}

var (
	errRecordedQuery = errors.New("recorded query")
	errRecordedExec  = errors.New("recorded exec")
)

type recordingExecutor struct {
	query string
	args  []any
}

func (e *recordingExecutor) QueryContext(_ context.Context, query string, args ...any) (*sql.Rows, error) {
	e.query = query
	e.args = append([]any(nil), args...)
	return nil, errRecordedQuery
}

func (e *recordingExecutor) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	e.query = query
	e.args = append([]any(nil), args...)
	return nil, errRecordedExec
}

func contains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}

func queryTestDialect(name string) Dialect {
	for _, dialect := range BuiltinDialects() {
		if dialect.Name() == name {
			return dialect
		}
	}
	panic("missing built-in query test dialect: " + name)
}
