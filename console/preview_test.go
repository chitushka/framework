package console

import (
	"context"
	frameworkdb "github.com/chitushka/framework/db"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGeneratorCLIPreviewDoesNotWrite(t *testing.T) {
	driver := registerToolDriver()
	open := func(ctx context.Context, config frameworkdb.Config) (*frameworkdb.Connection, error) {
		return frameworkdb.Open(ctx, config, frameworkdb.NewDefaultDialectRegistry())
	}
	for _, kind := range []string{"model", "crud", "migration"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "untouched")
			args := []string{"generate:" + kind, "-driver", driver, "-dialect", frameworkdb.PostgreSQL, "-pk", "id", "-auto", "id", "-dry-run"}
			switch kind {
			case "model":
				args = append(args, "-output", filepath.Join(root, "user.go"))
			case "crud":
				args = append(args, "-output", root, "-model-package", "example.com/app/model")
			case "migration":
				args = append(args, "-dir", root)
			}
			args = append(args, "users")
			output := runTool(t, open, time.Now, args)
			if !strings.Contains(output, "new ") {
				t.Fatalf("missing preview: %s", output)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("dry-run created output")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "user.go")
	old := []byte("package old\n")
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	output := runTool(t, open, time.Now, []string{"generate:model", "-driver", driver, "-dialect", frameworkdb.PostgreSQL, "-pk", "id", "-auto", "id", "-output", path, "-diff", "users"})
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(old) || !strings.Contains(output, "-package old") {
		t.Fatalf("diff wrote/missed existing file: %s %v", output, err)
	}
}
