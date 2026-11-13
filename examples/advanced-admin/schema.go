package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/db/migration"
	"github.com/chitushka/framework/rbac"
)

type schemaSource struct{ rbacSQL, indexDrop string }

func (s schemaSource) Migrations(context.Context) ([]migration.Migration, error) {
	// One statement per migration also works with MySQL multiStatements=false.
	steps := [][3]string{
		{"guard", "CREATE TABLE admin_guard (id INTEGER PRIMARY KEY, seeded INTEGER NOT NULL)", "DROP TABLE admin_guard"},
		{"guard_row", "INSERT INTO admin_guard(id,seeded) VALUES(1,0)", "DELETE FROM admin_guard WHERE id=1"},
		{"users", "CREATE TABLE admin_users (id VARCHAR(32) PRIMARY KEY, username VARCHAR(64) NOT NULL UNIQUE, display_name VARCHAR(120) NOT NULL, password_hash VARCHAR(256) NOT NULL, active INTEGER NOT NULL, auth_version BIGINT NOT NULL, version BIGINT NOT NULL)", "DROP TABLE admin_users"},
		{"catalog", "CREATE TABLE admin_catalog (id VARCHAR(32) PRIMARY KEY, code VARCHAR(64) NOT NULL UNIQUE, name VARCHAR(200) NOT NULL, version BIGINT NOT NULL)", "DROP TABLE admin_catalog"},
		{"audit", "CREATE TABLE admin_audit (id VARCHAR(32) PRIMARY KEY, created_at BIGINT NOT NULL, actor_id VARCHAR(32) NOT NULL, actor_name VARCHAR(64) NOT NULL, action VARCHAR(64) NOT NULL, target VARCHAR(160) NOT NULL, details TEXT NOT NULL)", "DROP TABLE admin_audit"},
		{"audit_index", "CREATE INDEX admin_audit_created ON admin_audit(created_at,id)", s.indexDrop},
	}
	tables := []string{"admin_auth_item", "admin_auth_item_child", "admin_auth_assignment"}
	for i, statement := range strings.Split(strings.TrimSuffix(s.rbacSQL, ";"), ";") {
		if i >= len(tables) {
			return nil, errors.New("admin: unexpected RBAC schema")
		}
		steps = append(steps, [3]string{tables[i], strings.TrimSpace(statement), "DROP TABLE " + tables[i]})
	}
	steps = append(steps, [3]string{"role_versions", "CREATE TABLE admin_role_version (name VARCHAR(255) PRIMARY KEY, version BIGINT NOT NULL, FOREIGN KEY(name) REFERENCES admin_auth_item(name) ON DELETE CASCADE)", "DROP TABLE admin_role_version"})
	var result []migration.Migration
	for i, step := range steps {
		result = append(result, migration.Migration{Version: fmt.Sprintf("202611130000%02d", i), Name: "advanced_" + step[0], UpSQL: step[1], DownSQL: step[2]})
	}
	return result, nil
}
func (s *store) runner() (*migration.Runner, error) {
	backend, err := rbac.NewDatabaseStore(rbac.DatabaseConfig{DB: s.connection, Dialect: s.connection.Dialect(), TablePrefix: "admin_auth"})
	if err != nil {
		return nil, err
	}
	drop := "DROP INDEX admin_audit_created"
	if s.connection.Dialect().Name() == db.MySQL {
		drop += " ON admin_audit"
	}
	return migration.NewRunner(s.connection, schemaSource{backend.SchemaSQL(), drop}, migration.Options{Table: "admin_migration"})
}
func (s *store) migrate(ctx context.Context) error {
	r, err := s.runner()
	if err != nil {
		return err
	}
	_, err = r.Up(ctx, 0)
	return err
}
func (s *store) schemaReady(ctx context.Context) error {
	r, err := s.runner()
	if err != nil {
		return err
	}
	statuses, err := r.Status(ctx)
	if err != nil {
		return err
	}
	if len(statuses) != 10 {
		return errors.New("admin: run -migrate before starting the application")
	}
	for _, status := range statuses {
		if status.State != migration.StateApplied {
			return errors.New("admin: unapplied or modified migration; run -migrate")
		}
	}
	return nil
}
func (s *store) seed(ctx context.Context, adminHash, editorHash string) error {
	return s.transact(ctx, "", "", func(t *transaction) error {
		var seeded int
		if err := t.tx.QueryRowContext(ctx, "SELECT seeded FROM admin_guard WHERE id=1").Scan(&seeded); err != nil {
			return err
		}
		if seeded == 1 {
			return nil
		}
		for _, hash := range []string{adminHash, editorHash} {
			if hash == "" {
				return errors.New("admin: both seed password hash files are required")
			}
			_, err := s.hasher.Verify(ctx, "configuration-validation", hash)
			if err != nil && !errors.Is(err, auth.ErrInvalidCredentials) {
				return errors.New("admin: invalid seed password hash")
			}
		}
		for _, item := range []rbac.Item{{Name: "administrator", Kind: rbac.Role, Description: "Администратор"}, {Name: "editor", Kind: rbac.Role, Description: "Редактор каталога"}} {
			if err := t.rbac.AddItem(ctx, item); err != nil {
				return err
			}
			if _, err := t.tx.ExecContext(ctx, s.bind("INSERT INTO admin_role_version(name,version) VALUES(?,1)"), item.Name); err != nil {
				return err
			}
		}
		for _, p := range permissions {
			if err := t.rbac.AddPermission(ctx, p, p); err != nil {
				return err
			}
			if err := t.rbac.AddChild(ctx, "administrator", p); err != nil {
				return err
			}
		}
		for _, p := range []string{"catalog.view", "catalog.manage"} {
			if err := t.rbac.AddChild(ctx, "editor", p); err != nil {
				return err
			}
		}
		for i, name := range []string{"admin", "editor"} {
			id, err := newID()
			if err != nil {
				return err
			}
			hash := adminHash
			roleName := "administrator"
			if i == 1 {
				hash = editorHash
				roleName = "editor"
			}
			if _, err = t.tx.ExecContext(ctx, s.bind("INSERT INTO admin_users(id,username,display_name,password_hash,active,auth_version,version) VALUES(?,?,?,?,1,1,1)"), id, name, name, hash); err != nil {
				return err
			}
			if err = t.rbac.Assign(ctx, id, roleName); err != nil {
				return err
			}
		}
		id, err := newID()
		if err != nil {
			return err
		}
		if _, err = t.tx.ExecContext(ctx, s.bind("INSERT INTO admin_catalog(id,code,name,version) VALUES(?,?,?,1)"), id, "active", "Активный"); err != nil {
			return err
		}
		if _, err = t.tx.ExecContext(ctx, "UPDATE admin_guard SET seeded=1 WHERE id=1"); err != nil {
			return err
		}
		t.actor.Username = "bootstrap"
		return t.audit(ctx, "bootstrap.seed", "application", map[string]any{"accounts": []string{"admin", "editor"}})
	})
}
func (s *store) checkSeeded(ctx context.Context) error {
	var seeded int
	if err := s.connection.QueryRowContext(ctx, "SELECT seeded FROM admin_guard WHERE id=1").Scan(&seeded); err != nil {
		return err
	}
	if seeded != 1 {
		return fmt.Errorf("admin: run -seed before starting")
	}
	return nil
}
func protectedRole(name string) bool { return name == "administrator" || name == "editor" }
func normalizedPermissions(values []string) ([]string, error) {
	seen := map[string]bool{}
	var result []string
	for _, p := range values {
		known := false
		for _, allowed := range permissions {
			if p == allowed {
				known = true
				break
			}
		}
		if !known || seen[p] {
			return nil, errors.New("invalid permissions")
		}
		seen[p] = true
		result = append(result, p)
	}
	return result, nil
}
func canonicalLogin(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
