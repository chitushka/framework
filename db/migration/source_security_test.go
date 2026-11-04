package migration

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMigrationSourceCannotFollowSymlinkOutsideDirectory(t *testing.T) {
	directory := t.TempDir()
	outside := filepath.Join(t.TempDir(), "external.sql")
	if err := os.WriteFile(outside, []byte("SELECT 'outside secret';"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "20261104120000_test.up.sql")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("Windows symlink privilege unavailable")
		}
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "20261104120000_test.down.sql"), []byte("SELECT 1;"), 0600); err != nil {
		t.Fatal(err)
	}
	if migrations, err := (DirectorySource{Path: directory}).Migrations(t.Context()); err == nil || len(migrations) != 0 {
		t.Fatal("migration source followed external symlink")
	}
}
