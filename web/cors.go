package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// CORSConfig uses exact HTTP origins, explicit methods and header names.
// "*" origins are allowed only without credentials. Empty origins deny CORS.
type CORSConfig struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	ExposedHeaders   []string
	AllowCredentials bool
	MaxAgeSeconds    int
}

// NewCORSHandler handles preflight before routing, including error/404 responses.
// It does not authenticate requests or protect cookie endpoints against CSRF.
func NewCORSHandler(next http.Handler, config CORSConfig) (http.Handler, error) {
	if next == nil {
		return nil, errors.New("web: HTTP handler cannot be nil")
	}
	origins := map[string]bool{}
	for _, raw := range config.AllowedOrigins {
		if raw == "*" {
			if config.AllowCredentials {
				return nil, errors.New("web: wildcard origin cannot allow credentials")
			}
			origins[raw] = true
			continue
		}
		origin, err := canonicalOrigin(raw)
		if err != nil {
			return nil, err
		}
		origins[origin] = true
	}
	if len(config.AllowedMethods) == 0 {
		config.AllowedMethods = []string{"GET", "HEAD", "POST"}
	}
	methods, err := corsTokens(config.AllowedMethods, false)
	if err != nil {
		return nil, err
	}
	headers, err := corsTokens(config.AllowedHeaders, true)
	if err != nil {
		return nil, err
	}
	exposed, err := corsTokens(config.ExposedHeaders, true)
	if err != nil {
		return nil, err
	}
	if config.MaxAgeSeconds < 0 || config.MaxAgeSeconds > 86400 {
		return nil, errors.New("web: CORS max age must be between 0 and 86400 seconds")
	}
	// Copy all configuration into immutable values before returning the handler.
	credentials, age := config.AllowCredentials, config.MaxAgeSeconds
	methodList := strings.Join(methods, ", ")
	headerList := strings.Join(headers, ", ")
	exposeList := strings.Join(exposed, ", ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addVary(w.Header(), "Origin")
		if r.Method == http.MethodOptions {
			addVary(w.Header(), "Access-Control-Request-Method")
			addVary(w.Header(), "Access-Control-Request-Headers")
		}
		values := r.Header.Values("Origin")
		if len(values) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		deny := func() {
			defaultErrorHandler(&Context{Writer: w, Request: r}, NewHTTPError(403, "CORS request is not allowed", nil))
		}
		if len(values) != 1 {
			deny()
			return
		}
		origin, err := canonicalOrigin(values[0])
		// A serialized Origin contains no path, trailing slash, credentials or whitespace.
		if err != nil || origin != values[0] {
			deny()
			return
		}
		ctx := &Context{Request: r}
		ownOrigin, ownErr := canonicalOrigin(ctx.Scheme() + "://" + ctx.Host())
		if ownErr == nil && origin == ownOrigin {
			next.ServeHTTP(w, r)
			return
		}
		if !origins[origin] && !origins["*"] {
			deny()
			return
		}
		preflight := r.Method == http.MethodOptions && len(r.Header.Values("Access-Control-Request-Method")) > 0
		method := r.Method
		if preflight {
			requested := r.Header.Values("Access-Control-Request-Method")
			if len(requested) != 1 || !httpToken(requested[0]) {
				deny()
				return
			}
			method = requested[0]
		}
		if !containsToken(methods, method) {
			deny()
			return
		}
		if preflight {
			if requested := strings.Join(r.Header.Values("Access-Control-Request-Headers"), ","); requested != "" {
				for _, name := range strings.Split(requested, ",") {
					name = strings.ToLower(strings.TrimSpace(name))
					if !httpToken(name) || !containsToken(headers, name) {
						deny()
						return
					}
				}
			}
		}
		if origins["*"] {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else {
			w.Header().Set("Access-Control-Allow-Origin", values[0])
		}
		if credentials {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		if preflight {
			w.Header().Set("Access-Control-Allow-Methods", methodList)
			if headerList != "" {
				w.Header().Set("Access-Control-Allow-Headers", headerList)
			}
			if age > 0 {
				w.Header().Set("Access-Control-Max-Age", strconv.Itoa(age))
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if exposeList != "" {
			w.Header().Set("Access-Control-Expose-Headers", exposeList)
		}
		next.ServeHTTP(w, r)
	}), nil
}

func httpToken(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return true
}

func corsTokens(values []string, lower bool) ([]string, error) {
	var result []string
	for _, value := range values {
		if !httpToken(value) || value == "*" {
			return nil, errors.New("web: CORS requires explicit method/header tokens")
		}
		if lower {
			value = strings.ToLower(value)
		}
		if !containsToken(result, value) {
			result = append(result, value)
		}
	}
	return result, nil
}

func containsToken(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func addVary(header http.Header, name string) {
	for _, value := range header.Values("Vary") {
		for _, token := range strings.Split(value, ",") {
			if strings.TrimSpace(token) == "*" || strings.EqualFold(strings.TrimSpace(token), name) {
				return
			}
		}
	}
	header.Add("Vary", name)
}
