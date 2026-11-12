// Command advanced-admin demonstrates an administrative application with
// hierarchical RBAC and an in-memory reference-data CRUD.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/rbac"
	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

type identity string

func (value identity) ID() string { return string(value) }

type users struct{}

func (users) FindIdentity(_ context.Context, id string) (auth.Identity, error) {
	if id == "1" || id == "2" {
		return identity(id), nil
	}
	return nil, nil
}

type dictionaryItem struct {
	ID   int    `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type catalog struct {
	mu     sync.RWMutex
	nextID int
	items  map[int]dictionaryItem
}

func newCatalog() *catalog {
	return &catalog{nextID: 2, items: map[int]dictionaryItem{1: {ID: 1, Code: "active", Name: "Active"}}}
}

func (store *catalog) list() []dictionaryItem {
	store.mu.RLock()
	result := make([]dictionaryItem, 0, len(store.items))
	for _, item := range store.items {
		result = append(result, item)
	}
	store.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (store *catalog) create(item dictionaryItem) dictionaryItem {
	store.mu.Lock()
	item.ID = store.nextID
	store.nextID++
	store.items[item.ID] = item
	store.mu.Unlock()
	return item
}

func (store *catalog) update(id int, item dictionaryItem) (dictionaryItem, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, found := store.items[id]; !found {
		return dictionaryItem{}, false
	}
	item.ID = id
	store.items[id] = item
	return item, true
}

func (store *catalog) delete(id int) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, found := store.items[id]; !found {
		return false
	}
	delete(store.items, id)
	return true
}

func newHandler() (http.Handler, error) {
	manager, err := seededRBAC(context.Background())
	if err != nil {
		return nil, err
	}
	sessions, err := session.New(session.NewMemoryBackend(), session.Config{Name: "admin_session"})
	if err != nil {
		return nil, err
	}
	data := newCatalog()
	router := web.NewRouter()
	if err := router.Use(web.Recover, sessions.Middleware, auth.Middleware(users{})); err != nil {
		return nil, err
	}
	if err := router.POST("/login", func(ctx *web.Context) error {
		var input struct {
			Username string `json:"username"`
		}
		if err := ctx.BindJSON(&input); err != nil {
			return err
		}
		id := map[string]string{"admin": "1", "editor": "2"}[input.Username]
		if id == "" {
			return web.NewHTTPError(http.StatusUnauthorized, "unknown demo user", nil)
		}
		if err := auth.Must(ctx.Request.Context()).Login(identity(id)); err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, map[string]string{"user_id": id})
	}, web.WithMiddleware(auth.RequireGuest)); err != nil {
		return nil, err
	}

	view := rbac.Require(manager, "dictionary.view", nil)
	manage := rbac.Require(manager, "dictionary.manage", nil)
	if err := router.GET("/admin/dictionaries", func(ctx *web.Context) error {
		return ctx.JSON(http.StatusOK, map[string]any{"items": data.list()})
	}, web.WithMiddleware(view)); err != nil {
		return nil, err
	}
	if err := router.POST("/admin/dictionaries", func(ctx *web.Context) error {
		var item dictionaryItem
		if err := ctx.BindJSON(&item); err != nil {
			return err
		}
		if item.Code == "" || item.Name == "" {
			return web.NewHTTPError(http.StatusUnprocessableEntity, "code and name are required", nil)
		}
		return ctx.JSON(http.StatusCreated, data.create(item))
	}, web.WithMiddleware(manage)); err != nil {
		return nil, err
	}
	if err := router.Handle(http.MethodPut, "/admin/dictionaries/{id}", func(ctx *web.Context) error {
		id, err := strconv.Atoi(ctx.Param("id"))
		if err != nil {
			return web.NewHTTPError(http.StatusBadRequest, "invalid id", err)
		}
		var item dictionaryItem
		if err := ctx.BindJSON(&item); err != nil {
			return err
		}
		updated, found := data.update(id, item)
		if !found {
			return web.NewHTTPError(http.StatusNotFound, "dictionary item not found", nil)
		}
		return ctx.JSON(http.StatusOK, updated)
	}, web.WithMiddleware(manage), web.Where("id", `[0-9]+`)); err != nil {
		return nil, err
	}
	if err := router.Handle(http.MethodDelete, "/admin/dictionaries/{id}", func(ctx *web.Context) error {
		id, _ := strconv.Atoi(ctx.Param("id"))
		if !data.delete(id) {
			return web.NewHTTPError(http.StatusNotFound, "dictionary item not found", nil)
		}
		return ctx.NoContent(http.StatusNoContent)
	}, web.WithMiddleware(manage), web.Where("id", `[0-9]+`)); err != nil {
		return nil, err
	}
	if err := router.GET("/admin/rbac", func(ctx *web.Context) error {
		roles, err := manager.Roles(ctx.Request.Context())
		if err != nil {
			return err
		}
		permissions, err := manager.Permissions(ctx.Request.Context())
		if err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, map[string]any{"roles": roles, "permissions": permissions})
	}, web.WithMiddleware(manage)); err != nil {
		return nil, err
	}
	return router, nil
}

func seededRBAC(ctx context.Context) (*rbac.Manager, error) {
	manager, err := rbac.New(rbac.NewMemoryStore())
	if err != nil {
		return nil, err
	}
	for _, item := range []rbac.Item{
		{Name: "administrator", Kind: rbac.Role, Description: "Full administration"},
		{Name: "editor", Kind: rbac.Role, Description: "Reference-data reader"},
		{Name: "dictionary.view", Kind: rbac.Permission},
		{Name: "dictionary.manage", Kind: rbac.Permission},
	} {
		if err := manager.AddItem(ctx, item); err != nil {
			return nil, err
		}
	}
	for _, edge := range [][2]string{{"administrator", "editor"}, {"administrator", "dictionary.manage"}, {"editor", "dictionary.view"}} {
		if err := manager.AddChild(ctx, edge[0], edge[1]); err != nil {
			return nil, err
		}
	}
	if err := manager.Assign(ctx, "1", "administrator"); err != nil {
		return nil, err
	}
	if err := manager.Assign(ctx, "2", "editor"); err != nil {
		return nil, err
	}
	return manager, nil
}

func main() {
	handler, err := newHandler()
	if err != nil {
		log.Fatal(err)
	}
	server, err := web.NewServer(web.ServerConfig{Address: ":8081", ShutdownTimeout: 5 * time.Second}, handler)
	if err != nil {
		log.Fatal(err)
	}
	application := app.New(nil, nil)
	if err := application.AddModule(server); err != nil {
		log.Fatal(err)
	}
	ctx, stop := app.SignalContext(context.Background())
	defer stop()
	if err := application.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
