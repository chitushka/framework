// Package auth provides request-scoped identity management on top of sessions.
package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

// Identity is the authenticated application principal.
type Identity interface{ ID() string }

// IdentityLoader restores an identity from its persistent identifier.
type IdentityLoader interface {
	FindIdentity(context.Context, string) (Identity, error)
}

type contextKey struct{}

// User manages the current identity and its session.
type User struct {
	session  *session.Session
	loader   IdentityLoader
	identity Identity
}

func FromContext(ctx context.Context) (*User, bool) {
	user, ok := ctx.Value(contextKey{}).(*User)
	return user, ok
}
func Must(ctx context.Context) *User {
	user, ok := FromContext(ctx)
	if !ok {
		panic("auth: middleware is not installed")
	}
	return user
}
func (u *User) IsGuest() bool { return u == nil || u.identity == nil }
func (u *User) Identity() Identity {
	if u == nil {
		return nil
	}
	return u.identity
}
func (u *User) ID() string {
	if u == nil || u.identity == nil {
		return ""
	}
	return u.identity.ID()
}

// Login authenticates identity and regenerates the session identifier against fixation.
func (u *User) Login(identity Identity) error {
	if identity == nil || identity.ID() == "" {
		return errors.New("auth: identity and its ID are required")
	}
	if err := u.session.Regenerate(); err != nil {
		return err
	}
	if err := u.session.Set("auth.identity", identity.ID()); err != nil {
		return err
	}
	u.identity = identity
	return nil
}

// Logout removes authentication state and regenerates the anonymous session identifier.
func (u *User) Logout() error {
	u.session.Delete("auth.identity")
	if err := u.session.Regenerate(); err != nil {
		return err
	}
	u.identity = nil
	return nil
}

// Middleware restores User from the session. A missing identity is treated as a guest.
func Middleware(loader IdentityLoader) web.Middleware {
	return func(next web.Handler) web.Handler {
		return func(ctx *web.Context) error {
			if loader == nil {
				return errors.New("auth: identity loader is required")
			}
			currentSession, ok := session.FromContext(ctx.Request.Context())
			if !ok {
				return errors.New("auth: session middleware must run before auth middleware")
			}
			user := &User{session: currentSession, loader: loader}
			var id string
			found, err := currentSession.Get("auth.identity", &id)
			if err != nil {
				return err
			}
			if found && id != "" {
				user.identity, err = loader.FindIdentity(ctx.Request.Context(), id)
				if err != nil {
					return fmt.Errorf("auth: load identity %q: %w", id, err)
				}
				if user.identity == nil {
					currentSession.Delete("auth.identity")
				}
			}
			ctx.Request = ctx.Request.WithContext(context.WithValue(ctx.Request.Context(), contextKey{}, user))
			return next(ctx)
		}
	}
}

// RequireGuest rejects authenticated users.
func RequireGuest(next web.Handler) web.Handler {
	return func(ctx *web.Context) error {
		if !Must(ctx.Request.Context()).IsGuest() {
			return web.NewHTTPError(403, "guest access required", nil)
		}
		return next(ctx)
	}
}

// RequireAuthenticated rejects guests.
func RequireAuthenticated(next web.Handler) web.Handler {
	return func(ctx *web.Context) error {
		if Must(ctx.Request.Context()).IsGuest() {
			return web.NewHTTPError(401, "authentication required", nil)
		}
		return next(ctx)
	}
}
