// Package security contains HTTP protections that are independent of application policy.
package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

// CSRFConfig controls token transport. Header supports Vue/fetch clients; FormField supports HTML forms.
type CSRFConfig struct {
	Header    string
	FormField string
}

// CSRF protects unsafe HTTP methods with a session-bound random token.
func CSRF(config CSRFConfig) web.Middleware {
	if config.Header == "" {
		config.Header = "X-CSRF-Token"
	}
	if config.FormField == "" {
		config.FormField = "_csrf"
	}
	return func(next web.Handler) web.Handler {
		return func(ctx *web.Context) error {
			current, ok := session.FromContext(ctx.Request.Context())
			if !ok {
				return errors.New("security: session middleware must run before CSRF middleware")
			}
			var token string
			found, err := current.Get("security.csrf", &token)
			if err != nil {
				return err
			}
			if !found || token == "" {
				token, err = randomToken()
				if err != nil {
					return err
				}
				if err = current.Set("security.csrf", token); err != nil {
					return err
				}
			}
			ctx.Writer.Header().Set(config.Header, token)
			if isSafe(ctx.Request.Method) {
				return next(ctx)
			}
			provided := ctx.Request.Header.Get(config.Header)
			if provided == "" {
				if err := ctx.Request.ParseForm(); err != nil {
					return web.NewHTTPError(http.StatusBadRequest, "invalid form", err)
				}
				provided = ctx.Request.Form.Get(config.FormField)
			}
			if len(provided) != len(token) || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
				return web.NewHTTPError(http.StatusForbidden, "invalid CSRF token", nil)
			}
			return next(ctx)
		}
	}
}

func isSafe(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}
func randomToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

// HeadersConfig configures common browser security headers. Empty values are omitted.
type HeadersConfig struct {
	ContentSecurityPolicy string
	FrameOptions          string
	HSTS                  string
	ReferrerPolicy        string
	ContentTypeOptions    string
}

// Headers adds browser security headers before calling the next handler.
func Headers(config HeadersConfig) web.Middleware {
	if config.FrameOptions == "" {
		config.FrameOptions = "DENY"
	}
	if config.ContentTypeOptions == "" {
		config.ContentTypeOptions = "nosniff"
	}
	return func(next web.Handler) web.Handler {
		return func(ctx *web.Context) error {
			setHeader(ctx.Writer.Header(), "Content-Security-Policy", config.ContentSecurityPolicy)
			setHeader(ctx.Writer.Header(), "X-Frame-Options", config.FrameOptions)
			setHeader(ctx.Writer.Header(), "Strict-Transport-Security", config.HSTS)
			setHeader(ctx.Writer.Header(), "Referrer-Policy", config.ReferrerPolicy)
			setHeader(ctx.Writer.Header(), "X-Content-Type-Options", config.ContentTypeOptions)
			return next(ctx)
		}
	}
}
func setHeader(header http.Header, name, value string) {
	if strings.TrimSpace(value) != "" {
		header.Set(name, value)
	}
}
