package web

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
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

// WithHost ограничивает маршрут доменом. Параметры задаются как {tenant}.example.com.
func WithHost(pattern string) RouteOption {
	return func(item *route) error {
		normalized, segments, err := parseHostPattern(pattern)
		if err != nil {
			return err
		}
		item.hostPattern = normalized
		item.hostSegments = segments
		return nil
	}
}

// Where ограничивает параметр маршрута регулярным выражением.
func Where(parameter, expression string) RouteOption {
	return func(item *route) error {
		if parameter == "" || expression == "" {
			return errors.New("web: параметр и регулярное выражение обязательны")
		}
		compiled, err := regexp.Compile("^(?:" + expression + ")$")
		if err != nil {
			return fmt.Errorf("web: ограничение параметра %q: %w", parameter, err)
		}
		item.constraints[parameter] = compiled
		return nil
	}
}

// WithMiddleware добавляет middleware только для выбранного маршрута.
func WithMiddleware(middleware ...Middleware) RouteOption {
	return func(item *route) error {
		if err := validateMiddleware(middleware); err != nil {
			return err
		}
		item.middleware = append(item.middleware, middleware...)
		return nil
	}
}

type segment struct {
	literal string
	param   string
}

type route struct {
	method       string
	pattern      string
	name         string
	segments     []segment
	hostPattern  string
	hostSegments []segment
	constraints  map[string]*regexp.Regexp
	middleware   []Middleware
	handler      Handler
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
	return &Router{named: make(map[string]*route), errorHandler: defaultErrorHandler}
}

// Use добавляет глобальные middleware в порядке объявления.
func (r *Router) Use(middleware ...Middleware) error {
	if err := validateMiddleware(middleware); err != nil {
		return err
	}
	r.mu.Lock()
	r.middleware = append(r.middleware, middleware...)
	r.mu.Unlock()
	return nil
}

// Group создаёт группу маршрутов с общим префиксом.
func (r *Router) Group(prefix string, configure func(*Group) error) error {
	normalized, err := normalizeGroupPrefix(prefix)
	if err != nil {
		return err
	}
	if configure == nil {
		return errors.New("web: configure группы не может быть nil")
	}
	return configure(&Group{router: r, prefix: normalized})
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
func (r *Router) Handle(method, pattern string, handler Handler, options ...RouteOption) error {
	if method == "" {
		return errors.New("web: HTTP-метод обязателен")
	}
	if handler == nil {
		return errors.New("web: обработчик маршрута не может быть nil")
	}
	segments, err := parsePathPattern(pattern)
	if err != nil {
		return err
	}
	item := &route{method: strings.ToUpper(method), pattern: pattern, segments: segments, constraints: make(map[string]*regexp.Regexp), handler: handler}
	for _, option := range options {
		if option == nil {
			return errors.New("web: опция маршрута не может быть nil")
		}
		if err := option(item); err != nil {
			return err
		}
	}
	if err := validateRouteParameters(item); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.routes {
		if existing.method == item.method && existing.pattern == item.pattern && existing.hostPattern == item.hostPattern {
			return fmt.Errorf("web: маршрут %s %s для host %q уже зарегистрирован", item.method, pattern, item.hostPattern)
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

// URL строит URL именованного маршрута. Для host-маршрута возвращается //host/path.
func (r *Router) URL(name string, params map[string]string) (string, error) {
	r.mu.RLock()
	item := r.named[name]
	r.mu.RUnlock()
	if item == nil {
		return "", fmt.Errorf("web: маршрут %q не найден", name)
	}
	pathParts, err := buildSegments(item.segments, params, item.constraints, false)
	if err != nil {
		return "", fmt.Errorf("web: маршрут %q: %w", name, err)
	}
	path := "/" + strings.Join(pathParts, "/")
	if item.hostPattern == "" {
		return path, nil
	}
	hostParts, err := buildSegments(item.hostSegments, params, item.constraints, true)
	if err != nil {
		return "", fmt.Errorf("web: маршрут %q: %w", name, err)
	}
	return "//" + strings.Join(hostParts, ".") + path, nil
}

// ServeHTTP реализует http.Handler.
func (r *Router) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	r.mu.RLock()
	routes := append([]*route(nil), r.routes...)
	globalMiddleware := append([]Middleware(nil), r.middleware...)
	errorHandler := r.errorHandler
	r.mu.RUnlock()

	path := splitPath(request.URL.Path)
	host := normalizeRequestHost(request.Host)
	methodMatched := false
	for _, item := range routes {
		params, matched := matchRoute(item, path, host)
		if !matched {
			continue
		}
		if item.method != request.Method {
			methodMatched = true
			continue
		}
		context := &Context{Writer: writer, Request: request, params: params}
		handler := item.handler
		middleware := append(append([]Middleware(nil), globalMiddleware...), item.middleware...)
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

// Group хранит общие настройки группы маршрутов.
type Group struct {
	router     *Router
	prefix     string
	host       string
	middleware []Middleware
}

// Use добавляет middleware к последующим маршрутам группы.
func (g *Group) Use(middleware ...Middleware) error {
	if err := validateMiddleware(middleware); err != nil {
		return err
	}
	g.middleware = append(g.middleware, middleware...)
	return nil
}

// Host задаёт общий host-шаблон группы.
func (g *Group) Host(pattern string) error {
	normalized, _, err := parseHostPattern(pattern)
	if err != nil {
		return err
	}
	g.host = normalized
	return nil
}

// Handle регистрирует маршрут внутри группы.
func (g *Group) Handle(method, pattern string, handler Handler, options ...RouteOption) error {
	joined, err := joinRoutePath(g.prefix, pattern)
	if err != nil {
		return err
	}
	groupOptions := make([]RouteOption, 0, 2+len(options))
	if g.host != "" {
		groupOptions = append(groupOptions, WithHost(g.host))
	}
	if len(g.middleware) > 0 {
		groupOptions = append(groupOptions, WithMiddleware(g.middleware...))
	}
	groupOptions = append(groupOptions, options...)
	return g.router.Handle(method, joined, handler, groupOptions...)
}

// GET регистрирует GET-маршрут внутри группы.
func (g *Group) GET(pattern string, handler Handler, options ...RouteOption) error {
	return g.Handle(http.MethodGet, pattern, handler, options...)
}

// POST регистрирует POST-маршрут внутри группы.
func (g *Group) POST(pattern string, handler Handler, options ...RouteOption) error {
	return g.Handle(http.MethodPost, pattern, handler, options...)
}

// Group создаёт вложенную группу, наследующую host и middleware.
func (g *Group) Group(prefix string, configure func(*Group) error) error {
	joined, err := joinGroupPrefix(g.prefix, prefix)
	if err != nil {
		return err
	}
	if configure == nil {
		return errors.New("web: configure группы не может быть nil")
	}
	nested := &Group{router: g.router, prefix: joined, host: g.host, middleware: append([]Middleware(nil), g.middleware...)}
	return configure(nested)
}

func parsePathPattern(pattern string) ([]segment, error) {
	if pattern == "" || pattern[0] != '/' {
		return nil, errors.New("web: шаблон маршрута должен начинаться с /")
	}
	return parseSegments(splitPath(pattern), false)
}

func parseHostPattern(pattern string) (string, []segment, error) {
	normalized := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(pattern), "."))
	if normalized == "" || strings.ContainsAny(normalized, "/ ") {
		return "", nil, errors.New("web: некорректный host-шаблон")
	}
	parts := strings.Split(normalized, ".")
	for _, part := range parts {
		if part == "" {
			return "", nil, errors.New("web: host-шаблон содержит пустой сегмент")
		}
	}
	segments, err := parseSegments(parts, true)
	return normalized, segments, err
}

func parseSegments(parts []string, host bool) ([]segment, error) {
	segments := make([]segment, len(parts))
	seen := make(map[string]struct{})
	for i, part := range parts {
		if strings.HasPrefix(part, "{") || strings.HasSuffix(part, "}") {
			if len(part) < 3 || part[0] != '{' || part[len(part)-1] != '}' {
				return nil, fmt.Errorf("web: некорректный сегмент %q", part)
			}
			name := part[1 : len(part)-1]
			if strings.ContainsAny(name, "{} /.") {
				return nil, fmt.Errorf("web: некорректное имя параметра %q", name)
			}
			if _, exists := seen[name]; exists {
				return nil, fmt.Errorf("web: параметр %q повторяется", name)
			}
			seen[name] = struct{}{}
			segments[i].param = name
			continue
		}
		if host && !validHostLiteral(part) {
			return nil, fmt.Errorf("web: некорректный host-сегмент %q", part)
		}
		segments[i].literal = part
	}
	return segments, nil
}

func validateRouteParameters(item *route) error {
	parameters := make(map[string]struct{})
	allSegments := append(append([]segment(nil), item.segments...), item.hostSegments...)
	for _, part := range allSegments {
		if part.param == "" {
			continue
		}
		if _, exists := parameters[part.param]; exists {
			return fmt.Errorf("web: параметр %q повторяется в path и host", part.param)
		}
		parameters[part.param] = struct{}{}
	}
	for parameter := range item.constraints {
		if _, exists := parameters[parameter]; !exists {
			return fmt.Errorf("web: ограничение задано для неизвестного параметра %q", parameter)
		}
	}
	return nil
}

func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func matchRoute(item *route, path []string, host string) (map[string]string, bool) {
	params, matched := matchSegments(item.segments, path, false)
	if !matched {
		return nil, false
	}
	if item.hostPattern != "" {
		hostParams, hostMatched := matchSegments(item.hostSegments, strings.Split(host, "."), true)
		if !hostMatched {
			return nil, false
		}
		for name, value := range hostParams {
			params[name] = value
		}
	}
	for name, constraint := range item.constraints {
		if !constraint.MatchString(params[name]) {
			return nil, false
		}
	}
	return params, true
}

func matchSegments(pattern []segment, values []string, caseInsensitive bool) (map[string]string, bool) {
	if len(pattern) != len(values) {
		return nil, false
	}
	params := make(map[string]string)
	for i, expected := range pattern {
		value := values[i]
		if expected.param != "" {
			if value == "" {
				return nil, false
			}
			params[expected.param] = value
			continue
		}
		if caseInsensitive {
			if !strings.EqualFold(expected.literal, value) {
				return nil, false
			}
			continue
		}
		if expected.literal != value {
			return nil, false
		}
	}
	return params, true
}

func buildSegments(pattern []segment, params map[string]string, constraints map[string]*regexp.Regexp, host bool) ([]string, error) {
	parts := make([]string, len(pattern))
	for i, part := range pattern {
		if part.param == "" {
			parts[i] = part.literal
			continue
		}
		value, exists := params[part.param]
		if !exists || value == "" {
			return nil, fmt.Errorf("отсутствует параметр %q", part.param)
		}
		if constraint := constraints[part.param]; constraint != nil && !constraint.MatchString(value) {
			return nil, fmt.Errorf("параметр %q не соответствует ограничению", part.param)
		}
		if host {
			if !validHostLabel(value) {
				return nil, fmt.Errorf("параметр host %q содержит недопустимое значение", part.param)
			}
			parts[i] = value
			continue
		}
		parts[i] = url.PathEscape(value)
	}
	return parts, nil
}

func normalizeRequestHost(hostPort string) string {
	host := strings.TrimSpace(hostPort)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func validHostLiteral(value string) bool {
	if strings.Contains(value, ":") {
		return net.ParseIP(value) != nil
	}
	return validHostLabel(value)
}

func validHostLabel(value string) bool {
	if value == "" || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validateMiddleware(middleware []Middleware) error {
	for _, item := range middleware {
		if item == nil {
			return errors.New("web: middleware не может быть nil")
		}
	}
	return nil
}

func normalizeGroupPrefix(prefix string) (string, error) {
	if prefix == "" || prefix == "/" {
		return "", nil
	}
	if prefix[0] != '/' || strings.ContainsAny(prefix, "?#") {
		return "", errors.New("web: префикс группы должен начинаться с / и не содержать query или fragment")
	}
	return strings.TrimSuffix(prefix, "/"), nil
}

func joinGroupPrefix(parent, child string) (string, error) {
	normalized, err := normalizeGroupPrefix(child)
	if err != nil {
		return "", err
	}
	return parent + normalized, nil
}

func joinRoutePath(prefix, pattern string) (string, error) {
	if pattern == "" || pattern[0] != '/' {
		return "", errors.New("web: шаблон маршрута группы должен начинаться с /")
	}
	if prefix == "" {
		return pattern, nil
	}
	if pattern == "/" {
		return prefix, nil
	}
	return prefix + pattern, nil
}
