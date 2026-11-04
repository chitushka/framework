package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/chitushka/framework/internal/httpwriter"
)

type productionKey struct{}

// IsProduction reports the explicit request profile; environment variables and
// client headers never select or disable the profile.
func IsProduction(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	production, _ := ctx.Value(productionKey{}).(bool)
	return production
}

// NewProductionHandler must wrap the complete public handler chain, including
// observability and proxy middleware. It disables framework debug surfaces,
// sanitizes framework error handling and recovers panics without exposing values.
// It cannot sanitize arbitrary application bodies or middleware outside it.
func NewProductionHandler(next http.Handler) (http.Handler, error) {
	if next == nil {
		return nil, errors.New("web: production handler is required")
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request = request.WithContext(context.WithValue(request.Context(), productionKey{}, true))
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Frame-Options", "DENY")
		if request.URL.Path == "/debug" || strings.HasPrefix(request.URL.Path, "/debug/") {
			defaultErrorHandler(&Context{Writer: writer, Request: request}, NewHTTPError(http.StatusNotFound, "", nil))
			return
		}
		recorded := &responseWriteRecorder{ResponseWriter: writer}
		preserved := httpwriter.Preserve(recorded)
		defer func() {
			if recovered := recover(); recovered != nil {
				if recorded.started {
					panic(http.ErrAbortHandler)
				}
				// Nothing was sent: discard uncommitted cookies, validators and
				// application headers rather than framing the generic error with them.
				clear(writer.Header())
				writer.Header().Set("X-Content-Type-Options", "nosniff")
				writer.Header().Set("Referrer-Policy", "no-referrer")
				writer.Header().Set("X-Frame-Options", "DENY")
				defaultErrorHandler(&Context{Writer: preserved, Request: request}, errors.New("request failed"))
			}
		}()
		next.ServeHTTP(preserved, request)
	}), nil
}

func requestErrorHandler(request *http.Request, configured ErrorHandler) ErrorHandler {
	if IsProduction(request.Context()) {
		return defaultErrorHandler
	}
	return configured
}
