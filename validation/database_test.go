package validation

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

func TestUniqueAndExistValidators(t *testing.T) {
	connection, recorder := openValidationConnection(t)
	defer connection.Close()
	database := DatabaseFrom(connection)
	model := &testInput{ID: 7, Email: "used@example.test"}

	recorder.push(true, nil)
	set := Set{Rules: []Rule{For([]string{"email"}, Unique{
		Database: database, Table: "users", Column: "email", Ignore: frameworkdb.Ne("id", model.ID),
	})}}
	errors_, err := set.Validate(context.Background(), model, "")
	if err != nil {
		t.Fatalf("Unique Validate() error = %v", err)
	}
	if violation, found := errors_.First("email"); !found || violation.Code != "unique" {
		t.Fatalf("Unique errors = %#v", errors_)
	}
	call := recorder.call(0)
	if call.query != `SELECT 1 FROM "users" WHERE ("email" = $1) AND ("id" <> $2) LIMIT 1` || !reflect.DeepEqual(call.args, []any{"used@example.test", int64(7)}) {
		t.Fatalf("Unique query = %q, %#v", call.query, call.args)
	}

	recorder.push(false, nil)
	set.Rules = []Rule{For([]string{"id"}, Exist{Database: database, Table: "users", Column: "id"})}
	errors_, err = set.Validate(context.Background(), model, "")
	if err != nil {
		t.Fatalf("Exist Validate() error = %v", err)
	}
	if violation, found := errors_.First("id"); !found || violation.Code != "exist" {
		t.Fatalf("Exist errors = %#v", errors_)
	}

	recorder.push(false, nil)
	set.Rules = []Rule{For([]string{"email"}, Unique{Database: database, Table: "users", Column: "email"})}
	errors_, err = set.Validate(context.Background(), model, "")
	if err != nil || !errors_.Valid() {
		t.Fatalf("available Unique errors = %#v, %v", errors_, err)
	}
}

func TestDatabaseValidatorPropagatesOperationalError(t *testing.T) {
	connection, recorder := openValidationConnection(t)
	defer connection.Close()
	databaseError := errors.New("database unavailable")
	recorder.push(false, databaseError)
	set := Set{Rules: []Rule{For([]string{"email"}, Unique{
		Database: DatabaseFrom(connection), Table: "users", Column: "email",
	})}}
	if _, err := set.Validate(context.Background(), &testInput{Email: "a@test"}, ""); !errors.Is(err, databaseError) {
		t.Fatalf("Validate() error = %v", err)
	}

	set.Rules = []Rule{For([]string{"email"}, Unique{Table: "users", Column: "email"})}
	if _, err := set.Validate(context.Background(), &testInput{Email: "a@test"}, ""); err == nil {
		t.Fatal("missing database error = nil")
	}
}

var validationDriverSequence atomic.Uint64

type validationCall struct {
	query string
	args  []any
}

type validationResponse struct {
	found bool
	err   error
}

type validationRecorder struct {
	mu        sync.Mutex
	calls     []validationCall
	responses []validationResponse
}

func (recorder *validationRecorder) push(found bool, err error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.responses = append(recorder.responses, validationResponse{found: found, err: err})
}

func (recorder *validationRecorder) call(index int) validationCall {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.calls[index]
}

func openValidationConnection(t *testing.T) (*frameworkdb.Connection, *validationRecorder) {
	t.Helper()
	recorder := &validationRecorder{}
	driverName := fmt.Sprintf("chitu-validation-test-%d", validationDriverSequence.Add(1))
	sql.Register(driverName, validationDriver{recorder: recorder})
	connection, err := frameworkdb.Open(context.Background(), frameworkdb.Config{
		Driver: driverName, Dialect: frameworkdb.PostgreSQL,
	}, frameworkdb.NewDefaultDialectRegistry())
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	return connection, recorder
}

type validationDriver struct{ recorder *validationRecorder }

func (driver_ validationDriver) Open(string) (driver.Conn, error) {
	return &validationConnection{recorder: driver_.recorder}, nil
}

type validationConnection struct{ recorder *validationRecorder }

func (*validationConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not supported")
}
func (*validationConnection) Close() error               { return nil }
func (*validationConnection) Begin() (driver.Tx, error)  { return nil, errors.New("not supported") }
func (*validationConnection) Ping(context.Context) error { return nil }
func (connection *validationConnection) QueryContext(_ context.Context, query string, values []driver.NamedValue) (driver.Rows, error) {
	connection.recorder.mu.Lock()
	defer connection.recorder.mu.Unlock()
	arguments := make([]any, len(values))
	for i, value := range values {
		arguments[i] = value.Value
	}
	connection.recorder.calls = append(connection.recorder.calls, validationCall{query: query, args: arguments})
	response := connection.recorder.responses[0]
	connection.recorder.responses = connection.recorder.responses[1:]
	if response.err != nil {
		return nil, response.err
	}
	return &validationRows{found: response.found}, nil
}

type validationRows struct {
	found bool
	read  bool
}

func (*validationRows) Columns() []string { return []string{"exists"} }
func (*validationRows) Close() error      { return nil }
func (rows *validationRows) Next(destination []driver.Value) error {
	if !rows.found || rows.read {
		return io.EOF
	}
	rows.read = true
	destination[0] = int64(1)
	return nil
}

var (
	_ driver.Driver         = validationDriver{}
	_ driver.Conn           = (*validationConnection)(nil)
	_ driver.Pinger         = (*validationConnection)(nil)
	_ driver.QueryerContext = (*validationConnection)(nil)
)
