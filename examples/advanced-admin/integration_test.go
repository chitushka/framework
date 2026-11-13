//go:build integration

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/web"
)

func required(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("%s required: use scripts/admin-e2e.sh", key)
	}
	return v
}
func status(err error) int {
	if err == nil {
		return 200
	}
	var e *web.HTTPError
	if errors.As(err, &e) {
		return e.Status
	}
	return 500
}
func fixtureSite(t *testing.T, s *store) *site {
	t.Helper()
	root := t.TempDir()
	check(t, os.MkdirAll(filepath.Join(root, "assets"), 0700))
	check(t, os.MkdirAll(filepath.Join(root, ".vite"), 0700))
	check(t, os.WriteFile(filepath.Join(root, ".vite", "manifest.json"), []byte(`{"src/admin.js":{"file":"assets/test.js","css":["assets/test.css"]}}`), 0600))
	check(t, os.WriteFile(filepath.Join(root, "assets", "test.js"), []byte("// fixture"), 0600))
	check(t, os.WriteFile(filepath.Join(root, "assets", "test.css"), []byte("body{}"), 0600))
	c, err := loadConfig(func(key string) string {
		if key == "ADMIN_DB_DSN" {
			return "test"
		}
		return ""
	})
	check(t, err)
	c.PublicDir = root
	site, err := newSite(s, c)
	check(t, err)
	t.Cleanup(func() { check(t, site.Close()) })
	return site
}
func request(h http.Handler, method, path string, values url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	var body string
	if values != nil {
		body = values.Encode()
	}
	r := httptest.NewRequest(method, "http://localhost:8081"+path, strings.NewReader(body))
	if values != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func cookie(t *testing.T, r *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	response := r.Result()
	defer func() { check(t, response.Body.Close()) }()
	for _, c := range response.Cookies() {
		if c.Name == "admin_session" {
			return c
		}
	}
	t.Fatal("session cookie missing")
	return nil
}
func signIn(t *testing.T, h http.Handler, name string) *http.Cookie {
	t.Helper()
	first := request(h, "GET", "/login", nil, nil)
	r := request(h, "POST", "/login", url.Values{"username": {name}, "password": {"integration-password"}, "_csrf": {first.Header().Get("X-CSRF-Token")}}, cookie(t, first))
	if r.Code != 303 {
		t.Fatalf("login %s: %d %s", name, r.Code, r.Body.String())
	}
	return cookie(t, r)
}
func userByName(t *testing.T, s *store, name string) user {
	t.Helper()
	u, err := scanUser(s.connection.QueryRowContext(t.Context(), s.bind("SELECT id,username,display_name,active,auth_version,version FROM admin_users WHERE username=?"), name))
	check(t, err)
	return u
}

func TestPersistentAdminBaseline(t *testing.T) {
	for _, backend := range []struct{ driver, dialect, env string }{{"pgx", db.PostgreSQL, "ADMIN_TEST_POSTGRES_DSN"}, {"mysql", db.MySQL, "ADMIN_TEST_MYSQL_DSN"}} {
		t.Run(backend.driver, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			config := db.Config{Driver: backend.driver, Dialect: backend.dialect, DSN: required(t, backend.env), MaxOpenConns: 8, MaxIdleConns: 4, PingTimeout: 5 * time.Second}
			connection, err := db.Open(ctx, config, db.NewDefaultDialectRegistry())
			check(t, err)
			defer func() { check(t, connection.Close()) }()
			s, err := newStore(connection)
			check(t, err)
			check(t, s.migrate(ctx))
			check(t, s.schemaReady(ctx))
			hash, err := s.hasher.Hash(ctx, "integration-password")
			check(t, err)
			check(t, s.seed(ctx, hash, hash))
			root := userByName(t, s, "admin")
			rootToken := principal{root}.ID()
			editor := userByName(t, s, "editor")
			editorToken := principal{editor}.ID()
			app := fixtureSite(t, s)
			adminCookie := signIn(t, app, "admin")
			editorCookie := signIn(t, app, "editor")
			adminCSRF := request(app, "GET", "/admin/users/new", nil, adminCookie).Header().Get("X-CSRF-Token")
			injectedUser := request(app, "POST", "/admin/users/new", url.Values{"_csrf": {adminCSRF}, "username": {"injected"}, "display_name": {"Injected"}, "password": {"integration-password"}, "active": {"1"}, "version": {"1"}, "role": {"administrator"}}, adminCookie)
			if injectedUser.Code != 400 {
				t.Fatal("user role mass assignment accepted")
			}
			if request(app, "GET", "/admin/catalog", nil, nil).Code != 303 {
				t.Fatal("guest access")
			}
			for _, path := range []string{"/admin/users", "/admin/roles", "/admin/assignments", "/admin/audit"} {
				if request(app, "GET", path, nil, editorCookie).Code != 403 {
					t.Fatalf("editor read escalation: %s", path)
				}
			}
			csrf := request(app, "GET", "/admin/catalog", nil, editorCookie).Header().Get("X-CSRF-Token")
			for _, path := range []string{"/admin/users/new", "/admin/roles/new", "/admin/assignments/add"} {
				r := request(app, "POST", path, url.Values{"_csrf": {csrf}, "role": {"administrator"}, "user_id": {editor.ID}, "version": {"1"}}, editorCookie)
				if r.Code != 403 {
					t.Fatalf("editor write escalation %s: %d", path, r.Code)
				}
			}
			injection := request(app, "POST", "/admin/catalog/new", url.Values{"_csrf": {csrf}, "code": {"injected"}, "name": {"Injected"}, "version": {"1"}, "is_admin": {"true"}}, editorCookie)
			if injection.Code != 400 {
				t.Fatal("mass assignment accepted")
			}
			if request(app, "POST", "/admin/catalog/new", url.Values{"code": {"csrf"}}, editorCookie).Code != 403 {
				t.Fatal("CSRF bypass")
			}
			id, err := s.saveCatalog(ctx, editorToken, catalogItem{Code: "persistent", Name: "Persisted"}, true)
			check(t, err)
			_, err = s.saveCatalog(ctx, editorToken, catalogItem{ID: id, Code: "persistent", Name: "Updated", Version: 1}, false)
			check(t, err)
			if _, err = s.saveCatalog(ctx, editorToken, catalogItem{ID: id, Code: "persistent", Name: "Stale", Version: 1}, false); status(err) != 409 {
				t.Fatal("stale catalog update")
			}
			if err = s.deleteCatalog(ctx, editorToken, id, 1); status(err) != 409 {
				t.Fatal("stale catalog delete")
			}
			// A second pool/handler observes persistent data and live authorization.
			otherConnection, err := db.Open(ctx, config, db.NewDefaultDialectRegistry())
			check(t, err)
			defer func() { check(t, otherConnection.Close()) }()
			other, err := newStore(otherConnection)
			check(t, err)
			check(t, other.seed(ctx, "", "")) // never reset seed state or overwrite users
			var persisted string
			check(t, other.connection.QueryRowContext(ctx, other.bind("SELECT name FROM admin_catalog WHERE id=?"), id).Scan(&persisted))
			if persisted != "Updated" {
				t.Fatal("restart persistence")
			}
			check(t, s.saveRole(ctx, rootToken, role{Name: "limited", Description: "Limited delegate", Permissions: []string{"rbac.manage"}}, true))
			delegateID, err := s.saveUser(ctx, rootToken, user{Username: "delegate", DisplayName: "Delegate", Active: 1}, "integration-password", true)
			check(t, err)
			check(t, s.assignment(ctx, rootToken, delegateID, "limited", 1, true))
			delegate := userByName(t, s, "delegate")
			delegateToken := principal{delegate}.ID()
			if err = s.saveRole(ctx, delegateToken, role{Name: "limited", Description: "Escalated", Permissions: []string{"rbac.manage", "users.manage"}, Version: 1}, false); status(err) != 403 {
				t.Fatal("delegated role escalation")
			}
			if err = s.assignment(ctx, delegateToken, delegateID, "administrator", delegate.Version, true); status(err) != 403 {
				t.Fatal("delegated assignment escalation")
			}
			if _, err = s.saveUser(ctx, delegateToken, user{ID: root.ID, Username: "admin", DisplayName: "Taken", Active: 1, Version: root.Version}, "attacker-password", false); status(err) != 403 {
				t.Fatal("password-reset escalation")
			}
			check(t, s.saveRole(ctx, rootToken, role{Name: "observer", Description: "Observer", Permissions: []string{"catalog.view"}}, true))
			observerID, err := s.saveUser(ctx, rootToken, user{Username: "observer", DisplayName: "Observer", Active: 1}, "integration-password", true)
			check(t, err)
			check(t, s.assignment(ctx, rootToken, observerID, "observer", 1, true))
			observerCookie := signIn(t, app, "observer")
			if request(app, "GET", "/admin/catalog", nil, observerCookie).Code != 200 {
				t.Fatal("assignment missing")
			}
			check(t, other.saveRole(ctx, rootToken, role{Name: "observer", Description: "No catalog", Version: 1}, false))
			// Idempotent metadata updates must work with MySQL changed-row semantics.
			check(t, other.saveRole(ctx, rootToken, role{Name: "observer", Description: "No catalog", Version: 2}, false))
			if request(app, "GET", "/admin/catalog", nil, observerCookie).Code != 403 {
				t.Fatal("revocation cached across pools")
			}
			observer := userByName(t, s, "observer")
			observer.Active = 0
			_, err = s.saveUser(ctx, rootToken, observer, "", false)
			check(t, err)
			if request(app, "GET", "/admin", nil, observerCookie).Code != 303 {
				t.Fatal("disabled user session retained")
			}
			observer = userByName(t, s, "observer")
			observer.Active = 1
			_, err = s.saveUser(ctx, rootToken, observer, "", false)
			check(t, err)
			if request(app, "GET", "/admin", nil, observerCookie).Code != 303 {
				t.Fatal("old ID revived after enable")
			}
			resetCookie := signIn(t, app, "observer")
			observer = userByName(t, s, "observer")
			_, err = s.saveUser(ctx, rootToken, observer, "changed-integration-password", false)
			check(t, err)
			if request(app, "GET", "/admin", nil, resetCookie).Code != 303 {
				t.Fatal("password reset did not revoke sessions")
			}
			if err = s.assignment(ctx, rootToken, root.ID, "administrator", root.Version, false); status(err) != 409 {
				t.Fatalf("last-admin revoke: %v", err)
			}
			if err = s.deleteUser(ctx, rootToken, root.ID, root.Version); status(err) != 409 {
				t.Fatal("last-admin delete")
			}
			if err = s.deleteRole(ctx, rootToken, "administrator", 1); status(err) != 403 {
				t.Fatal("protected role delete")
			}
			// Audit failure MUST undo mutation and assignment version changes.
			installAuditFailure(t, s, backend.driver)
			_, err = s.saveCatalog(ctx, rootToken, catalogItem{Code: "audit-failed", Name: "Must roll back"}, true)
			if err == nil {
				t.Fatal("audit failure ignored")
			}
			var count int
			check(t, s.connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_catalog WHERE code='audit-failed'").Scan(&count))
			if count != 0 {
				t.Fatal("unaudited mutation survived")
			}
			observer = userByName(t, s, "observer")
			if err = s.assignment(ctx, rootToken, observerID, "editor", observer.Version, true); err == nil {
				t.Fatal("assignment audit failure ignored")
			}
			if userByName(t, s, "observer").Version != observer.Version {
				t.Fatal("partial assignment/version update")
			}
			removeAuditFailure(t, s, backend.driver)
			// Two independent processes/pools cannot remove the last administrator.
			backupID, err := s.saveUser(ctx, rootToken, user{Username: "backup", DisplayName: "Backup", Active: 1}, "integration-password", true)
			check(t, err)
			check(t, s.assignment(ctx, rootToken, backupID, "administrator", 1, true))
			var group sync.WaitGroup
			errs := make(chan error, 2)
			for i, target := range []user{userByName(t, s, "admin"), userByName(t, s, "backup")} {
				repository := s
				if i == 1 {
					repository = other
				}
				group.Add(1)
				go func(u user) {
					defer group.Done()
					errs <- repository.assignment(ctx, rootToken, u.ID, "administrator", u.Version, false)
				}(target)
			}
			group.Wait()
			close(errs)
			success := 0
			for e := range errs {
				if e == nil {
					success++
				} else if status(e) != 403 && status(e) != 409 {
					t.Fatal(e)
				}
			}
			if success != 1 {
				t.Fatalf("last-admin race successes=%d", success)
			}
			check(t, s.connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_users u JOIN admin_auth_assignment a ON a.user_id=u.id WHERE a.role_name='administrator' AND u.active=1").Scan(&count))
			if count != 1 {
				t.Fatal("lost all administrators")
			}
			// No secrets in persisted audit payloads.
			rows, err := s.connection.QueryContext(ctx, "SELECT details FROM admin_audit")
			check(t, err)
			for rows.Next() {
				var details string
				check(t, rows.Scan(&details))
				if strings.Contains(details, "integration-password") || strings.Contains(details, "$argon2") {
					t.Fatal("audit secret disclosure")
				}
			}
			check(t, rows.Err())
			check(t, rows.Close())
			runner, err := s.runner()
			check(t, err)
			_, err = runner.Down(ctx, 99)
			check(t, err)
			check(t, s.migrate(ctx))
			check(t, s.seed(ctx, hash, hash))
		})
	}
}
func installAuditFailure(t *testing.T, s *store, driver string) {
	t.Helper()
	ctx := t.Context()
	if driver == "pgx" {
		_, err := s.connection.ExecContext(ctx, `CREATE FUNCTION reject_admin_audit() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RAISE EXCEPTION ''audit failure''; END;'`)
		check(t, err)
		_, err = s.connection.ExecContext(ctx, "CREATE TRIGGER reject_admin_audit BEFORE INSERT ON admin_audit FOR EACH ROW EXECUTE FUNCTION reject_admin_audit()")
		check(t, err)
	} else {
		_, err := s.connection.ExecContext(ctx, "CREATE TRIGGER reject_admin_audit BEFORE INSERT ON admin_audit FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='audit failure'")
		check(t, err)
	}
}
func removeAuditFailure(t *testing.T, s *store, driver string) {
	t.Helper()
	query := "DROP TRIGGER reject_admin_audit"
	if driver == "pgx" {
		query += " ON admin_audit"
	}
	_, err := s.connection.ExecContext(t.Context(), query)
	check(t, err)
	if driver == "pgx" {
		_, err = s.connection.ExecContext(t.Context(), "DROP FUNCTION reject_admin_audit()")
		check(t, err)
	}
}
