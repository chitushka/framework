package migration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	frameworkdb "github.com/chitushka/framework/db"
)

func TestCLICreate(t *testing.T) {
	directory := t.TempDir()
	var output bytes.Buffer
	cli := NewCLI()
	cli.Out = &output
	cli.Err = &output
	cli.Now = func() time.Time { return time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC) }
	if err := cli.Run(context.Background(), []string{"migration:create", "-dir", directory, "add-users"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	wantUp := filepath.Join(directory, "20261101090000_add_users.up.sql")
	wantDown := filepath.Join(directory, "20261101090000_add_users.down.sql")
	if !strings.Contains(output.String(), wantUp) || !strings.Contains(output.String(), wantDown) {
		t.Fatalf("Run() output = %q", output.String())
	}
}

func TestCLIValidation(t *testing.T) {
	cli := NewCLI()
	cli.Out = &bytes.Buffer{}
	cli.Err = &bytes.Buffer{}
	tests := [][]string{
		nil,
		{"unknown"},
		{"create"},
		{"up", "unexpected"},
		{"down", "-limit", "0"},
	}
	for _, args := range tests {
		if err := cli.Run(context.Background(), args); err == nil {
			t.Fatalf("Run(%q) error = nil", args)
		}
	}
}

func TestCLIMigrationLifecycle(t *testing.T) {
	directory := t.TempDir()
	base := filepath.Join(directory, "20261101090000_users")
	if err := os.WriteFile(base+".up.sql", []byte("UP users"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".down.sql", []byte("DOWN users"), 0o644); err != nil {
		t.Fatal(err)
	}
	recorder := &migrationRecorder{history: make(map[string]historyRecord)}
	var output bytes.Buffer
	cli := NewCLI()
	cli.Out = &output
	cli.Err = &output
	cli.Open = migrationTestOpen(t, frameworkdb.PostgreSQL, recorder)
	cli.Now = func() time.Time { return time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC) }

	if err := cli.Run(context.Background(), []string{"up", "-dir", directory, "-limit", "1"}); err != nil {
		t.Fatalf("up error = %v", err)
	}
	if !strings.Contains(output.String(), "applied 20261101090000_users") {
		t.Fatalf("up output = %q", output.String())
	}

	output.Reset()
	if err := cli.Run(context.Background(), []string{"status", "-dir", directory}); err != nil {
		t.Fatalf("status error = %v", err)
	}
	if !strings.Contains(output.String(), "applied") || !strings.Contains(output.String(), "20261101090000_users") {
		t.Fatalf("status output = %q", output.String())
	}

	output.Reset()
	if err := cli.Run(context.Background(), []string{"down", "-dir", directory}); err != nil {
		t.Fatalf("down error = %v", err)
	}
	if !strings.Contains(output.String(), "reverted 20261101090000_users") {
		t.Fatalf("down output = %q", output.String())
	}
}
