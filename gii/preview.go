package gii

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type FileChange struct{ Path, State, Diff string }

// Preview is read-only. Diff uses a deterministic whole-file hunk.
func Preview(files []GeneratedFile) ([]FileChange, error) {
	seen := map[string]bool{}
	changes := make([]FileChange, 0, len(files))
	for _, file := range files {
		path, err := filepath.Abs(file.Path)
		if err != nil {
			return nil, err
		}
		identity := path
		if runtime.GOOS == "windows" {
			identity = strings.ToLower(path)
		}
		if seen[identity] {
			return nil, fmt.Errorf("gii: duplicate output: %s", path)
		}
		seen[identity] = true
		state := "new"
		var before []byte
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("gii: output is not a regular file: %s", path)
			}
			before, err = os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			state = "changed"
			if bytes.Equal(before, file.Content) {
				state = "unchanged"
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		var diff strings.Builder
		if state != "unchanged" {
			fmt.Fprintf(&diff, "--- %s\n+++ %s\n", path, path)
			oldLines := strings.Split(strings.TrimSuffix(string(before), "\n"), "\n")
			if len(before) == 0 {
				oldLines = nil
			}
			newLines := strings.Split(strings.TrimSuffix(string(file.Content), "\n"), "\n")
			if len(file.Content) == 0 {
				newLines = nil
			}
			fmt.Fprintf(&diff, "@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines))
			for _, line := range oldLines {
				fmt.Fprintf(&diff, "-%s\n", line)
			}
			for _, line := range newLines {
				fmt.Fprintf(&diff, "+%s\n", line)
			}
		}
		changes = append(changes, FileChange{path, state, diff.String()})
	}
	return changes, nil
}
