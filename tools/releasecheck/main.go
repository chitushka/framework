// releasecheck verifies version-tag installation using a private file GOPROXY.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/zip"
)

func command(dir string, env []string, name string, args ...string) {
	if name == "tar" && runtime.GOOS == "windows" {
		name = filepath.Join(os.Getenv("SystemRoot"), "System32", "tar.exe")
	}
	c := exec.Command(name, args...)
	c.Dir, c.Env, c.Stdout, c.Stderr = dir, env, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		log.Fatalf("%s: %v", name, err)
	}
}

func main() {
	source := flag.String("source", "", "clean archived release source")
	output := flag.String("output", "", "new verification directory")
	version := flag.String("version", "", "candidate version")
	flag.Parse()
	if err := run(*source, *output, *version); err != nil {
		log.Fatal(err)
	}
}

func run(source, output, version string) error {
	if source == "" || output == "" || module.CanonicalVersion(version) != version || !strings.Contains(version, "-rc.") {
		return fmt.Errorf("explicit source/output and canonical RC version required")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		return fmt.Errorf("output must not already exist")
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}
	output, _ = filepath.Abs(output)
	checkout := filepath.Join(output, "tagged-source")
	if err := os.CopyFS(checkout, os.DirFS(source)); err != nil {
		return err
	}
	env := os.Environ()
	command(checkout, env, "git", "init", "-q")
	command(checkout, env, "git", "add", ".")
	command(checkout, env, "git", "-c", "user.name=RC verification", "-c", "user.email=rc@localhost", "-c", "commit.gpgsign=false", "commit", "-qm", "isolated candidate snapshot")
	proxy := filepath.Join(output, "proxy")
	modules := []string{"", "adapters", "examples/advanced-admin"}
	for _, sub := range modules {
		path := "github.com/chitushka/framework"
		tag := version
		if sub != "" {
			path += "/" + sub
			tag = sub + "/" + version
		}
		command(checkout, env, "git", "tag", tag)
		// The proxy is populated from this exact isolated version tag.
		tagSource := filepath.Join(output, "export-"+strings.ReplaceAll(sub, "/", "_"))
		if err := os.MkdirAll(tagSource, 0o755); err != nil {
			return err
		}
		archive := filepath.Join(output, "export.tar")
		command(checkout, env, "git", "archive", "--format=tar", "-o", archive, tag)
		command(checkout, env, "tar", "-xf", archive, "-C", tagSource)
		moduleDir := filepath.Join(tagSource, sub)
		dir := filepath.Join(proxy, filepath.FromSlash(path), "@v")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		f, err := os.Create(filepath.Join(dir, version+".zip"))
		if err != nil {
			return err
		}
		err = zip.CreateFromDir(f, module.Version{Path: path, Version: version}, moduleDir)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		mod, err := os.ReadFile(filepath.Join(moduleDir, "go.mod"))
		if err != nil {
			return err
		}
		info, _ := json.Marshal(struct {
			Version string
			Time    time.Time
		}{version, time.Unix(0, 0).UTC()})
		for name, data := range map[string][]byte{version + ".mod": mod, version + ".info": info, "list": []byte(version + "\n")} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				return err
			}
		}
	}
	consumer := filepath.Join(output, "consumer")
	if err := os.MkdirAll(consumer, 0o755); err != nil {
		return err
	}
	code := `package consumer
import (
 "testing"
 "github.com/chitushka/framework/web"
 _ "github.com/chitushka/framework/adapters/redis"
 _ "github.com/chitushka/framework/adapters/rabbitmq"
 _ "github.com/chitushka/framework/adapters/otel"
)
func TestInstalledRouter(t *testing.T) { if web.NewRouter() == nil { t.Fatal("missing router") } }
`
	if err := os.WriteFile(filepath.Join(consumer, "installed_test.go"), []byte(code), 0o644); err != nil {
		return err
	}
	// Only project modules bypass the public checksum database. Other dependencies
	// continue to use the standard proxy and sum.golang.org validation.
	filtered := make([]string, 0, len(env)+4)
	for _, v := range env {
		if !strings.HasPrefix(v, "GOPROXY=") && !strings.HasPrefix(v, "GONOSUMDB=") && !strings.HasPrefix(v, "GOWORK=") && !strings.HasPrefix(v, "GOSUMDB=") && !strings.HasPrefix(v, "GOMODCACHE=") {
			filtered = append(filtered, v)
		}
	}
	uri := "file://" + filepath.ToSlash(proxy)
	if filepath.VolumeName(proxy) != "" {
		uri = "file:///" + filepath.ToSlash(proxy)
	}
	filtered = append(filtered, "GOWORK=off", "GOPROXY="+uri+",https://proxy.golang.org", "GONOSUMDB=github.com/chitushka/framework*", "GOSUMDB=sum.golang.org", "GOMODCACHE="+filepath.Join(output, "module-cache"))
	command(consumer, filtered, "go", "mod", "init", "example.com/rc-consumer")
	command(consumer, filtered, "go", "get", "github.com/chitushka/framework@"+version, "github.com/chitushka/framework/adapters@"+version)
	command(consumer, filtered, "go", "test", "-mod=readonly", "./...")
	command(consumer, filtered, "go", "mod", "verify")
	command(consumer, filtered, "go", "get", "github.com/chitushka/framework/examples/advanced-admin@"+version)
	command(consumer, filtered, "go", "build", "-mod=readonly", "-o", filepath.Join(output, "admin-from-version"), "github.com/chitushka/framework/examples/advanced-admin")
	command(consumer, filtered, "go", "mod", "verify")
	return nil
}
