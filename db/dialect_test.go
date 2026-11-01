package db_test

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/chitushka/framework/db"
)

func TestBuiltinDialects(t *testing.T) {
	registry := db.NewDefaultDialectRegistry()
	if want := []string{db.MySQL, db.Oracle, db.PostgreSQL, db.SQLServer}; !reflect.DeepEqual(registry.Names(), want) {
		t.Fatalf("unexpected dialect names: %v", registry.Names())
	}

	tests := []struct {
		name        string
		identifier  string
		quoted      string
		position    int
		placeholder string
	}{
		{db.PostgreSQL, `user"name`, `"user""name"`, 2, "$2"},
		{db.MySQL, "user`name", "`user``name`", 2, "?"},
		{db.Oracle, `user"name`, `"user""name"`, 2, ":2"},
		{db.SQLServer, "user]name", "[user]]name]", 2, "@p2"},
	}
	for _, test := range tests {
		dialect, exists := registry.Lookup(strings.ToUpper(test.name))
		if !exists {
			t.Fatalf("dialect %q was not found", test.name)
		}
		if quoted := dialect.QuoteIdentifier(test.identifier); quoted != test.quoted {
			t.Fatalf("%s quote: got %q, want %q", test.name, quoted, test.quoted)
		}
		if placeholder := dialect.Placeholder(test.position); placeholder != test.placeholder {
			t.Fatalf("%s placeholder: got %q, want %q", test.name, placeholder, test.placeholder)
		}
	}
}

func TestDialectRegistryExtensionAndConcurrency(t *testing.T) {
	registry := &db.DialectRegistry{}
	if err := registry.Register(testDialect{name: "custom"}); err != nil {
		t.Fatal(err)
	}
	if _, exists := registry.Lookup(" CUSTOM "); !exists {
		t.Fatal("custom dialect was not found case-insensitively")
	}
	if err := registry.Register(testDialect{name: "Custom"}); err == nil {
		t.Fatal("expected duplicate dialect error")
	}

	var wait sync.WaitGroup
	for index := range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			name := fmt.Sprintf("dialect-%d", index)
			if err := registry.Register(testDialect{name: name}); err != nil {
				t.Errorf("register %s: %v", name, err)
				return
			}
			if _, exists := registry.Lookup(name); !exists {
				t.Errorf("lookup %s failed", name)
			}
		}()
	}
	wait.Wait()
	if len(registry.Names()) != 21 {
		t.Fatalf("unexpected registry size: %d", len(registry.Names()))
	}
}

func TestDialectRegistryValidation(t *testing.T) {
	registry, err := db.NewDialectRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var typedNil *testDialect
	for name, dialect := range map[string]db.Dialect{
		"nil":       nil,
		"typed nil": typedNil,
		"empty name": testDialect{
			name: " ",
		},
	} {
		if err := registry.Register(dialect); err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}
	if _, err := registry.Require("missing"); err == nil {
		t.Fatal("expected missing dialect error")
	}
	var nilRegistry *db.DialectRegistry
	if err := nilRegistry.Register(testDialect{name: "custom"}); err == nil {
		t.Fatal("expected nil registry error")
	}
}

type testDialect struct {
	name string
}

func (dialect testDialect) Name() string                     { return dialect.name }
func (testDialect) QuoteIdentifier(identifier string) string { return "<" + identifier + ">" }
func (testDialect) Placeholder(position int) string          { return fmt.Sprintf("#%d", position) }
