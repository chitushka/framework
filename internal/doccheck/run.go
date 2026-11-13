package doccheck

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Run copies only onboarding sources into a new module; it never invokes commands
// found in Markdown. Dependencies must be prefetched with go mod download.
func Run(ctx context.Context, root string, out io.Writer) (result error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "chitu-guide-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(directory); result == nil {
			result = err
		}
	}()
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}
	version := ""
	for _, line := range strings.Split(string(mod), "\n") {
		if strings.HasPrefix(line, "go ") {
			version = strings.TrimSpace(strings.TrimPrefix(line, "go "))
		}
	}
	if version == "" {
		return fmt.Errorf("missing Go version in go.mod")
	}
	for _, name := range []string{"http", "jobs", "generate"} {
		source := filepath.Join(root, "examples", "guide", name)
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(directory, name), 0700); err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
				if entry.Type()&os.ModeSymlink != 0 {
					return fmt.Errorf("snippet symlink: %s", entry.Name())
				}
				data, err := os.ReadFile(filepath.Join(source, entry.Name()))
				if err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(directory, name, entry.Name()), data, 0600); err != nil {
					return err
				}
			}
		}
	}
	for _, args := range [][]string{
		{"mod", "init", "example.com/catalog"},
		{"mod", "edit", "-go=" + version, "-require=github.com/chitushka/framework@v0.0.0", "-replace=github.com/chitushka/framework=" + filepath.ToSlash(root)},
		{"mod", "tidy"},
		{"test", "-count=1", "./..."},
	} {
		command := exec.CommandContext(ctx, "go", args...)
		command.Dir = directory
		command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
		command.WaitDelay = time.Second
		command.Stdout, command.Stderr = out, out
		if err := command.Run(); err != nil {
			return fmt.Errorf("isolated onboarding %s: %w", strings.Join(args[:min(2, len(args))], " "), err)
		}
	}
	return nil
}
