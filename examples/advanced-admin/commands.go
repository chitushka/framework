package main

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/chitushka/framework/rbac"
	"github.com/chitushka/framework/web"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{1,63}$`)

func invalid(message string) error { return web.NewHTTPError(422, message, nil) }
func text(value string, max int) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= max && !strings.ContainsAny(value, "\x00\r\n")
}
func validateCatalog(code, name string) error {
	if !namePattern.MatchString(code) || !text(name, 200) {
		return invalid("Код: 2–64 латинских символа; название: 1–200 символов")
	}
	return nil
}
func validateUser(login, name, password string, creating bool, active int) error {
	if canonicalLogin(login) != login || len(login) < 3 || !namePattern.MatchString(login) || !text(name, 120) || (active != 0 && active != 1) {
		return invalid("Проверьте имя пользователя, имя и статус")
	}
	if (creating || password != "") && (len(password) < 12 || len(password) > 1024 || !utf8.ValidString(password)) {
		return invalid("Пароль должен содержать 12–1024 байта")
	}
	return nil
}

func (s *store) saveCatalog(ctx context.Context, actor string, item catalogItem, creating bool) (string, error) {
	if err := validateCatalog(item.Code, item.Name); err != nil {
		return "", err
	}
	if creating {
		id, err := newID()
		if err != nil {
			return "", err
		}
		item.ID = id
	}
	err := s.transact(ctx, actor, "catalog.manage", func(t *transaction) error {
		var duplicate int
		if err := t.tx.QueryRowContext(ctx, s.bind("SELECT COUNT(*) FROM admin_catalog WHERE code=? AND id<>?"), item.Code, item.ID).Scan(&duplicate); err != nil {
			return err
		}
		if duplicate > 0 {
			return invalid("Этот код уже используется")
		}
		if creating {
			if _, err := t.tx.ExecContext(ctx, s.bind("INSERT INTO admin_catalog(id,code,name,version) VALUES(?,?,?,1)"), item.ID, item.Code, item.Name); err != nil {
				return err
			}
			return t.audit(ctx, "catalog.create", item.ID, map[string]any{"code": item.Code, "name": item.Name})
		}
		before, err := t.catalog(ctx, item.ID)
		if err != nil {
			return domainError(err)
		}
		if err := affected(t.tx.ExecContext(ctx, s.bind("UPDATE admin_catalog SET code=?,name=?,version=version+1 WHERE id=? AND version=?"), item.Code, item.Name, item.ID, item.Version)); err != nil {
			return err
		}
		item.Version = before.Version + 1
		return t.audit(ctx, "catalog.update", item.ID, map[string]any{"before": before, "after": item})
	})
	return item.ID, err
}
func (s *store) deleteCatalog(ctx context.Context, actor, id string, version int64) error {
	return s.transact(ctx, actor, "catalog.manage", func(t *transaction) error {
		before, err := t.catalog(ctx, id)
		if err != nil {
			return domainError(err)
		}
		if err := affected(t.tx.ExecContext(ctx, s.bind("DELETE FROM admin_catalog WHERE id=? AND version=?"), id, version)); err != nil {
			return err
		}
		return t.audit(ctx, "catalog.delete", id, before)
	})
}
func (t *transaction) catalog(ctx context.Context, id string) (catalogItem, error) {
	var item catalogItem
	err := t.tx.QueryRowContext(ctx, t.s.bind("SELECT id,code,name,version FROM admin_catalog WHERE id=?"), id).Scan(&item.ID, &item.Code, &item.Name, &item.Version)
	return item, err
}
func (s *store) saveUser(ctx context.Context, actor string, u user, password string, creating bool) (string, error) {
	if err := validateUser(u.Username, u.DisplayName, password, creating, u.Active); err != nil {
		return "", err
	}
	var encoded string
	var err error
	if password != "" {
		encoded, err = s.hasher.Hash(ctx, password)
		if err != nil {
			return "", err
		}
	}
	if creating {
		u.ID, err = newID()
		if err != nil {
			return "", err
		}
	}
	err = s.transact(ctx, actor, "users.manage", func(t *transaction) error {
		var duplicate int
		if err := t.tx.QueryRowContext(ctx, s.bind("SELECT COUNT(*) FROM admin_users WHERE username=? AND id<>?"), u.Username, u.ID).Scan(&duplicate); err != nil {
			return err
		}
		if duplicate > 0 {
			return invalid("Это имя пользователя уже используется")
		}
		if creating {
			if _, err := t.tx.ExecContext(ctx, s.bind("INSERT INTO admin_users(id,username,display_name,password_hash,active,auth_version,version) VALUES(?,?,?,?,?,1,1)"), u.ID, u.Username, u.DisplayName, encoded, u.Active); err != nil {
				return err
			}
			u.Version = 1
			return t.audit(ctx, "user.create", u.ID, visibleUser(u))
		}
		before, err := t.user(ctx, u.ID)
		if err != nil {
			return domainError(err)
		}
		if err := t.controlUser(ctx, u.ID); err != nil {
			return err
		}
		epoch := before.Epoch
		if encoded != "" || u.Active != before.Active {
			epoch++
		}
		if encoded == "" {
			err = affected(t.tx.ExecContext(ctx, s.bind("UPDATE admin_users SET username=?,display_name=?,active=?,auth_version=?,version=version+1 WHERE id=? AND version=?"), u.Username, u.DisplayName, u.Active, epoch, u.ID, u.Version))
		} else {
			err = affected(t.tx.ExecContext(ctx, s.bind("UPDATE admin_users SET username=?,display_name=?,active=?,auth_version=?,password_hash=?,version=version+1 WHERE id=? AND version=?"), u.Username, u.DisplayName, u.Active, epoch, encoded, u.ID, u.Version))
		}
		if err != nil {
			return err
		}
		if err = t.lastAdministrator(ctx); err != nil {
			return err
		}
		u.Version = before.Version + 1
		return t.audit(ctx, "user.update", u.ID, map[string]any{"before": visibleUser(before), "after": visibleUser(u), "password_changed": encoded != ""})
	})
	return u.ID, err
}
func (s *store) deleteUser(ctx context.Context, actor, id string, version int64) error {
	return s.transact(ctx, actor, "users.manage", func(t *transaction) error {
		before, err := t.user(ctx, id)
		if err != nil {
			return domainError(err)
		}
		if err := t.controlUser(ctx, id); err != nil {
			return err
		}
		if _, err = t.tx.ExecContext(ctx, s.bind("DELETE FROM admin_auth_assignment WHERE user_id=?"), id); err != nil {
			return err
		}
		if err := affected(t.tx.ExecContext(ctx, s.bind("DELETE FROM admin_users WHERE id=? AND version=?"), id, version)); err != nil {
			return err
		}
		if err := t.lastAdministrator(ctx); err != nil {
			return err
		}
		return t.audit(ctx, "user.delete", id, visibleUser(before))
	})
}
func (t *transaction) role(ctx context.Context, name string) (role, error) {
	item, found, err := t.rbac.Item(ctx, name)
	if err != nil {
		return role{}, err
	}
	if !found || item.Kind != rbac.Role {
		return role{}, sql.ErrNoRows
	}
	children, err := t.rbac.Children(ctx, name)
	if err != nil {
		return role{}, err
	}
	r := role{Name: name, Description: item.Description, Protected: protectedRole(name)}
	for _, c := range children {
		if c.Kind != rbac.Permission {
			return role{}, errors.New("admin: unsupported role hierarchy in application data")
		}
		r.Permissions = append(r.Permissions, c.Name)
	}
	err = t.tx.QueryRowContext(ctx, t.s.bind("SELECT version FROM admin_role_version WHERE name=?"), name).Scan(&r.Version)
	return r, err
}
func (s *store) saveRole(ctx context.Context, actor string, r role, creating bool) error {
	if !namePattern.MatchString(r.Name) || !text(r.Description, 120) {
		return invalid("Название роли: 2–64 латинских символа; описание: 1–120 символов")
	}
	values, err := normalizedPermissions(r.Permissions)
	if err != nil {
		return invalid("Неизвестные или повторяющиеся разрешения")
	}
	r.Permissions = values
	return s.transact(ctx, actor, "rbac.manage", func(t *transaction) error {
		if protectedRole(r.Name) {
			return web.NewHTTPError(403, "Системные роли защищены", nil)
		}
		if err := t.grantCeiling(ctx, r.Permissions); err != nil {
			return err
		}
		if creating {
			var count int
			if err := t.tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_auth_item WHERE type='role'").Scan(&count); err != nil {
				return err
			}
			if count >= 200 {
				return invalid("Достигнут лимит 200 ролей")
			}
			_, found, err := t.rbac.Item(ctx, r.Name)
			if err != nil {
				return err
			}
			if found {
				return invalid("Роль уже существует")
			}
			if err = t.rbac.AddRole(ctx, r.Name, r.Description); err != nil {
				return err
			}
			if _, err = t.tx.ExecContext(ctx, s.bind("INSERT INTO admin_role_version(name,version) VALUES(?,1)"), r.Name); err != nil {
				return err
			}
		} else {
			before, err := t.role(ctx, r.Name)
			if err != nil {
				return domainError(err)
			}
			if err = t.grantCeiling(ctx, before.Permissions); err != nil {
				return err
			}
			if err = affected(t.tx.ExecContext(ctx, s.bind("UPDATE admin_role_version SET version=version+1 WHERE name=? AND version=?"), r.Name, r.Version)); err != nil {
				return err
			}
			if err = t.rbac.UpdateItem(ctx, rbac.Item{Name: r.Name, Kind: rbac.Role, Description: r.Description}); err != nil {
				return err
			}
			for _, p := range before.Permissions {
				if err = t.rbac.RemoveChild(ctx, r.Name, p); err != nil {
					return err
				}
			}
		}
		for _, p := range r.Permissions {
			if err := t.rbac.AddChild(ctx, r.Name, p); err != nil {
				return err
			}
		}
		action := "role.update"
		if creating {
			action = "role.create"
		}
		return t.audit(ctx, action, r.Name, map[string]any{"description": r.Description, "permissions": r.Permissions})
	})
}
func (s *store) deleteRole(ctx context.Context, actor, name string, version int64) error {
	return s.transact(ctx, actor, "rbac.manage", func(t *transaction) error {
		if protectedRole(name) {
			return web.NewHTTPError(403, "Системные роли защищены", nil)
		}
		r, err := t.role(ctx, name)
		if err != nil {
			return domainError(err)
		}
		if r.Version != version {
			return web.NewHTTPError(409, "Роль изменилась", nil)
		}
		if err = t.grantCeiling(ctx, r.Permissions); err != nil {
			return err
		}
		if err = t.rbac.RemoveItem(ctx, name); err != nil {
			return err
		}
		return t.audit(ctx, "role.delete", name, map[string]any{"permissions": r.Permissions})
	})
}
func (s *store) assignment(ctx context.Context, actor, id, name string, version int64, add bool) error {
	return s.transact(ctx, actor, "rbac.manage", func(t *transaction) error {
		u, err := t.user(ctx, id)
		if err != nil {
			return domainError(err)
		}
		if err = t.controlUser(ctx, id); err != nil {
			return err
		}
		r, err := t.role(ctx, name)
		if err != nil {
			return domainError(err)
		}
		if err = t.grantCeiling(ctx, r.Permissions); err != nil {
			return err
		}
		if err = affected(t.tx.ExecContext(ctx, s.bind("UPDATE admin_users SET version=version+1 WHERE id=? AND version=?"), id, version)); err != nil {
			return err
		}
		action := "assignment.revoke"
		if add {
			roles, countErr := t.rbac.Assignments(ctx, id)
			if countErr != nil {
				return countErr
			}
			if len(roles) >= 20 {
				return invalid("Не более 20 ролей на пользователя")
			}
			err = t.rbac.Assign(ctx, id, name)
			action = "assignment.add"
		} else {
			err = t.rbac.Revoke(ctx, id, name)
		}
		if err != nil {
			if errors.Is(err, rbac.ErrAlreadyExists) || errors.Is(err, rbac.ErrNotFound) {
				return web.NewHTTPError(409, "Назначения изменились", err)
			}
			return err
		}
		if err = t.lastAdministrator(ctx); err != nil {
			return err
		}
		return t.audit(ctx, action, id, map[string]any{"username": u.Username, "role": name})
	})
}
