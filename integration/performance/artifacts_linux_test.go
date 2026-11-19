//go:build performance && linux

package performance_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPerformanceArtifactReadableByHost(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("run the artifact ownership regression in the performance container as root")
	}
	directory, err := os.MkdirTemp("", "chitu-performance-artifacts-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERF_OUTPUT_DIR", directory)
	writeJSON(t, "artifact.json", map[string]string{"kind": "public-performance-result"})
	reader := exec.CommandContext(t.Context(), "cat", filepath.Join(directory, "artifact.json"))
	reader.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	output, err := reader.CombinedOutput()
	if err != nil {
		t.Fatalf("host with another UID cannot read the container artifact: %v: %s", err, output)
	}
	var artifact map[string]string
	if err := json.Unmarshal(output, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact["kind"] != "public-performance-result" {
		t.Fatalf("artifact content differs: %s", output)
	}
}
