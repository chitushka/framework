//go:build e2e

package main

import (
	"crypto/ed25519"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Build and run the real entry point with external issuer material; no handler
// injection. Exercise config, JWT, CRUD, errors, rate limits and shutdown over TCP.
func TestProcessE2E(t *testing.T) {
	_, key := exampleTokens(t)
	keyFile := publicKeyFile(t, key.Public().(ed25519.PublicKey))
	binary := filepath.Join(t.TempDir(), "rest-api")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	cmd := exec.Command(binary)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(name, "REST_") && !strings.HasPrefix(name, "JWT_") && name != "APP_ENV" {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "APP_ENV=production", "REST_ADDRESS="+address, "REST_ALLOWED_HOSTS="+address, "REST_CLIENT_RATE=30", "REST_SUBJECT_RATE=20", "JWT_PUBLIC_KEY_FILE="+keyFile, "JWT_KEY_ID=test", "JWT_ISSUER=https://issuer.example", "JWT_AUDIENCE=articles")
	logFile, err := os.Create(filepath.Join(t.TempDir(), "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
			<-done
		}
	})
	client := &http.Client{Timeout: 3 * time.Second}
	base := "http://" + address
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := client.Get(base + "/openapi.json")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		select {
		case err := <-done:
			t.Fatalf("server exited: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("server not ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
	token := exampleToken(t, key, time.Now().Add(time.Minute))
	request := func(method, path, body, credential string, status int) http.Header {
		t.Helper()
		r, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != status {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, data)
		}
		if status >= 400 && !strings.Contains(string(data), `"code":"`+errorCode(status)+`"`) {
			t.Fatalf("error contract: %s", data)
		}
		return resp.Header
	}
	request("GET", "/v1/articles", "", "", 401)
	request("GET", "/v1/articles", "", token+"x", 401)
	request("GET", "/v1/articles", "", exampleToken(t, key, time.Now().Add(-time.Second)), 401)
	location := request("POST", "/v1/articles", `{"title":"E2E"}`, token, 201).Get("Location")
	if location != "/v1/articles/2" {
		t.Fatal(location)
	}
	request("GET", location, "", token, 200)
	request("PUT", location, `{"title":"Updated"}`, token, 200)
	request("GET", "/v1/articles?title=Updated&limit=1", "", token, 200)
	request("POST", "/v1/articles", `{"id":1,"title":"injection"}`, token, 400)
	request("DELETE", location, "", token, 204)
	request("GET", location, "", token, 404)
	request("PATCH", "/v1/articles/1", "", token, 405)
	request("GET", "/debug/pprof/", "", token, 404)
	request("POST", "/v1/articles", `{"title":"`+strings.Repeat("x", 5000)+`"}`, token, 413)
	request("POST", "/v1/articles", `{"title":"`+strings.Repeat("x", 9<<20)+`"}`, token, 413)
	// The server's outer resource layer rejects these before router/auth/quotas.
	badMedia, err := http.NewRequest("POST", base+"/v1/articles", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	badMedia.Header.Set("Content-Type", "not a media type")
	badResponse, err := client.Do(badMedia)
	if err != nil {
		t.Fatal(err)
	}
	badBody, err := io.ReadAll(badResponse.Body)
	_ = badResponse.Body.Close()
	if err != nil || badResponse.StatusCode != 415 || !strings.Contains(string(badBody), `"code":"unsupported_media_type"`) {
		t.Fatalf("server outer contract: %d %s %v", badResponse.StatusCode, badBody, err)
	}
	// Seven matched authenticated requests consumed the subject window; router
	// 405 and blocked debug paths do not run the Bearer/subject middleware.
	for range 13 {
		request("GET", "/v1/articles", "", token, 200)
	}
	if header := request("GET", "/v1/articles", "", token, 429); header.Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	if runtime.GOOS == "windows" {
		_ = cmd.Process.Kill()
		<-done
		return
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("shutdown exceeded budget")
	}
}
