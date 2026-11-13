package doccheck

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, path, content string) {
	t.Helper()
	path = filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLinksFencesReferencesAndAnchors(t *testing.T) {
	root := t.TempDir()
	write(t, root, "README.md", "# Guide\n[topic](docs/topic.md#запуск-api)\n[again][reference]\n[reference]: <docs/topic.md#запуск-api-1> \"Title\"\n[asset](docs/image%20%281%29.svg)\n[public](https://example.com/docs)\n[email](mailto:team@example.com)\n`[ignored](missing)`\n~~~text\n[ignored](missing)\n~~~\n")
	write(t, root, "docs/topic.md", "# Запуск API\n# Запуск API\n[home](../README.md#guide)\n```go file=../examples/guide/http/main.go\npackage main\n```\n")
	write(t, root, "docs/image (1).svg", "<svg/>")
	write(t, root, "examples/guide/http/main.go", "package main\r\n")
	report, err := Check(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Documents != 2 || report.Links != 6 || report.Snippets != 1 {
		t.Fatalf("report: %+v", report)
	}
}

func TestRejectsBrokenDocumentation(t *testing.T) {
	for _, tc := range []struct{ name, body, extra, reason string }{
		{"missing file", "[x](missing.md)", "", "missing target"},
		{"anchor", "# Good\n[x](#missing)", "", "missing heading"},
		{"case", "[x](readme.md)", "", ""},
		{"escape", "[x](../outside.md)", "", "escapes repository"},
		{"reference", "[x][unknown]", "", "undefined reference"},
		{"unclosed link", "[x](README.md", "", "unclosed link"},
		{"unclosed fence", "```go\npackage main", "", "unclosed code fence"},
		{"stale snippet", "```go file=source.go\nstale\n```", "source.go", "source fence differs"},
		{"empty source", "```go file=\n```", "", "empty source"},
		{"bad URL", "[x](https:///path)", "", "invalid or unsupported"},
		{"bad scheme", "[x](file:///tmp/secret)", "", "invalid or unsupported"},
		{"bad encoding", "[x](bad%zz.md)", "", "invalid URL"},
		{"scheme relative", "[x](//example.com/a)", "", "scheme-relative"},
		{"unfenced example", "# Guide", "examples/guide/http/main.go", "no source-backed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "README.md", tc.body+"\n")
			if tc.extra != "" {
				write(t, root, tc.extra, "package main\n")
			}
			_, err := Check(root)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("expected %q, got %v", tc.reason, err)
			}
		})
	}
}

func TestEmptyRootIgnoredArtifactsAndExternalSymlinks(t *testing.T) {
	root := t.TempDir()
	if _, err := Check(root); err == nil {
		t.Fatal("empty tree accepted")
	}
	write(t, root, "README.md", "# Main\n")
	write(t, root, ".cache/broken.md", "[x](nope)")
	write(t, root, "frontend/node_modules/broken.md", "```unclosed")
	if _, err := Check(root); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Log("symlink creation unavailable; Linux CI covers this case")
		return
	}
	write(t, root, "README.md", "[escape](escape.txt)\n")
	if _, err := Check(root); err == nil || !strings.Contains(err.Error(), "symlink escapes") {
		t.Fatalf("symlink boundary: %v", err)
	}
}

func TestRunFailsOnMissingSources(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/fixture\ngo 1.26.11\n")
	var output bytes.Buffer
	if err := Run(context.Background(), root, &output); err == nil {
		t.Fatal("missing source tree accepted")
	}
}

func TestRunPropagatesFailedExample(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module github.com/chitushka/framework\n\ngo 1.26.11\n")
	for _, name := range []string{"http", "jobs", "generate"} {
		write(t, root, "examples/guide/"+name+"/main.go", "package main\nfunc main() {}\n")
	}
	write(t, root, "examples/guide/http/main_test.go", "package main\nimport \"testing\"\nfunc TestFailure(t *testing.T){t.Fatal(\"intentional example failure\")}\n")
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := Run(ctx, root, &output); err == nil || !strings.Contains(output.String(), "intentional example failure") {
		t.Fatalf("example failure was not propagated: %v\n%s", err, output.String())
	}
}
