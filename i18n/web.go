package i18n

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/chitushka/framework/web"
)

type MiddlewareConfig struct {
	QueryParameter string
	CookieName     string
	CookiePath     string
	CookieTTL      time.Duration
	Secure         bool
}

// Middleware selects a supported locale in query -> cookie -> Accept-Language
// order and stores it in the request context.
func Middleware(translator *Translator, config MiddlewareConfig) web.Middleware {
	if config.QueryParameter == "" {
		config.QueryParameter = "lang"
	}
	if config.CookieName == "" {
		config.CookieName = "lang"
	}
	if config.CookiePath == "" {
		config.CookiePath = "/"
	}
	if config.CookieTTL <= 0 {
		config.CookieTTL = 365 * 24 * time.Hour
	}
	return func(next web.Handler) web.Handler {
		return func(ctx *web.Context) error {
			if translator == nil {
				return errors.New("i18n: translator is required")
			}
			selected := Locale("")
			persist := false
			if value := ctx.Request.URL.Query().Get(config.QueryParameter); value != "" {
				if locale, err := ParseLocale(value); err == nil {
					if matched, found := translator.MatchFound(locale); found {
						selected = matched
						persist = true
					}
				}
			}
			if selected == "" {
				if cookie, err := ctx.Request.Cookie(config.CookieName); err == nil {
					if locale, parseErr := ParseLocale(cookie.Value); parseErr == nil {
						if matched, found := translator.MatchFound(locale); found {
							selected = matched
						}
					}
				}
			}
			if selected == "" {
				preferred := ParseAcceptLanguage(ctx.Request.Header.Get("Accept-Language"))
				if matched, found := translator.MatchFound(preferred...); found {
					selected = matched
				}
			}
			if selected == "" {
				selected = translator.matcher.Fallback()
			}
			requestContext := context.WithValue(ctx.Request.Context(), localeContextKey{}, selected)
			ctx.Request = ctx.Request.WithContext(requestContext)
			ctx.Writer.Header().Set("Content-Language", selected.String())
			appendVary(ctx.Writer.Header(), "Accept-Language")
			if persist {
				maxAge := int(config.CookieTTL.Seconds())
				http.SetCookie(ctx.Writer, &http.Cookie{Name: config.CookieName, Value: selected.String(), Path: config.CookiePath, MaxAge: maxAge, Secure: config.Secure, SameSite: http.SameSiteLaxMode})
			}
			return next(ctx)
		}
	}
}

func appendVary(header http.Header, value string) {
	for _, line := range header.Values("Vary") {
		for _, existing := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(existing), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}
