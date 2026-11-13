package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGeneratedProjectCompilesAndPreservesInputPolicy(t *testing.T) {
	directory := t.TempDir()
	if err := generate(directory); err != nil {
		t.Fatal(err)
	}
	if err := generate(directory); err == nil {
		t.Fatal("overwrite accepted")
	}
	// Locate framework from the actual module graph, also inside the isolated
	// onboarding module created by tools/checkdocs -run.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	locate := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}", "github.com/chitushka/framework")
	location, err := locate.Output()
	if err != nil {
		t.Fatal(err)
	}
	root := strings.TrimSpace(string(location))
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	version := ""
	for _, line := range strings.Split(string(mod), "\n") {
		if strings.HasPrefix(line, "go ") {
			version = strings.TrimSpace(strings.TrimPrefix(line, "go "))
		}
	}
	module := fmt.Sprintf("module example.com/catalog\n\ngo %s\n\nrequire github.com/chitushka/framework v0.0.0\nreplace github.com/chitushka/framework => %q\n", version, filepath.ToSlash(root))
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0600); err != nil {
		t.Fatal(err)
	}
	source := `package controller
import("testing";"example.com/catalog/model")
func TestInputPolicy(t *testing.T){name:="updated";m:=model.Product{ID:7,Name:"old",Price:"12.30",Version:2};input:=ProductInput{Name:&name};input.Apply(&m);if m.ID!=7||m.Version!=2||m.Price!="12.30"||m.Name!="updated"{t.Fatalf("invalid mutation: %+v",m)}}
`
	if err := os.WriteFile(filepath.Join(directory, "controller", "policy_test.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "go", "test", "-mod=mod", "./...")
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	cmd.WaitDelay = time.Second
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated project: %v\n%s", err, output)
	}
	migrations, err := filepath.Glob(filepath.Join(directory, "migrations", "*.up.sql"))
	if err != nil || len(migrations) != 1 {
		t.Fatal("missing up migration", err)
	}
	ddl, err := os.ReadFile(migrations[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ddl), "DECIMAL(12,2)") || !strings.Contains(string(ddl), "BIGSERIAL") {
		t.Fatal(string(ddl))
	}
}
