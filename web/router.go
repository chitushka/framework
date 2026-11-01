package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// RouteOption настраивает маршрут во время регистрации.
type RouteOption func(*route) error

// WithName назначает маршруту уникальное имя для обратной генерации URL.
func WithName(name string) RouteOption {
	return func(item *route) error {
		if name == "" {
			return errors.New("web: имя маршрута не может быть пустым")
		}
		item.name = name
		return nil
	}
}

type segment struct {
	literal string
	param   string
}

type route struct {
	method   string
	pattern  string
	name     string
	segments []segment
	handler  Handler
}

// Router сопоставляет HTTP-запросы со строгими параметризованными маршрутами.
type Router struct {
	mu           sync.RWMutex
	routes       []*route
	named        map[string]*route
	middleware   []Middleware
	errorHandler ErrorHandler
}

// NewRouter создаёт пустой маршрутизатор.
func NewRouter() *Router {
	return &Router{
		named:        make(map[string]*route),
		errorHandler: defaultErrorHandler,
	}
}

// Use добавляет глобальные middleware в порядке объявления.
func (r *Router) Use(middleware ...Middleware) error {
	for _, item := range middleware {
		if item == nil {
			return errors.New("web: middleware не может быть nil")
		}
	}
	r.mu.Lock()
	r.middleware = append(r.middleware, middleware...)
	r.mu.Unlock()
	return nil
}

// SetErrorHandler заменяет стандартный JSON-обработчик ошибок.
func (r *Router) SetErrorHandler(handler ErrorHandler) error {
	if handler == nil {
		return errors.New("web: обработчик ошибок не может быть nil")
	}
	r.mu.Lock()
	r.errorHandler = handler
	r.mu.Unlock()
	return nil
}

// Handle регистрирует маршрут для HTTP-метода и шаблона.
// Параметры пути задаются как /users/{id}.
func (r *Router) Handle(method, pattern string, handler Handler, options ...RouteOption) error {
	if method == "" {
		return errors.New("web: HTTP-метод обязателен")
	}
	if handler == nil {
		return errors.New("web: обработчик маршрута не может быть nil")
	}
	segments, err := parsePattern(pattern)
	if err != nil {
		return err
	}
	item := &route{method: strings.ToUpper(method), pattern: pattern, segments: segments, handler: handler}
	for _, option := range options {
		if option == nil {
			return errors.New("web: опция маршрута не может быть nil")
		}
		if err := option(item); err != nil {
			return err
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.routes {
		if existing.method == item.method && existing.pattern == item.pattern {
			return fmt.Errorf("web: маршрут %s %s уже зарегистрирован", item.method, pattern)
		}
	}
	if item.name != "" {
		if _, exists := r.named[item.name]; exists {
			return fmt.Errorf("web: имя маршрута %q уже зарегистрировано", item.name)
		}
		r.named[item.name] = item
	}
	r.routes = append(r.routes, item)
	return nil
}

// GET регистрирует GET-маршрут.
func (r *Router) GET(pattern string, handler Handler, options ...RouteOption) error {
	return r.Handle(http.MethodGet, pattern, handler, options...)
}

// POST регистрирует POST-маршрут.
func (r *Router) POST(pattern string, handler Handler, options ...RouteOption) error {
	return r.Handle(http.MethodPost, pattern, handler, options...)
}

// URL строит путь именованного маршрута, экранируя значения параметров.
func (r *Router) URL(name string, params map[string]string) (string, error) {
	r.mu.RLock()
	item := r.named[name]
	r.mu.RUnlock()
	if item == nil {
		return "", fmt.Errorf("web: маршрут %q не найден", name)
	}
	if len(item.segments) == 0 {
		return "/", nil
	}
	parts := make([]string, len(item.segments))
	for i, part := range item.segments {
		if part.param == "" {
			parts[i] = part.literal
			continue
		}
		value, exists := params[part.param]
		if !exists || value == "" {
			return "", fmt.Errorf("web: отсутствует параметр %q маршрута %q", part.param, name)
		}
		parts[i] = url.PathEscape(value)
	}
	return "/" + strings.Join(parts, "/"), nil
}

// ServeHTTP реализует http.Handler.
func (r *Router) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	r.mu.RLock()
	routes := append([]*route(nil), r.routes...)
	middleware := append([]Middleware(nil), r.middleware...)
	errorHandler := r.errorHandler
	r.mu.RUnlock()

	path := splitPath(request.URL.Path)
	methodMatched := false
	for _, item := range routes {
		params, matched := match(item.segments, path)
		if !matched {
			continue
		}
		if item.method != request.Method {
			methodMatched = true
			continue
		}
		context := &Context{Writer: writer, Request: request, params: params}
		handler := item.handler
		for i := len(middleware) - 1; i >= 0; i-- {
			handler = middleware[i](handler)
		}
		if err := handler(context); err != nil {
			errorHandler(context, err)
		}
		return
	}

	context := &Context{Writer: writer, Request: request}
	if methodMatched {
		errorHandler(context, NewHTTPError(http.StatusMethodNotAllowed, http.StatusText(http.StatusMethodNotAllowed), nil))
		return
	}
	errorHandler(context, NewHTTPError(http.StatusNotFound, http.StatusText(http.StatusNotFound), nil))
}

func parsePattern(pattern string) ([]segment, error) {
	if pattern == "" || pattern[0] != '/' {
		return nil, errors.New("web: шаблон маршрута должен начинаться с /")
	}
	parts := splitPath(pattern)
	segments := make([]segment, len(parts))
	seen := make(map[string]struct{})
	for i, part := range parts {
		if strings.HasPrefix(part, "{") || strings.HasSuffix(part, "}") {
			if len(part) < 3 || part[0] != '{' || part[len(part)-1] != '}' {
				return nil, fmt.Errorf("web: некорректный сегмент %q", part)
			}
			name := part[1 : len(part)-1]
			if strings.ContainsAny(name, "{} /") {
				return nil, fmt.Errorf("web: некорректное имя параметра %q", name)
			}
			if _, exists := seen[name]; exists {
				return nil, fmt.Errorf("web: параметр %q повторяется", name)
			}
			seen[name] = struct{}{}
			segments[i].param = name
			continue
		}
		segments[i].literal = part
	}
	return segments, nil
}

func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func match(pattern []segment, path []string) (map[string]string, bool) {
	if len(pattern) != len(path) {
		return nil, false
	}
	params := make(map[string]string)
	for i, expected := range pattern {
		if expected.param != "" {
			if path[i] == "" {
				return nil, false
			}
			params[expected.param] = path[i]
			continue
		}
		if expected.literal != path[i] {
			return nil, false
		}
	}
	return params, true
}
