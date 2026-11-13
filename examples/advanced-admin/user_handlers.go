package main

import (
	"github.com/chitushka/framework/web"
	"strconv"
)

func (s *site) userRoutes(r *web.Router) error {
	if err := r.GET("/admin/users", s.protect("users.manage", func(c *web.Context) error {
		p, err := s.loadPage(c, "users.manage", "Пользователи", "users", func(t *transaction, p *pageData) error {
			var err error
			p.Users, err = t.listUsers(c.Request.Context(), p.Page)
			return err
		})
		if err != nil {
			return err
		}
		return s.render(c, 200, "users", p)
	})); err != nil {
		return err
	}
	for _, creating := range []bool{true, false} {
		path := "/admin/users/{id}/edit"
		if creating {
			path = "/admin/users/new"
		}
		load := func(c *web.Context) (pageData, error) {
			return s.loadPage(c, "users.manage", "Пользователь", "users", func(t *transaction, p *pageData) error {
				p.Creating = creating
				p.User = user{Version: 1, Active: 1}
				if !creating {
					u, err := t.user(c.Request.Context(), c.Param("id"))
					if err != nil {
						return err
					}
					if err = t.controlUser(c.Request.Context(), u.ID); err != nil {
						return err
					}
					p.User = u
				}
				return nil
			})
		}
		if err := r.GET(path, s.protect("users.manage", func(c *web.Context) error {
			p, err := load(c)
			if err != nil {
				return s.problem(c, domainError(err), pageData{Title: "Доступ ограничен"}, "error")
			}
			return s.render(c, 200, "user_form", p)
		})); err != nil {
			return err
		}
		if err := r.POST(path, s.protect("users.manage", func(c *web.Context) error {
			if err := form(c, "", "username", "display_name", "password", "active", "version"); err != nil {
				return err
			}
			p, err := load(c)
			if err != nil {
				return s.problem(c, domainError(err), pageData{Title: "Доступ ограничен"}, "error")
			}
			v, err := version(c)
			if err != nil {
				return err
			}
			active, err := strconv.Atoi(c.Request.PostForm.Get("active"))
			if err != nil {
				return invalid("Некорректный статус")
			}
			u := user{ID: c.Param("id"), Username: canonicalLogin(c.Request.PostForm.Get("username")), DisplayName: c.Request.PostForm.Get("display_name"), Active: active, Version: v}
			p.User = u
			_, err = s.store.saveUser(c.Request.Context(), token(c), u, c.Request.PostForm.Get("password"), creating)
			if err != nil {
				return s.problem(c, err, p, "user_form")
			}
			return c.Redirect(303, "/admin/users")
		})); err != nil {
			return err
		}
	}
	return r.POST("/admin/users/{id}/delete", s.protect("users.manage", func(c *web.Context) error {
		if err := form(c, "", "version"); err != nil {
			return err
		}
		v, err := version(c)
		if err != nil {
			return err
		}
		if err = s.store.deleteUser(c.Request.Context(), token(c), c.Param("id"), v); err != nil {
			return s.problem(c, err, pageData{Title: "Удаление"}, "error")
		}
		return c.Redirect(303, "/admin/users")
	}))
}
