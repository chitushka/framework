package main

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/chitushka/framework/asset"
	"github.com/chitushka/framework/web"
)

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func TestConfigValidationAndNoImplicitDatabase(t *testing.T) {
	if _, err := loadConfig(func(string) string { return "" }); err == nil {
		t.Fatal("implicit database accepted")
	}
	for _, input := range []map[string]string{{"ADMIN_DB_DSN": "test", "APP_ENV": "staging"}, {"ADMIN_DB_DSN": "test", "ADMIN_DB_DRIVER": "sqlite"}, {"ADMIN_DB_DSN": "test", "APP_ENV": "production"}, {"ADMIN_DB_DSN": "test", "ADMIN_ADDRESS": "bad"}} {
		if _, err := loadConfig(func(key string) string { return input[key] }); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	c, err := loadConfig(func(key string) string {
		if key == "ADMIN_DB_DSN" {
			return "private"
		}
		return ""
	})
	check(t, err)
	if c.Database.MaxOpenConns != 8 || c.Proxy.AllowedHosts[0] != "localhost:8081" {
		t.Fatal("unbounded or unusable defaults")
	}
	if strings.Contains(c.Database.Driver, "private") {
		t.Fatal("invalid driver")
	}
}
func TestInputBoundariesAndEpochTokens(t *testing.T) {
	if validateUser("owner", "Owner", "long-enough-password", true, 1) != nil {
		t.Fatal("valid user")
	}
	for _, u := range []user{{Username: "root<script>", DisplayName: "X", Active: 1}, {Username: "owner", DisplayName: "", Active: 1}, {Username: "owner", DisplayName: "X", Active: 2}} {
		if validateUser(u.Username, u.DisplayName, "long-enough-password", true, u.Active) == nil {
			t.Fatal("unsafe input")
		}
	}
	if validateCatalog("ok", "Name") != nil || validateCatalog("bad<script>", "Name") == nil {
		t.Fatal("catalog validation")
	}
	if _, err := normalizedPermissions([]string{"catalog.view", "catalog.view"}); err == nil {
		t.Fatal("duplicate permissions")
	}
	if _, err := normalizedPermissions([]string{"is_admin"}); err == nil {
		t.Fatal("arbitrary permission")
	}
	for _, value := range []string{"1:1", strings.Repeat("z", 32) + ":1", strings.Repeat("a", 32) + ":-1"} {
		if _, _, ok := decodePrincipal(value); ok {
			t.Fatal("invalid identity token")
		}
	}
	if _, _, ok := decodePrincipal(strings.Repeat("a", 32) + ":1"); !ok {
		t.Fatal("valid identity token")
	}
}
func TestAllEmbeddedViewsRenderWithEscaping(t *testing.T) {
	views, err := fs.Sub(templates, "templates")
	check(t, err)
	registry := asset.NewRegistry()
	check(t, registry.Register("admin", asset.Bundle{}))
	functions := registry.TemplateFuncs("admin")
	functions["has"] = func([]string, string) bool { return true }
	renderer, err := web.NewRenderer(views, web.WithDefaultLayout("layout"), web.WithTemplateFuncs(functions))
	check(t, err)
	p := pageData{Title: "Test", Username: "<script>alert(1)</script>", Capabilities: map[string]bool{"rbac.manage": true, "catalog.manage": true}, CSRF: "token", PermissionNames: permissions, Roles: []role{{Name: "role", Permissions: permissions}}, Assignments: []assignmentView{{User: user{ID: "target", Version: 2}, Roles: []string{"role"}}}}
	for _, name := range []string{"login", "home", "error", "catalog", "catalog_form", "users", "user_form", "roles", "role_form", "assignments", "audit"} {
		body, err := renderer.Render(name, p)
		check(t, err)
		if strings.Contains(string(body), "<script>alert(1)</script>") {
			t.Fatal("HTML injection")
		}
	}
	body, err := renderer.Render("assignments", p)
	check(t, err)
	if !strings.Contains(string(body), `name="user_id" value="target"`) {
		t.Fatal("assignment target lost in nested template")
	}
}
