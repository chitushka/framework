package main

import (
	"context"
	"time"
)

const pageSize = 25

type assignmentView struct {
	User  user
	Roles []string
}
type pageData struct {
	Title, CSRF, Username, Message, Section string
	Capabilities                            map[string]bool
	Catalog                                 []catalogItem
	Item                                    catalogItem
	Users                                   []user
	User                                    user
	Roles                                   []role
	Role                                    role
	PermissionNames                         []string
	Assignments                             []assignmentView
	Audit                                   []auditRecord
	Page, Next, Previous                    int
	Creating                                bool
}

func (t *transaction) page(ctx context.Context, title, section string, page int) (pageData, error) {
	caps, err := t.capabilities(ctx, t.actor.ID)
	return pageData{Title: title, Section: section, Username: t.actor.Username, Capabilities: caps, PermissionNames: permissions, Page: page, Next: page + 1, Previous: max(1, page-1)}, err
}
func (t *transaction) listCatalog(ctx context.Context, page int) ([]catalogItem, error) {
	rows, err := t.tx.QueryContext(ctx, t.s.bind("SELECT id,code,name,version FROM admin_catalog ORDER BY code LIMIT ? OFFSET ?"), pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var list []catalogItem
	for rows.Next() {
		var r catalogItem
		if err := rows.Scan(&r.ID, &r.Code, &r.Name, &r.Version); err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, rows.Err()
}
func (t *transaction) listUsers(ctx context.Context, page int) ([]user, error) {
	rows, err := t.tx.QueryContext(ctx, t.s.bind("SELECT id,username,display_name,active,auth_version,version FROM admin_users ORDER BY username LIMIT ? OFFSET ?"), pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var list []user
	for rows.Next() {
		r, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, rows.Err()
}
func (t *transaction) listRoles(ctx context.Context) ([]role, error) {
	items, err := t.rbac.Roles(ctx)
	if err != nil {
		return nil, err
	}
	var list []role
	for _, item := range items {
		r, err := t.role(ctx, item.Name)
		if err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, nil
}
func (t *transaction) listAssignments(ctx context.Context, page int) ([]assignmentView, error) {
	users, err := t.listUsers(ctx, page)
	if err != nil {
		return nil, err
	}
	var list []assignmentView
	for _, u := range users {
		roles, err := t.rbac.Assignments(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		row := assignmentView{User: u}
		for _, r := range roles {
			row.Roles = append(row.Roles, r.Role)
		}
		list = append(list, row)
	}
	return list, nil
}
func (t *transaction) listAudit(ctx context.Context, page int) ([]auditRecord, error) {
	rows, err := t.tx.QueryContext(ctx, t.s.bind("SELECT id,created_at,actor_name,action,target,details FROM admin_audit ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?"), pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var list []auditRecord
	for rows.Next() {
		var r auditRecord
		var timestamp int64
		if err := rows.Scan(&r.ID, &timestamp, &r.Actor, &r.Action, &r.Target, &r.Details); err != nil {
			return nil, err
		}
		r.When = time.UnixMicro(timestamp).UTC().Format("2006-01-02 15:04:05 UTC")
		list = append(list, r)
	}
	return list, rows.Err()
}
