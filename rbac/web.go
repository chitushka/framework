package rbac

import (
	"net/http"

	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/web"
)

// ParamsFunc derives contextual rule parameters from the current request.
type ParamsFunc func(*web.Context) map[string]any

// Require creates route/controller middleware for a permission.
func Require(manager *Manager, permission string, params ParamsFunc) web.Middleware {
	return func(next web.Handler) web.Handler {
		return func(ctx *web.Context) error {
			user, found := auth.FromContext(ctx.Request.Context())
			if !found || user.IsGuest() {
				return web.NewHTTPError(http.StatusUnauthorized, "authentication required", nil)
			}
			var values map[string]any
			if params != nil {
				values = params(ctx)
			}
			allowed, err := manager.CheckAccess(ctx.Request.Context(), user.ID(), permission, values)
			if err != nil {
				return err
			}
			if !allowed {
				return web.NewHTTPError(http.StatusForbidden, "permission denied", nil)
			}
			return next(ctx)
		}
	}
}

// TemplateFunc creates an error-free permission helper suitable for html/template.
// Storage or rule errors fail closed.
func (manager *Manager) TemplateFunc(ctx *web.Context, params map[string]any) func(string) bool {
	return func(permission string) bool {
		user, found := auth.FromContext(ctx.Request.Context())
		if !found || user.IsGuest() {
			return false
		}
		allowed, err := manager.CheckAccess(ctx.Request.Context(), user.ID(), permission, params)
		return err == nil && allowed
	}
}
