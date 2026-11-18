// These tests are copied unchanged into an external module installed from the
// frozen version tag. They may import public packages only.
package compatibility_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/chitushka/framework/asset"
	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/cache"
	"github.com/chitushka/framework/config"
	"github.com/chitushka/framework/console"
	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/db/migration"
	"github.com/chitushka/framework/queue"
	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/validation"
	"github.com/chitushka/framework/web"
)

var _ session.AtomicBackend = (*session.MemoryBackend)(nil)
var _ queue.DeadLetterStore = (*queue.FileDeadLetters)(nil)
var _ queue.Broker = (*queue.MemoryBroker)(nil)
var _ cache.Backend = (*cache.MemoryBackend)(nil)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicHTTPAndSQLContracts(t *testing.T) {
	router := web.NewRouter()
	must(t, router.GET("/items/{id}", func(c *web.Context) error { return c.JSON(200, map[string]string{"id": c.Param("id")}) }, web.WithName("item")))
	must(t, router.GET("/failure", func(*web.Context) error { return errors.New("private-database-detail") }))
	url, err := router.URL("item", map[string]string{"id": "café space"})
	must(t, err)
	if url != "/items/caf%C3%A9%20space" {
		t.Fatal(url)
	}
	handler, err := web.NewProductionHandler(router)
	must(t, err)
	for _, test := range []struct {
		method, path string
		code         int
		body         string
	}{
		{"GET", url, 200, `{"id":"café space"}`}, {"HEAD", url, 200, ""},
		{"OPTIONS", url, 204, ""}, {"POST", url, 405, ""}, {"GET", "/failure", 500, ""},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.code {
			t.Fatalf("%s %s: %d", test.method, test.path, response.Code)
		}
		if test.body != "" && strings.TrimSpace(response.Body.String()) != test.body {
			t.Fatal(response.Body.String())
		}
		if test.method == "HEAD" && response.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
		if test.code == 405 && response.Header().Get("Allow") != "GET, HEAD, OPTIONS" {
			t.Fatal(response.Header())
		}
		if strings.Contains(response.Body.String(), "private-database-detail") {
			t.Fatal("private error leaked")
		}
	}
	for _, test := range []struct{ dialect, sql string }{
		{db.PostgreSQL, `SELECT "id" FROM "users" WHERE "name" = $1 LIMIT 10`},
		{db.MySQL, "SELECT `id` FROM `users` WHERE `name` = ? LIMIT 10"},
	} {
		dialect, err := db.NewDefaultDialectRegistry().Require(test.dialect)
		must(t, err)
		query, args, err := db.NewQueryBuilder(dialect).Select("id").From("users").Where(db.Eq("name", "a' OR 1=1 --")).Limit(10).Build()
		must(t, err)
		if query != test.sql || !reflect.DeepEqual(args, []any{"a' OR 1=1 --"}) {
			t.Fatalf("SQL contract: %s %#v", query, args)
		}
	}
}

func TestAcceptedSessionV1FormatAndCAS(t *testing.T) {
	ctx := context.Background()
	backend := session.NewMemoryBackend()
	manager, err := session.New(backend, session.Config{Secure: true})
	must(t, err)
	id := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	now := time.Now().UTC().Add(-time.Second)
	// A v1 record from the accepted RC, including its revision and timestamps.
	record, err := json.Marshal(map[string]any{"v": 1, "id": id, "revision": id, "created": now, "touched": now, "values": map[string]any{"name": "Alice"}})
	must(t, err)
	must(t, backend.Save(ctx, id, record, time.Hour))
	router := web.NewRouter()
	must(t, router.Use(manager.Middleware))
	must(t, router.GET("/profile", func(c *web.Context) error {
		var name string
		found, err := session.Must(c.Request.Context()).Get("name", &name)
		if err != nil {
			return err
		}
		if !found || name != "Alice" {
			return errors.New("v1 session not read")
		}
		if err := session.Must(c.Request.Context()).Set("counter", 1); err != nil {
			return err
		}
		return c.JSON(200, map[string]string{"name": name})
	}))
	request := httptest.NewRequest("GET", "https://example.test/profile", nil)
	request.AddCookie(&http.Cookie{Name: "chitu_session", Value: id})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	result := response.Result()
	defer func() { _ = result.Body.Close() }()
	for _, cookie := range result.Cookies() {
		if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
			t.Fatal("cookie policy changed")
		}
	}
	updated, found, err := backend.Load(ctx, id)
	must(t, err)
	if !found {
		t.Fatal("session lost")
	}
	var fields map[string]json.RawMessage
	must(t, json.Unmarshal(updated, &fields))
	if string(fields["v"]) != "1" || len(fields) != 6 {
		t.Fatal("session wire format changed", string(updated))
	}
	ok, err := backend.CompareAndSwap(ctx, id, record, id, record, time.Hour)
	must(t, err)
	if ok {
		t.Fatal("stale session resurrected")
	}
}

func TestAcceptedQueueWireDLQAndReplay(t *testing.T) {
	ctx := context.Background()
	wire := `{"id":"compat-operation","name":"report","payload":{"n":7},"attempt":0,"maxAttempts":3,"createdAt":"2026-11-18T00:00:00Z"}`
	var message queue.Message
	must(t, json.Unmarshal([]byte(wire), &message))
	encoded, err := json.Marshal(message)
	must(t, err)
	if string(encoded) != wire {
		t.Fatalf("queue wire drift: %s", encoded)
	}
	broker := queue.NewMemoryBroker()
	must(t, broker.Enqueue(ctx, message, 0))
	delivery, err := broker.Reserve(ctx, 0)
	must(t, err)
	if delivery.Message().ID != message.ID {
		t.Fatal("delivery ID drift")
	}
	must(t, delivery.Ack(ctx))
	if _, err := broker.Reserve(ctx, 0); !errors.Is(err, queue.ErrEmpty) {
		t.Fatal(err)
	}
	directory := t.TempDir()
	store, err := queue.NewFileDeadLetters(directory)
	must(t, err)
	must(t, store.Save(ctx, queue.FailedJob{Message: message, Error: "synthetic failure", FailedAt: message.CreatedAt}))
	must(t, store.Close())
	reopened, err := queue.NewFileDeadLetters(directory)
	must(t, err)
	defer func() { _ = reopened.Close() }()
	failed, err := reopened.Get(ctx, message.ID)
	must(t, err)
	if failed.Error != "synthetic failure" || failed.Message.ID != message.ID {
		t.Fatal("durable record drift")
	}
	replayed, err := queue.Replay(ctx, reopened, message.ID, broker)
	must(t, err)
	if replayed == message.ID {
		t.Fatal("replay must have new identity")
	}
	delivery, err = broker.Reserve(ctx, 0)
	must(t, err)
	if delivery.Message().ID != replayed || delivery.Message().Attempt != 0 {
		t.Fatal("replay envelope drift")
	}
	must(t, delivery.Ack(ctx))
}

func TestConfigPasswordAndMigrationCompatibility(t *testing.T) {
	var settings struct {
		Timeout  time.Duration `env:"TIMEOUT" envDefault:"5s"`
		Required string        `env:"REQUIRED" envRequired:"true"`
	}
	if err := config.New(config.WithEnvironment(map[string]string{})).Load(&settings); err == nil {
		t.Fatal("required env ignored")
	}
	must(t, config.New(config.WithEnvironment(map[string]string{"REQUIRED": "set", "TIMEOUT": "2s"})).Load(&settings))
	if settings.Timeout != 2*time.Second || settings.Required != "set" {
		t.Fatal(settings)
	}
	hasher, err := auth.NewPasswordHasher(auth.PasswordConfig{MemoryKiB: 8192, Iterations: 1, Parallelism: 1})
	must(t, err)
	hash, err := hasher.Hash(context.Background(), "compatibility password")
	must(t, err)
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=8192,t=1,p=1$") {
		t.Fatal("PHC format drift")
	}
	_, err = hasher.Verify(context.Background(), "compatibility password", hash)
	must(t, err)
	if _, err := hasher.Verify(context.Background(), "wrong", hash); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatal(err)
	}
	m := migration.Migration{Version: "20261118000000", Name: "compat", UpSQL: "CREATE TABLE items(id INT);\n", DownSQL: "DROP TABLE items;\n"}
	windows := m
	windows.UpSQL = strings.ReplaceAll(windows.UpSQL, "\n", "\r\n")
	windows.DownSQL = strings.ReplaceAll(windows.DownSQL, "\n", "\r\n")
	if m.ID() != "20261118000000_compat" || m.Checksum() != windows.Checksum() {
		t.Fatal("migration identity/newline contract drift")
	}
	nontransactional := m
	nontransactional.NonTransactional = true
	if m.Checksum() == nontransactional.Checksum() {
		t.Fatal("DDL mode missing from migration identity")
	}
}

func TestCLIManifestAndScenarioContracts(t *testing.T) {
	var output bytes.Buffer
	cli := console.New(nil)
	cli.Out, cli.Err = &output, &output
	must(t, console.RegisterTools(cli, console.ToolOptions{}))
	must(t, cli.Run(context.Background(), []string{"help"}))
	for _, command := range []string{"migration:create", "migration:up", "migration:down", "migration:status", "migration:resolve", "generate:model", "generate:crud", "generate:migration"} {
		if !strings.Contains(output.String(), command) {
			t.Fatalf("CLI command lost: %s", command)
		}
	}
	manifest, err := asset.LoadViteManifest(fstest.MapFS{"manifest.json": {Data: []byte(`{"src/main.js":{"file":"assets/main-hash.js","css":["assets/main-hash.css"]}}`)}}, "manifest.json")
	must(t, err)
	_, err = manifest.Bundle("src/main.js", "/")
	must(t, err)
	var model struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
		Role string `json:"role"`
	}
	model.Name, model.Role = "before", "viewer"
	rules := validation.Set{Scenarios: map[string]validation.Scenario{"public": {Fields: []string{"name", "age"}}}}
	_, err = rules.Assign(&model, map[string]any{"name": "after", "age": "invalid"}, "public")
	if err == nil || model.Name != "before" {
		t.Fatal("failed mass assignment partially applied")
	}
	assignment, err := rules.Assign(&model, map[string]any{"name": "after", "role": "admin"}, "public")
	must(t, err)
	if model.Name != "after" || model.Role != "viewer" || !reflect.DeepEqual(assignment.Ignored, []string{"role"}) {
		t.Fatal("scenario allowlist drift")
	}
}
