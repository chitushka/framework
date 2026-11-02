package rbac_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/rbac"
	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

func TestManagerChecksInheritedPermissionAndRule(t *testing.T) {
	manager, _ := rbac.New(rbac.NewMemoryStore())
	err := manager.RegisterRule("ownsPost", rbac.RuleFunc(func(_ context.Context, userID string, params map[string]any) (bool, error) {
		owner, _ := params["owner"].(string)
		return owner == userID, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, item := range []rbac.Item{{Name: "admin", Kind: rbac.Role}, {Name: "editor", Kind: rbac.Role}, {Name: "post.update", Kind: rbac.Permission, Rule: "ownsPost"}} {
		if err := manager.AddItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.AddChild(ctx, "admin", "editor"); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddChild(ctx, "editor", "post.update"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Assign(ctx, "42", "admin"); err != nil {
		t.Fatal(err)
	}
	allowed, err := manager.CheckAccess(ctx, "42", "post.update", map[string]any{"owner": "42"})
	if err != nil || !allowed {
		t.Fatalf("owned access = %v, %v", allowed, err)
	}
	allowed, err = manager.CheckAccess(ctx, "42", "post.update", map[string]any{"owner": "7"})
	if err != nil || allowed {
		t.Fatalf("foreign access = %v, %v", allowed, err)
	}
}

func TestManagerProtectsHierarchyInvariants(t *testing.T) {
	manager, _ := rbac.New(rbac.NewMemoryStore())
	ctx := context.Background()
	_ = manager.AddRole(ctx, "parent", "")
	_ = manager.AddRole(ctx, "child", "")
	_ = manager.AddPermission(ctx, "read", "")
	if err := manager.AddChild(ctx, "parent", "child"); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddChild(ctx, "child", "parent"); !errors.Is(err, rbac.ErrCycle) {
		t.Fatalf("cycle error = %v", err)
	}
	if err := manager.AddChild(ctx, "read", "parent"); err == nil {
		t.Fatal("permission accepted role child")
	}
	if err := manager.Assign(ctx, "1", "read"); err == nil {
		t.Fatal("permission assigned as role")
	}
	if err := manager.AddRole(ctx, "parent", ""); !errors.Is(err, rbac.ErrAlreadyExists) {
		t.Fatalf("duplicate error = %v", err)
	}
	if err := manager.UpdateItem(ctx, rbac.Item{Name: "parent", Kind: rbac.Role, Description: "Parent role"}); err != nil {
		t.Fatal(err)
	}
	roles, err := manager.Roles(ctx)
	if err != nil || len(roles) != 2 || roles[1].Description != "Parent role" {
		t.Fatalf("roles = %#v, %v", roles, err)
	}
	permissions, err := manager.Permissions(ctx)
	if err != nil || len(permissions) != 1 {
		t.Fatalf("permissions = %#v, %v", permissions, err)
	}
	children, err := manager.Children(ctx, "parent")
	if err != nil || len(children) != 1 {
		t.Fatalf("children = %#v, %v", children, err)
	}
	if err := manager.RemoveChild(ctx, "parent", "child"); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddChild(ctx, "parent", "child"); err != nil {
		t.Fatal(err)
	}
	item, found, err := manager.Item(ctx, "parent")
	if err != nil || !found || item.Description != "Parent role" {
		t.Fatalf("item = %#v, %v, %v", item, found, err)
	}
	if err := manager.RemoveItem(ctx, "child"); err != nil {
		t.Fatal(err)
	}
	allowed, err := manager.CheckAccess(ctx, "1", "read", nil)
	if err != nil || allowed {
		t.Fatalf("access = %v, %v", allowed, err)
	}
}

func TestAssignmentsCanBeRevoked(t *testing.T) {
	store := rbac.NewMemoryStore()
	manager, _ := rbac.New(store)
	ctx := context.Background()
	_ = manager.AddRole(ctx, "reader", "")
	_ = manager.AddPermission(ctx, "post.read", "")
	_ = manager.AddChild(ctx, "reader", "post.read")
	_ = manager.Assign(ctx, "42", "reader")
	assignments, err := store.Assignments(ctx, "42")
	if err != nil || len(assignments) != 1 {
		t.Fatalf("assignments = %#v, %v", assignments, err)
	}
	assignments, err = manager.Assignments(ctx, "42")
	if err != nil || len(assignments) != 1 {
		t.Fatalf("manager assignments = %#v, %v", assignments, err)
	}
	if err := manager.Revoke(ctx, "42", "reader"); err != nil {
		t.Fatal(err)
	}
	allowed, err := manager.CheckAccess(ctx, "42", "post.read", nil)
	if err != nil || allowed {
		t.Fatalf("revoked access = %v, %v", allowed, err)
	}
}

type identity string

func (value identity) ID() string { return string(value) }

type identityLoader struct{}

func (identityLoader) FindIdentity(_ context.Context, id string) (auth.Identity, error) {
	return identity(id), nil
}

func TestRequireMiddlewareAndTemplateFunc(t *testing.T) {
	manager, _ := rbac.New(rbac.NewMemoryStore())
	ctx := context.Background()
	_ = manager.AddRole(ctx, "reader", "")
	_ = manager.AddPermission(ctx, "post.read", "")
	_ = manager.AddChild(ctx, "reader", "post.read")
	_ = manager.Assign(ctx, "42", "reader")
	sessions, _ := session.New(session.NewMemoryBackend(), session.Config{Name: "sid"})
	router := web.NewRouter()
	_ = router.Use(sessions.Middleware, auth.Middleware(identityLoader{}))
	_ = router.POST("/login", func(ctx *web.Context) error { return auth.Must(ctx.Request.Context()).Login(identity("42")) })
	_ = router.GET("/posts", func(ctx *web.Context) error {
		if !manager.TemplateFunc(ctx, nil)("post.read") {
			return errors.New("template access denied")
		}
		return ctx.NoContent(204)
	}, web.WithMiddleware(rbac.Require(manager, "post.read", nil)))
	_ = router.GET("/admin", func(ctx *web.Context) error { return ctx.NoContent(204) }, web.WithMiddleware(rbac.Require(manager, "admin.access", nil)))
	guest := httptest.NewRecorder()
	router.ServeHTTP(guest, httptest.NewRequest(http.MethodGet, "/posts", nil))
	if guest.Code != 401 {
		t.Fatalf("guest status = %d", guest.Code)
	}
	login := httptest.NewRecorder()
	router.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/login", nil))
	request := httptest.NewRequest(http.MethodGet, "/posts", nil)
	request.AddCookie(login.Result().Cookies()[0])
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 204 {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	deniedRequest := httptest.NewRequest(http.MethodGet, "/admin", nil)
	deniedRequest.AddCookie(login.Result().Cookies()[0])
	denied := httptest.NewRecorder()
	router.ServeHTTP(denied, deniedRequest)
	if denied.Code != 403 {
		t.Fatalf("denied status = %d", denied.Code)
	}
}

func TestManagerValidationAndRuleFailures(t *testing.T) {
	if _, err := rbac.New(nil); err == nil {
		t.Fatal("nil store accepted")
	}
	manager, _ := rbac.New(rbac.NewMemoryStore())
	ctx := context.Background()
	if err := manager.RegisterRule("nil", nil); err == nil {
		t.Fatal("nil rule accepted")
	}
	if err := manager.RegisterRule("", rbac.RuleFunc(func(context.Context, string, map[string]any) (bool, error) { return true, nil })); err == nil {
		t.Fatal("empty rule accepted")
	}
	ruleError := errors.New("rule failed")
	if err := manager.RegisterRule("broken", rbac.RuleFunc(func(context.Context, string, map[string]any) (bool, error) { return false, ruleError })); err != nil {
		t.Fatal(err)
	}
	if err := manager.RegisterRule("broken", rbac.RuleFunc(func(context.Context, string, map[string]any) (bool, error) { return true, nil })); !errors.Is(err, rbac.ErrAlreadyExists) {
		t.Fatalf("duplicate rule = %v", err)
	}
	if err := manager.AddItem(ctx, rbac.Item{Name: "bad", Kind: "unknown"}); err == nil {
		t.Fatal("invalid kind accepted")
	}
	if err := manager.AddItem(ctx, rbac.Item{Name: "unregistered", Kind: rbac.Permission, Rule: "missing"}); err == nil {
		t.Fatal("unregistered rule accepted")
	}
	_ = manager.AddRole(ctx, "role", "")
	_ = manager.AddItem(ctx, rbac.Item{Name: "guarded", Kind: rbac.Permission, Rule: "broken"})
	_ = manager.AddChild(ctx, "role", "guarded")
	_ = manager.Assign(ctx, "user", "role")
	if _, err := manager.CheckAccess(ctx, "user", "guarded", nil); !errors.Is(err, ruleError) {
		t.Fatalf("rule error = %v", err)
	}
	if err := manager.UpdateItem(ctx, rbac.Item{Name: "role", Kind: rbac.Permission}); err == nil {
		t.Fatal("kind change accepted")
	}
	if err := manager.UpdateItem(ctx, rbac.Item{Name: "missing", Kind: rbac.Role}); !errors.Is(err, rbac.ErrNotFound) {
		t.Fatalf("update missing = %v", err)
	}
	if err := manager.AddChild(ctx, "missing", "role"); !errors.Is(err, rbac.ErrNotFound) {
		t.Fatalf("missing parent = %v", err)
	}
	if err := manager.AddChild(ctx, "role", "missing"); !errors.Is(err, rbac.ErrNotFound) {
		t.Fatalf("missing child = %v", err)
	}
	if err := manager.RemoveChild(ctx, "role", "missing"); !errors.Is(err, rbac.ErrNotFound) {
		t.Fatalf("remove missing child = %v", err)
	}
	if allowed, err := manager.CheckAccess(ctx, "user", "role", nil); err != nil || allowed {
		t.Fatalf("role access = %v, %v", allowed, err)
	}
	if allowed, err := manager.CheckAccess(ctx, "user", "missing", nil); err != nil || allowed {
		t.Fatalf("missing access = %v, %v", allowed, err)
	}
	if err := manager.RemoveItem(ctx, "missing"); !errors.Is(err, rbac.ErrNotFound) {
		t.Fatalf("remove missing = %v", err)
	}
	if err := manager.Revoke(ctx, "user", "missing"); !errors.Is(err, rbac.ErrNotFound) {
		t.Fatalf("revoke missing = %v", err)
	}
	if _, err := manager.Roles(nil); err == nil {
		t.Fatal("nil context accepted")
	}
}
