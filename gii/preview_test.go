package gii

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewDiffAndDuplicateDestinations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.go")
	check := func(content string, want string) {
		t.Helper()
		changes, err := Preview([]GeneratedFile{{Path: path, Content: []byte(content)}})
		if err != nil || len(changes) != 1 || changes[0].State != want {
			t.Fatalf("preview: %v %v", changes, err)
		}
	}
	check("new\n", "new")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preview wrote file")
	}
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	check("old\n", "unchanged")
	check("new\n", "changed")
	changes, err := Preview([]GeneratedFile{{Path: path, Content: []byte("new\n")}})
	if err != nil || !strings.Contains(changes[0].Diff, "-old\n+new") {
		t.Fatal(changes, err)
	}
	if _, err := WriteFiles([]GeneratedFile{{Path: path, Content: []byte("new")}}); err == nil {
		t.Fatal("existing file overwritten")
	}
	duplicate := filepath.Join(t.TempDir(), "duplicate")
	if _, err := WriteFiles([]GeneratedFile{{Path: duplicate}, {Path: duplicate}}); err == nil {
		t.Fatal("duplicate outputs accepted")
	}
	if _, err := os.Stat(duplicate); !os.IsNotExist(err) {
		t.Fatal("partial output on invalid plan")
	}
}
