package migration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateAndDirectorySource(t *testing.T) {
	directory := t.TempDir()
	createdAt := time.Date(2026, 11, 1, 12, 34, 56, 0, time.FixedZone("test", 3*60*60))
	files, err := Create(directory, "Create Users!", createdAt)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if filepath.Base(files.Up) != "20261101093456_create_users.up.sql" || filepath.Base(files.Down) != "20261101093456_create_users.down.sql" {
		t.Fatalf("Create() files = %#v", files)
	}
	if _, err := Create(directory, "Create Users!", createdAt); err == nil {
		t.Fatal("second Create() error = nil")
	}
	if err := os.WriteFile(files.Up, []byte("CREATE TABLE users (id INT);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files.Down, []byte("DROP TABLE users;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	migrations, err := (DirectorySource{Path: directory}).Migrations(context.Background())
	if err != nil {
		t.Fatalf("Migrations() error = %v", err)
	}
	if len(migrations) != 1 || migrations[0].ID() != "20261101093456_create_users" {
		t.Fatalf("Migrations() = %#v", migrations)
	}
	if len(migrations[0].Checksum()) != 64 {
		t.Fatalf("Checksum() = %q", migrations[0].Checksum())
	}
}

func TestDirectorySourceRejectsIncompleteAndInvalidFiles(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "20261101000000_test.up.sql"), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (DirectorySource{Path: directory}).Migrations(context.Background()); err == nil || !strings.Contains(err.Error(), "both up and down") {
		t.Fatalf("incomplete Migrations() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "bad.sql"), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (DirectorySource{Path: directory}).Migrations(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid SQL migration file") {
		t.Fatalf("invalid Migrations() error = %v", err)
	}
}

func TestCreateRejectsInvalidName(t *testing.T) {
	if _, err := Create(t.TempDir(), "123", time.Now()); err == nil {
		t.Fatal("Create() error = nil")
	}
}

func TestChecksumIgnoresPlatformLineEndings(t *testing.T) {
	unix := Migration{UpSQL: "CREATE TABLE users;\n", DownSQL: "DROP TABLE users;\n"}
	windows := Migration{UpSQL: "CREATE TABLE users;\r\n", DownSQL: "DROP TABLE users;\r\n"}
	if unix.Checksum() != windows.Checksum() {
		t.Fatalf("checksums differ: %s != %s", unix.Checksum(), windows.Checksum())
	}
}

func TestNonTransactionalDirectiveAndChecksum(t *testing.T) {
	directory := t.TempDir()
	base := filepath.Join(directory, "20261108000000_index")
	for _, direction := range []string{"up", "down"} {
		if err := os.WriteFile(base+"."+direction+".sql", []byte("-- chitu:nontransactional\r\nSELECT 1;\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	items, err := (DirectorySource{Path: directory}).Migrations(t.Context())
	if err != nil || len(items) != 1 || !items[0].NonTransactional {
		t.Fatalf("directive: %v %v", items, err)
	}
	transactional := items[0]
	transactional.NonTransactional = false
	if transactional.Checksum() == items[0].Checksum() {
		t.Fatal("execution mode must affect checksum")
	}
}

func TestDirectorySourceRejectsUneditedTemplate(t *testing.T) {
	directory := t.TempDir()
	if _, err := Create(directory, "users", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := (DirectorySource{Path: directory}).Migrations(context.Background()); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("Migrations() error = %v", err)
	}
}
