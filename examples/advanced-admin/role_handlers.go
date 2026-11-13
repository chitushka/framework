package main

import "github.com/chitushka/framework/web"

func (s *site) roleRoutes(r *web.Router) error {
	if err := r.GET("/admin/roles", s.protect("rbac.manage", func(c *web.Context) error {
		p, err := s.loadPage(c, "rbac.manage", "Роли и разрешения", "roles", func(t *transaction, p *pageData) error {
			var err error
			p.Roles, err = t.listRoles(c.Request.Context())
			return err
		})
		if err != nil {
			return err
		}
		return s.render(c, 200, "roles", p)
	})); err != nil {
		return err
	}
	for _, creating := range []bool{true, false} {
		path := "/admin/roles/{name}/edit"
		if creating {
			path = "/admin/roles/new"
		}
		load := func(c *web.Context) (pageData, error) {
			return s.loadPage(c, "rbac.manage", "Роль", "roles", func(t *transaction, p *pageData) error {
				p.Creating = creating
				p.Role.Version = 1
				if !creating {
					var err error
					p.Role, err = t.role(c.Request.Context(), c.Param("name"))
					return err
				}
				return nil
			})
		}
		if err := r.GET(path, s.protect("rbac.manage", func(c *web.Context) error {
			p, err := load(c)
			if err != nil {
				return domainError(err)
			}
			return s.render(c, 200, "role_form", p)
		})); err != nil {
			return err
		}
		if err := r.POST(path, s.protect("rbac.manage", func(c *web.Context) error {
			if err := form(c, "permissions", "name", "description", "permissions", "version"); err != nil {
				return err
			}
			p, err := load(c)
			if err != nil {
				return domainError(err)
			}
			v, err := version(c)
			if err != nil {
				return err
			}
			name := c.Param("name")
			if creating {
				name = c.Request.PostForm.Get("name")
			} else if c.Request.PostForm.Get("name") != name {
				return web.NewHTTPError(400, "Имя роли неизменно", nil)
			}
			value := role{Name: name, Description: c.Request.PostForm.Get("description"), Permissions: c.Request.PostForm["permissions"], Version: v}
			p.Role = value
			if err = s.store.saveRole(c.Request.Context(), token(c), value, creating); err != nil {
				return s.problem(c, err, p, "role_form")
			}
			return c.Redirect(303, "/admin/roles")
		})); err != nil {
			return err
		}
	}
	if err := r.POST("/admin/roles/{name}/delete", s.protect("rbac.manage", func(c *web.Context) error {
		if err := form(c, "", "version"); err != nil {
			return err
		}
		v, err := version(c)
		if err != nil {
			return err
		}
		if err = s.store.deleteRole(c.Request.Context(), token(c), c.Param("name"), v); err != nil {
			return s.problem(c, err, pageData{Title: "Удаление роли"}, "error")
		}
		return c.Redirect(303, "/admin/roles")
	})); err != nil {
		return err
	}
	if err := r.GET("/admin/assignments", s.protect("rbac.manage", func(c *web.Context) error {
		p, err := s.loadPage(c, "rbac.manage", "Назначения ролей", "assignments", func(t *transaction, p *pageData) error {
			var err error
			p.Assignments, err = t.listAssignments(c.Request.Context(), p.Page)
			if err != nil {
				return err
			}
			roles, err := t.listRoles(c.Request.Context())
			if err != nil {
				return err
			}
			for _, r := range roles {
				allowed := true
				for _, permission := range r.Permissions {
					if !p.Capabilities[permission] {
						allowed = false
					}
				}
				if allowed {
					p.Roles = append(p.Roles, r)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		return s.render(c, 200, "assignments", p)
	})); err != nil {
		return err
	}
	for _, add := range []bool{true, false} {
		path := "/admin/assignments/revoke"
		if add {
			path = "/admin/assignments/add"
		}
		if err := r.POST(path, s.protect("rbac.manage", func(c *web.Context) error {
			if err := form(c, "", "user_id", "role", "version"); err != nil {
				return err
			}
			v, err := version(c)
			if err != nil {
				return err
			}
			if err = s.store.assignment(c.Request.Context(), token(c), c.Request.PostForm.Get("user_id"), c.Request.PostForm.Get("role"), v, add); err != nil {
				return s.problem(c, err, pageData{Title: "Назначение роли"}, "error")
			}
			return c.Redirect(303, "/admin/assignments")
		})); err != nil {
			return err
		}
	}
	return r.GET("/admin/audit", s.protect("audit.view", func(c *web.Context) error {
		p, err := s.loadPage(c, "audit.view", "Аудит изменений", "audit", func(t *transaction, p *pageData) error {
			var err error
			p.Audit, err = t.listAudit(c.Request.Context(), p.Page)
			return err
		})
		if err != nil {
			return err
		}
		return s.render(c, 200, "audit", p)
	}))
}
