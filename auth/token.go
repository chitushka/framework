package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/chitushka/framework/web"
)

var ErrInvalidToken = errors.New("auth: invalid token")

// Principal contains verified identity metadata only. Authorization, scopes,
// disabled-user checks and token revocation remain application policy.
type Principal struct {
	Subject   string
	Issuer    string
	Audience  []string
	IssuedAt  time.Time
	ExpiresAt time.Time
	TokenID   string
}

// TokenVerifier must validate authenticity, intended issuer/audience and lifetime
// before returning a nonempty Subject. Invalid credentials return ErrInvalidToken;
// dependency/cancellation errors must remain distinct and fail closed.
type TokenVerifier interface {
	Verify(context.Context, string) (Principal, error)
}

type principalKey struct{}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	principal.Audience = append([]string(nil), principal.Audience...)
	return principal, ok
}

// Bearer accepts one Authorization header only; query/cookie tokens are ignored.
// Verification errors produce generic 401 or 503, without disclosing keys/claims.
func Bearer(verifier TokenVerifier) web.Middleware {
	return func(next web.Handler) web.Handler {
		return func(ctx *web.Context) error {
			ctx.Writer.Header().Set("Cache-Control", "no-store")
			headers := ctx.Request.Header.Values("Authorization")
			var parts []string
			if len(headers) == 1 && len(headers[0]) <= 8200 {
				parts = strings.Fields(headers[0])
			}
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 8192 {
				ctx.Writer.Header().Set("WWW-Authenticate", "Bearer")
				return web.NewHTTPError(http.StatusUnauthorized, "bearer token required", nil)
			}
			if verifier == nil {
				return web.NewHTTPError(http.StatusServiceUnavailable, "authentication unavailable", nil)
			}
			principal, err := verifier.Verify(ctx.Request.Context(), parts[1])
			if err != nil {
				if errors.Is(err, ErrInvalidToken) {
					ctx.Writer.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
					return web.NewHTTPError(http.StatusUnauthorized, "invalid bearer token", nil)
				}
				return web.NewHTTPError(http.StatusServiceUnavailable, "authentication unavailable", err)
			}
			if principal.Subject == "" {
				return web.NewHTTPError(http.StatusServiceUnavailable, "authentication unavailable", nil)
			}
			principal.Audience = append([]string(nil), principal.Audience...)
			ctx.Request = ctx.Request.WithContext(context.WithValue(ctx.Request.Context(), principalKey{}, principal))
			return next(ctx)
		}
	}
}
