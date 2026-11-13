package main

import "github.com/chitushka/framework/web"

func (s *site) catalogRoutes(r *web.Router) error {
	if err := r.GET("/admin/catalog", s.protect("catalog.view", func(c *web.Context) error {
		p, err := s.loadPage(c, "catalog.view", "Каталог", "catalog", func(t *transaction, p *pageData) error {
			var err error
			p.Catalog, err = t.listCatalog(c.Request.Context(), p.Page)
			return err
		})
		if err != nil {
			return err
		}
		return s.render(c, 200, "catalog", p)
	})); err != nil {
		return err
	}
	for _, creating := range []bool{true, false} {
		path := "/admin/catalog/{id}/edit"
		if creating {
			path = "/admin/catalog/new"
		}
		load := func(c *web.Context) (pageData, error) {
			return s.loadPage(c, "catalog.manage", "Запись каталога", "catalog", func(t *transaction, p *pageData) error {
				p.Creating = creating
				p.Item.Version = 1
				if !creating {
					var err error
					p.Item, err = t.catalog(c.Request.Context(), c.Param("id"))
					return err
				}
				return nil
			})
		}
		if err := r.GET(path, s.protect("catalog.manage", func(c *web.Context) error {
			p, err := load(c)
			if err != nil {
				return domainError(err)
			}
			return s.render(c, 200, "catalog_form", p)
		})); err != nil {
			return err
		}
		if err := r.POST(path, s.protect("catalog.manage", func(c *web.Context) error {
			if err := form(c, "", "code", "name", "version"); err != nil {
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
			item := catalogItem{ID: c.Param("id"), Code: c.Request.PostForm.Get("code"), Name: c.Request.PostForm.Get("name"), Version: v}
			p.Item = item
			_, err = s.store.saveCatalog(c.Request.Context(), token(c), item, creating)
			if err != nil {
				return s.problem(c, err, p, "catalog_form")
			}
			return c.Redirect(303, "/admin/catalog")
		})); err != nil {
			return err
		}
	}
	return r.POST("/admin/catalog/{id}/delete", s.protect("catalog.manage", func(c *web.Context) error {
		if err := form(c, "", "version"); err != nil {
			return err
		}
		v, err := version(c)
		if err != nil {
			return err
		}
		if err = s.store.deleteCatalog(c.Request.Context(), token(c), c.Param("id"), v); err != nil {
			return s.problem(c, err, pageData{Title: "Удаление"}, "error")
		}
		return c.Redirect(303, "/admin/catalog")
	}))
}
