package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/rbac"
	"github.com/chitushka/framework/web"
)

var permissions = []string{"catalog.view", "catalog.manage", "users.manage", "rbac.manage", "audit.view"}

type user struct {
	ID, Username, DisplayName string
	Active                    int
	Epoch, Version            int64
}
type principal struct{ user }

func (p principal) ID() string { return p.user.ID + ":" + strconv.FormatInt(p.Epoch, 10) }

type catalogItem struct {
	ID, Code, Name string
	Version        int64
}
type auditRecord struct{ ID, Actor, Action, Target, Details, When string }
type role struct {
	Name, Description string
	Permissions       []string
	Protected         bool
	Version           int64
}

type store struct {
	connection *db.Connection
	hasher     *auth.PasswordHasher
}
type transaction struct {
	s     *store
	tx    *sql.Tx
	rbac  *rbac.Manager
	actor user
}
type sqlReader interface{ Scan(...any) error }

func newStore(connection *db.Connection) (*store, error) {
	h, err := auth.NewPasswordHasher(auth.PasswordConfig{})
	if err != nil {
		return nil, err
	}
	return &store{connection: connection, hasher: h}, nil
}
func newID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

// SQL text is constant application code. Values are always driver parameters.
func (s *store) bind(query string) string {
	parts := strings.Split(query, "?")
	var out strings.Builder
	for i, part := range parts {
		if i > 0 {
			out.WriteString(s.connection.Dialect().Placeholder(i))
		}
		out.WriteString(part)
	}
	return out.String()
}
func scanUser(row sqlReader) (user, error) {
	var u user
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Active, &u.Epoch, &u.Version)
	return u, err
}
func decodePrincipal(value string) (string, int64, bool) {
	id, version, ok := strings.Cut(value, ":")
	epoch, err := strconv.ParseInt(version, 10, 64)
	_, hexErr := hex.DecodeString(id)
	return id, epoch, ok && len(id) == 32 && hexErr == nil && err == nil && epoch > 0
}
func (s *store) FindIdentity(ctx context.Context, value string) (auth.Identity, error) {
	id, epoch, ok := decodePrincipal(value)
	if !ok {
		return nil, nil
	}
	u, err := scanUser(s.connection.QueryRowContext(ctx, s.bind("SELECT id,username,display_name,active,auth_version,version FROM admin_users WHERE id=?"), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if u.Active != 1 || u.Epoch != epoch {
		return nil, nil
	}
	return principal{u}, nil
}
func (s *store) FindCredential(ctx context.Context, login string) (*auth.Credential, error) {
	var u user
	var hash string
	err := s.connection.QueryRowContext(ctx, s.bind("SELECT id,username,display_name,active,auth_version,version,password_hash FROM admin_users WHERE username=?"), login).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Active, &u.Epoch, &u.Version, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &auth.Credential{Identity: principal{u}, PasswordHash: hash, Disabled: u.Active != 1}, nil
}
func (s *store) ReplacePasswordHash(ctx context.Context, identityID, old, next string) (bool, error) {
	id, epoch, ok := decodePrincipal(identityID)
	if !ok {
		return false, nil
	}
	replaced := false
	err := s.transact(ctx, "", "", func(t *transaction) error {
		result, err := t.tx.ExecContext(ctx, s.bind("UPDATE admin_users SET password_hash=? WHERE id=? AND auth_version=? AND password_hash=? AND active=1"), next, id, epoch, old)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		replaced = count == 1
		if replaced {
			t.actor, err = t.user(ctx, id)
			if err != nil {
				return err
			}
			return t.audit(ctx, "user.rehash", id, map[string]any{"cost_upgrade": true})
		}
		return nil
	})
	return replaced, err
}

// The guard serializes authorization, grant/revoke, last-admin checks and writes
// across application processes. Audits share the same transaction as mutations.
func (s *store) transact(ctx context.Context, token, permission string, fn func(*transaction) error) (err error) {
	tx, err := s.connection.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var guard int
	if err = tx.QueryRowContext(ctx, "SELECT id FROM admin_guard WHERE id=1 FOR UPDATE").Scan(&guard); err != nil {
		return err
	}
	backend, err := rbac.NewDatabaseStore(rbac.DatabaseConfig{DB: tx, Dialect: s.connection.Dialect(), TablePrefix: "admin_auth"})
	if err != nil {
		return err
	}
	manager, err := rbac.New(backend)
	if err != nil {
		return err
	}
	t := &transaction{s: s, tx: tx, rbac: manager}
	if permission != "" {
		id, epoch, ok := decodePrincipal(token)
		if !ok {
			return web.NewHTTPError(401, "Войдите в систему", nil)
		}
		t.actor, err = t.user(ctx, id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (t.actor.Active != 1 || t.actor.Epoch != epoch)) {
			return web.NewHTTPError(401, "Войдите в систему", nil)
		}
		if err != nil {
			return err
		}
		if permission != "authenticated" {
			allowed, err := manager.CheckAccess(ctx, id, permission, nil)
			if err != nil {
				return err
			}
			if !allowed {
				return web.NewHTTPError(403, "Недостаточно прав", nil)
			}
		}
	}
	if err = fn(t); err != nil {
		return err
	}
	return tx.Commit()
}
func (t *transaction) user(ctx context.Context, id string) (user, error) {
	return scanUser(t.tx.QueryRowContext(ctx, t.s.bind("SELECT id,username,display_name,active,auth_version,version FROM admin_users WHERE id=?"), id))
}
func (t *transaction) audit(ctx context.Context, action, target string, details any) error {
	payload, err := json.Marshal(details)
	if err != nil {
		return err
	}
	id, err := newID()
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, t.s.bind("INSERT INTO admin_audit(id,created_at,actor_id,actor_name,action,target,details) VALUES(?,?,?,?,?,?,?)"), id, time.Now().UTC().UnixMicro(), t.actor.ID, t.actor.Username, action, target, string(payload))
	return err
}
func (t *transaction) capabilities(ctx context.Context, id string) (map[string]bool, error) {
	result := make(map[string]bool)
	for _, p := range permissions {
		allowed, err := t.rbac.CheckAccess(ctx, id, p, nil)
		if err != nil {
			return nil, err
		}
		result[p] = allowed
	}
	return result, nil
}
func (t *transaction) grantCeiling(ctx context.Context, targetPermissions []string) error {
	current, err := t.capabilities(ctx, t.actor.ID)
	if err != nil {
		return err
	}
	for _, p := range targetPermissions {
		if !current[p] {
			return web.NewHTTPError(403, "Нельзя передать права, которых у вас нет", nil)
		}
	}
	return nil
}
func (t *transaction) controlUser(ctx context.Context, id string) error {
	values, err := t.capabilities(ctx, id)
	if err != nil {
		return err
	}
	var granted []string
	for p, ok := range values {
		if ok {
			granted = append(granted, p)
		}
	}
	return t.grantCeiling(ctx, granted)
}
func (t *transaction) lastAdministrator(ctx context.Context) error {
	var count int
	err := t.tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_users u JOIN admin_auth_assignment a ON a.user_id=u.id WHERE u.active=1 AND a.role_name='administrator'").Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		return web.NewHTTPError(409, "Нужен хотя бы один активный administrator", nil)
	}
	return nil
}
func affected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return web.NewHTTPError(409, "Запись изменилась. Обновите страницу", nil)
	}
	return nil
}
func domainError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return web.NewHTTPError(404, "Запись не найдена", err)
	}
	return err
}
func visibleUser(u user) map[string]any {
	return map[string]any{"id": u.ID, "username": u.Username, "name": u.DisplayName, "active": u.Active, "version": u.Version}
}
