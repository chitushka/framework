package ar

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	frameworkdb "github.com/chitushka/framework/db"
)

type testUser struct {
	ID      int64  `db:"id,primary,auto"`
	Email   string `db:"email"`
	Version int64  `db:"version,version"`
}

func (testUser) TableName() string { return "users" }

type testPost struct {
	ID     int64  `db:"id,primary,auto"`
	UserID int64  `db:"user_id"`
	Title  string `db:"title"`
}

func (testPost) TableName() string { return "posts" }

func TestRepositoryInsertUpdateDeleteAndOptimisticLock(t *testing.T) {
	connection, recorder := openARConnection(t)
	defer func() { _ = connection.Close() }()
	repository, err := NewRepository[testUser](connection, Options{})
	if err != nil {
		t.Fatalf("NewRepository() error = %v", err)
	}
	user := &testUser{Email: "ada@example.test"}
	recorder.pushQuery(arQueryResponse{columns: []string{"id"}, rows: [][]driver.Value{{int64(7)}}})
	if err := repository.Insert(context.Background(), user); err != nil {
		t.Fatalf("Insert() error = %v", err)
	}
	if user.ID != 7 || user.Version != 1 {
		t.Fatalf("Insert() model = %#v", user)
	}
	assertARCall(t, recorder.queryCall(0), `INSERT INTO "users" ("email", "version") VALUES ($1, $2) RETURNING "id"`, []any{"ada@example.test", int64(1)})

	user.Email = "new@example.test"
	recorder.pushExec(arExecResponse{affected: 1})
	if err := repository.Update(context.Background(), user); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if user.Version != 2 {
		t.Fatalf("Update() version = %d", user.Version)
	}
	assertARCall(t, recorder.execCall(0), `UPDATE "users" SET "email" = $1, "version" = $2 WHERE ("id" = $3) AND ("version" = $4)`, []any{"new@example.test", int64(2), int64(7), int64(1)})

	recorder.pushExec(arExecResponse{affected: 0})
	if err := repository.Update(context.Background(), user); !errors.Is(err, ErrStaleModel) {
		t.Fatalf("stale Update() error = %v", err)
	}
	if user.Version != 2 {
		t.Fatalf("stale Update() did not restore version: %d", user.Version)
	}

	recorder.pushExec(arExecResponse{affected: 1})
	if err := repository.Delete(context.Background(), user); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	assertARCall(t, recorder.execCall(2), `DELETE FROM "users" WHERE ("id" = $1) AND ("version" = $2)`, []any{int64(7), int64(2)})
}

func TestRepositoryFindAndFinderImmutability(t *testing.T) {
	connection, recorder := openARConnection(t)
	defer func() { _ = connection.Close() }()
	repository, _ := NewRepository[testUser](connection, Options{})
	recorder.pushQuery(arQueryResponse{rows: [][]driver.Value{{int64(3), "a@test", int64(4)}}})
	user, err := repository.FindByPK(context.Background(), int64(3))
	if err != nil {
		t.Fatalf("FindByPK() error = %v", err)
	}
	if !reflect.DeepEqual(user, &testUser{ID: 3, Email: "a@test", Version: 4}) {
		t.Fatalf("FindByPK() = %#v", user)
	}
	assertARCall(t, recorder.queryCall(0), `SELECT "id", "email", "version" FROM "users" WHERE ("id" = $1) LIMIT 1`, []any{int64(3)})

	base := repository.Find().Where(frameworkdb.Eq("version", 4))
	limited := base.OrderBy("id", frameworkdb.SortDescending).Limit(2)
	recorder.pushQuery(arQueryResponse{})
	if _, err := base.All(context.Background()); err != nil {
		t.Fatal(err)
	}
	recorder.pushQuery(arQueryResponse{})
	if _, err := limited.All(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recorder.queryCall(1).query == recorder.queryCall(2).query {
		t.Fatal("Finder mutation changed the base query")
	}

	recorder.pushQuery(arQueryResponse{})
	if _, err := repository.Find().One(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("One() error = %v", err)
	}
}

func TestRelationsLazyAndEager(t *testing.T) {
	connection, recorder := openARConnection(t)
	defer func() { _ = connection.Close() }()
	posts, _ := NewRepository[testPost](connection, Options{})
	relation := Relation{LocalColumn: "id", ForeignColumn: "user_id"}

	recorder.pushQuery(arQueryResponse{rows: [][]driver.Value{{int64(10), int64(1), "one"}}})
	lazy, err := HasMany(context.Background(), &testUser{ID: 1}, posts, relation)
	if err != nil || len(lazy) != 1 || lazy[0].Title != "one" {
		t.Fatalf("HasMany() = %#v, %v", lazy, err)
	}

	recorder.pushQuery(arQueryResponse{rows: [][]driver.Value{
		{int64(10), int64(1), "one"},
		{int64(11), int64(1), "two"},
		{int64(12), int64(2), "three"},
	}})
	groups, err := LoadHasMany(context.Background(), []*testUser{{ID: 1}, {ID: 2}, nil}, posts, relation)
	if err != nil {
		t.Fatalf("LoadHasMany() error = %v", err)
	}
	if len(groups) != 3 || len(groups[0]) != 2 || len(groups[1]) != 1 || groups[2] != nil {
		t.Fatalf("LoadHasMany() = %#v", groups)
	}
	if got := recorder.queryCall(1); got.query != `SELECT "id", "user_id", "title" FROM "posts" WHERE "user_id" IN ($1, $2)` {
		t.Fatalf("eager relation query = %q", got.query)
	}

	recorder.pushQuery(arQueryResponse{rows: [][]driver.Value{{int64(13), int64(1), "only"}}})
	ones, err := LoadHasOne(context.Background(), []*testUser{{ID: 1}, {ID: 2}}, posts, relation)
	if err != nil || len(ones) != 2 || ones[0] == nil || ones[1] != nil {
		t.Fatalf("LoadHasOne() = %#v, %v", ones, err)
	}
}

func TestRepositoryValidation(t *testing.T) {
	connection, _ := openARConnection(t)
	defer func() { _ = connection.Close() }()
	if _, err := NewRepository[struct{ Value string }](connection, Options{}); err == nil {
		t.Fatal("repository without table error = nil")
	}
	type noKey struct{ Value string }
	repository, err := NewRepository[noKey](connection, Options{Table: "values"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Update(context.Background(), &noKey{}); !errors.Is(err, ErrMissingPrimaryKey) {
		t.Fatalf("Update() error = %v", err)
	}
	if _, err := repository.WithExecutor(nil); err == nil {
		t.Fatal("WithExecutor(nil) error = nil")
	}
	if _, err := repository.WithExecutor(connection); err != nil {
		t.Fatalf("WithExecutor() error = %v", err)
	}
	if _, err := (*Finder[noKey])(nil).All(context.Background()); err == nil {
		t.Fatal("nil Finder.All() error = nil")
	}
}

func TestRepositoryGeneratedIDsForBuiltInDialects(t *testing.T) {
	tests := []struct {
		name     string
		dialect  string
		query    string
		useQuery bool
	}{
		{"postgresql", frameworkdb.PostgreSQL, `INSERT INTO "users" ("email", "version") VALUES ($1, $2) RETURNING "id"`, true},
		{"mysql", frameworkdb.MySQL, "INSERT INTO `users` (`email`, `version`) VALUES (?, ?)", false},
		{"oracle", frameworkdb.Oracle, `INSERT INTO "users" ("email", "version") VALUES (:1, :2) RETURNING "id" INTO :3`, false},
		{"sqlserver", frameworkdb.SQLServer, `INSERT INTO [users] ([email], [version]) OUTPUT INSERTED.[id] VALUES (@p1, @p2)`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connection, recorder := openARConnectionDialect(t, tt.dialect)
			defer func() { _ = connection.Close() }()
			repository, _ := NewRepository[testUser](connection, Options{})
			if tt.useQuery {
				recorder.pushQuery(arQueryResponse{columns: []string{"id"}, rows: [][]driver.Value{{int64(9)}}})
			} else {
				recorder.pushExec(arExecResponse{lastID: 9, outID: 9, affected: 1})
			}
			model := &testUser{Email: "dialect@test"}
			if err := repository.Insert(context.Background(), model); err != nil {
				t.Fatalf("Insert() error = %v", err)
			}
			if model.ID != 9 || model.Version != 1 {
				t.Fatalf("Insert() model = %#v", model)
			}
			var call arCall
			if tt.useQuery {
				call = recorder.queryCall(0)
			} else {
				call = recorder.execCall(0)
			}
			if call.query != tt.query {
				t.Fatalf("Insert() query = %q, want %q", call.query, tt.query)
			}
		})
	}
}

func TestInsertFailureRestoresInitializedVersion(t *testing.T) {
	connection, recorder := openARConnection(t)
	defer func() { _ = connection.Close() }()
	repository, _ := NewRepository[testUser](connection, Options{})
	recorder.pushQuery(arQueryResponse{err: errors.New("insert failed")})
	model := &testUser{Email: "fail@test"}
	if err := repository.Insert(context.Background(), model); err == nil {
		t.Fatal("Insert() error = nil")
	}
	if model.Version != 0 {
		t.Fatalf("Insert() version = %d", model.Version)
	}
}

func TestSaveChoosesInsertForEmptyAutoKey(t *testing.T) {
	connection, recorder := openARConnection(t)
	defer func() { _ = connection.Close() }()
	repository, _ := NewRepository[testUser](connection, Options{})
	recorder.pushQuery(arQueryResponse{columns: []string{"id"}, rows: [][]driver.Value{{int64(5)}}})
	model := &testUser{Email: "save@test"}
	if err := repository.Save(context.Background(), model); err != nil || model.ID != 5 {
		t.Fatalf("Save() model = %#v, error = %v", model, err)
	}
}

func assertARCall(t *testing.T, call arCall, query string, args []any) {
	t.Helper()
	if call.query != query || !reflect.DeepEqual(call.args, args) {
		t.Fatalf("call = %q, %#v; want %q, %#v", call.query, call.args, query, args)
	}
}

var arDriverSequence atomic.Uint64

func openARConnection(t *testing.T) (*frameworkdb.Connection, *arRecorder) {
	return openARConnectionDialect(t, frameworkdb.PostgreSQL)
}

func openARConnectionDialect(t *testing.T, dialect string) (*frameworkdb.Connection, *arRecorder) {
	t.Helper()
	recorder := &arRecorder{}
	driverName := fmt.Sprintf("chitu-ar-test-%d", arDriverSequence.Add(1))
	sql.Register(driverName, arDriver{recorder: recorder})
	connection, err := frameworkdb.Open(context.Background(), frameworkdb.Config{
		Driver: driverName, Dialect: dialect,
	}, frameworkdb.NewDefaultDialectRegistry())
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	return connection, recorder
}

type arCall struct {
	query string
	args  []any
}

type arExecResponse struct {
	lastID   int64
	outID    int64
	affected int64
	err      error
}

type arQueryResponse struct {
	rows     [][]driver.Value
	columns  []string
	types    []string
	nullable []bool
	err      error
}

type arRecorder struct {
	mu             sync.Mutex
	execCalls      []arCall
	queryCalls     []arCall
	execResponses  []arExecResponse
	queryResponses []arQueryResponse
}

func (r *arRecorder) pushExec(response arExecResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.execResponses = append(r.execResponses, response)
}

func (r *arRecorder) pushQuery(response arQueryResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queryResponses = append(r.queryResponses, response)
}

func (r *arRecorder) execCall(index int) arCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.execCalls[index]
}

func (r *arRecorder) queryCall(index int) arCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queryCalls[index]
}

type arDriver struct{ recorder *arRecorder }

func (driver_ arDriver) Open(string) (driver.Conn, error) {
	return &arDriverConn{recorder: driver_.recorder}, nil
}

type arDriverConn struct{ recorder *arRecorder }

func (*arDriverConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (*arDriverConn) Close() error                        { return nil }
func (*arDriverConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }
func (*arDriverConn) Ping(context.Context) error          { return nil }

func (connection *arDriverConn) ExecContext(_ context.Context, query string, values []driver.NamedValue) (driver.Result, error) {
	connection.recorder.mu.Lock()
	defer connection.recorder.mu.Unlock()
	connection.recorder.execCalls = append(connection.recorder.execCalls, arCall{query: query, args: arNamedValues(values)})
	if len(connection.recorder.execResponses) == 0 {
		return nil, errors.New("missing exec response")
	}
	response := connection.recorder.execResponses[0]
	connection.recorder.execResponses = connection.recorder.execResponses[1:]
	if response.outID != 0 {
		for _, value := range values {
			if output, ok := value.Value.(sql.Out); ok {
				if destination, ok := output.Dest.(*int64); ok {
					*destination = response.outID
				}
			}
		}
	}
	return arResult{lastID: response.lastID, affected: response.affected}, response.err
}

func (*arDriverConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (connection *arDriverConn) QueryContext(_ context.Context, query string, values []driver.NamedValue) (driver.Rows, error) {
	connection.recorder.mu.Lock()
	defer connection.recorder.mu.Unlock()
	connection.recorder.queryCalls = append(connection.recorder.queryCalls, arCall{query: query, args: arNamedValues(values)})
	if len(connection.recorder.queryResponses) == 0 {
		return nil, errors.New("missing query response")
	}
	response := connection.recorder.queryResponses[0]
	connection.recorder.queryResponses = connection.recorder.queryResponses[1:]
	return &arRows{rows: response.rows, columns: response.columns, types: response.types, nullable: response.nullable}, response.err
}

func arNamedValues(values []driver.NamedValue) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value.Value
	}
	return result
}

type arResult struct{ lastID, affected int64 }

func (result arResult) LastInsertId() (int64, error) { return result.lastID, nil }
func (result arResult) RowsAffected() (int64, error) { return result.affected, nil }

type arRows struct {
	rows     [][]driver.Value
	columns  []string
	types    []string
	nullable []bool
	index    int
}

func (rows *arRows) Columns() []string {
	if len(rows.columns) > 0 {
		return rows.columns
	}
	return []string{"id", "value", "version"}
}
func (*arRows) Close() error { return nil }
func (rows *arRows) ColumnTypeDatabaseTypeName(index int) string {
	if index < len(rows.types) {
		return rows.types[index]
	}
	return ""
}
func (rows *arRows) ColumnTypeNullable(index int) (bool, bool) {
	if index < len(rows.nullable) {
		return rows.nullable[index], true
	}
	return false, false
}
func (rows *arRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.rows) {
		return io.EOF
	}
	copy(destination, rows.rows[rows.index])
	rows.index++
	return nil
}

var (
	_ driver.Driver            = arDriver{}
	_ driver.Conn              = (*arDriverConn)(nil)
	_ driver.Pinger            = (*arDriverConn)(nil)
	_ driver.ExecerContext     = (*arDriverConn)(nil)
	_ driver.QueryerContext    = (*arDriverConn)(nil)
	_ driver.NamedValueChecker = (*arDriverConn)(nil)
)
