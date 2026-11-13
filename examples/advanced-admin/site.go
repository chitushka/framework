package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"math"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chitushka/framework/asset"
	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/internal/starter"
	"github.com/chitushka/framework/security"
	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

//go:embed templates/*.html
var templates embed.FS

type site struct {
	http.Handler
	store     *store
	root      *os.Root
	views     *web.Renderer
	passwords *auth.PasswordAuthenticator
}

func (s *site) Close() error { return s.root.Close() }
func newSite(store *store, c configuration) (_ *site, err error) {
	root, err := os.OpenRoot(c.PublicDir)
	if err != nil {
		return nil, errors.New("admin: public directory missing; run node scripts/admin.mjs build")
	}
	defer func() {
		if err != nil {
			_ = root.Close()
		}
	}()
	manifest, err := asset.LoadViteManifest(root.FS(), ".vite/manifest.json")
	if err != nil {
		return nil, errors.New("admin: frontend build required")
	}
	bundle, err := manifest.Bundle("src/admin.js", "/")
	if err != nil {
		return nil, err
	}
	if len(bundle.Styles) == 0 || len(bundle.Scripts) == 0 {
		return nil, errors.New("admin: JavaScript/CSS missing in manifest")
	}
	for _, resources := range [][]asset.Resource{bundle.Styles, bundle.Scripts, bundle.ModulePreloads} {
		for _, r := range resources {
			name := strings.TrimPrefix(r.URL, "/")
			if !fs.ValidPath(name) || !strings.HasPrefix(name, "assets/") {
				return nil, errors.New("admin: invalid local manifest resource")
			}
			info, err := fs.Stat(root.FS(), name)
			if err != nil || !info.Mode().IsRegular() {
				return nil, errors.New("admin: manifest asset missing")
			}
		}
	}
	registry := asset.NewRegistry()
	if err = registry.Register("admin", bundle); err != nil {
		return nil, err
	}
	views, err := fs.Sub(templates, "templates")
	if err != nil {
		return nil, err
	}
	functions := registry.TemplateFuncs("admin")
	functions["has"] = func(values []string, value string) bool {
		for _, v := range values {
			if v == value {
				return true
			}
		}
		return false
	}
	renderer, err := web.NewRenderer(views, web.WithDefaultLayout("layout"), web.WithTemplateFuncs(functions))
	if err != nil {
		return nil, err
	}
	limiter, err := auth.NewMemoryAttemptLimiter(auth.AttemptLimitConfig{AccountLimit: 10, ClientLimit: 60})
	if err != nil {
		return nil, err
	}
	passwords, err := auth.NewPasswordAuthenticator(store, store.hasher, limiter)
	if err != nil {
		return nil, err
	}
	name := "admin_session"
	if c.production() {
		name = "__Host-admin_session"
	}
	sessions, err := session.New(starter.NewSessionBackend(5000), session.Config{Name: name, Secure: c.production(), TTL: 30 * time.Minute, AbsoluteTTL: 8 * time.Hour})
	if err != nil {
		return nil, err
	}
	s := &site{store: store, root: root, views: renderer, passwords: passwords}
	router := web.NewRouter()
	if err = router.SetRequestLimits(web.RequestLimits{MaxBodyBytes: 16 << 10, MaxFormBytes: 16 << 10, MaxFormValues: 16}); err != nil {
		return nil, err
	}
	if err = router.Use(web.Recover, sessions.Middleware, security.CSRF(security.CSRFConfig{}), auth.Middleware(store)); err != nil {
		return nil, err
	}
	if err = s.routes(router); err != nil {
		return nil, err
	}
	static, err := fs.Sub(root.FS(), "assets")
	if err != nil {
		return nil, err
	}
	files := http.StripPrefix("/assets/", http.FileServerFS(static))
	mux := http.NewServeMux()
	mux.Handle("/", router)
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(405)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/assets/")
		if !fs.ValidPath(path) || path == "." || strings.ContainsAny(path, "\\:\x00") {
			http.NotFound(w, r)
			return
		}
		for _, part := range strings.Split(path, "/") {
			if strings.HasPrefix(part, ".") {
				http.NotFound(w, r)
				return
			}
		}
		info, err := fs.Stat(static, path)
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if c.production() {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		mux.ServeHTTP(w, r)
	})
	handler, err = web.NewProxyHandler(handler, c.Proxy)
	if err != nil {
		return nil, err
	}
	if c.production() {
		handler, err = web.NewProductionHandler(handler)
		if err != nil {
			return nil, err
		}
	}
	s.Handler = handler
	return s, nil
}
func token(c *web.Context) string { return auth.Must(c.Request.Context()).ID() }
func (s *site) render(c *web.Context, status int, view string, p pageData) error {
	p.CSRF = c.Writer.Header().Get("X-CSRF-Token")
	if p.Username == "" {
		if user, ok := auth.Must(c.Request.Context()).Identity().(principal); ok {
			p.Username = user.Username
		}
	}
	return c.Render(status, s.views, view, p)
}
func (s *site) problem(c *web.Context, err error, p pageData, view string) error {
	var public *web.HTTPError
	if errors.As(err, &public) && public.Status >= 400 && public.Status < 500 {
		p.Message = public.Message
		return s.render(c, public.Status, view, p)
	}
	return err
}
func (s *site) protect(permission string, next web.Handler) web.Handler {
	return func(c *web.Context) error {
		if auth.Must(c.Request.Context()).IsGuest() {
			return c.Redirect(303, "/login")
		}
		err := s.store.transact(c.Request.Context(), token(c), permission, func(*transaction) error { return nil })
		if err != nil {
			return s.problem(c, err, pageData{Title: "Доступ ограничен"}, "error")
		}
		return next(c)
	}
}
func form(c *web.Context, multiple string, fields ...string) error {
	media, _, err := mime.ParseMediaType(c.Request.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" {
		return web.NewHTTPError(415, "Используйте HTML-форму", err)
	}
	if err = c.Request.ParseForm(); err != nil {
		return web.NewHTTPError(400, "Некорректная форма", err)
	}
	allowed := map[string]bool{"_csrf": true}
	for _, f := range fields {
		allowed[f] = true
	}
	for name, values := range c.Request.PostForm {
		if !allowed[name] || (name != multiple && len(values) != 1) {
			return web.NewHTTPError(400, "Неизвестные или повторяющиеся поля", nil)
		}
	}
	return nil
}
func version(c *web.Context) (int64, error) {
	v, err := strconv.ParseInt(c.Request.PostForm.Get("version"), 10, 64)
	if err != nil || v < 1 {
		return 0, web.NewHTTPError(400, "Некорректная версия", err)
	}
	return v, nil
}
func pageNumber(c *web.Context) (int, error) {
	raw := c.Request.URL.Query().Get("page")
	if raw == "" {
		return 1, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 || v > 4000 {
		return 0, web.NewHTTPError(400, "Некорректная страница", err)
	}
	return v, nil
}
func (s *site) loadPage(c *web.Context, permission, title, section string, fn func(*transaction, *pageData) error) (pageData, error) {
	page, err := pageNumber(c)
	if err != nil {
		return pageData{}, err
	}
	var result pageData
	err = s.store.transact(c.Request.Context(), token(c), permission, func(t *transaction) error {
		var err error
		result, err = t.page(c.Request.Context(), title, section, page)
		if err != nil {
			return err
		}
		return fn(t, &result)
	})
	return result, domainError(err)
}
func (s *site) routes(r *web.Router) error {
	if err := r.GET("/", func(c *web.Context) error {
		if auth.Must(c.Request.Context()).IsGuest() {
			return c.Redirect(303, "/login")
		}
		return c.Redirect(303, "/admin")
	}); err != nil {
		return err
	}
	if err := r.GET("/login", func(c *web.Context) error {
		if !auth.Must(c.Request.Context()).IsGuest() {
			return c.Redirect(303, "/admin")
		}
		return s.render(c, 200, "login", pageData{Title: "Вход"})
	}); err != nil {
		return err
	}
	if err := r.POST("/login", s.login); err != nil {
		return err
	}
	if err := r.POST("/logout", func(c *web.Context) error {
		if err := form(c, ""); err != nil {
			return err
		}
		if err := auth.Must(c.Request.Context()).Logout(); err != nil {
			return err
		}
		c.Writer.Header().Del("X-CSRF-Token")
		return c.Redirect(303, "/login")
	}); err != nil {
		return err
	}
	if err := r.GET("/admin", s.protect("authenticated", func(c *web.Context) error {
		p, err := s.loadPage(c, "authenticated", "Обзор", "home", func(*transaction, *pageData) error { return nil })
		if err != nil {
			return err
		}
		return s.render(c, 200, "home", p)
	})); err != nil {
		return err
	}
	if err := s.catalogRoutes(r); err != nil {
		return err
	}
	if err := s.userRoutes(r); err != nil {
		return err
	}
	if err := s.roleRoutes(r); err != nil {
		return err
	}
	return nil
}
func (s *site) login(c *web.Context) error {
	if err := form(c, "", "username", "password"); err != nil {
		return err
	}
	if !auth.Must(c.Request.Context()).IsGuest() {
		return c.Redirect(303, "/admin")
	}
	login := canonicalLogin(c.Request.PostForm.Get("username"))
	password := c.Request.PostForm.Get("password")
	p := pageData{Title: "Вход", User: user{Username: login}}
	if login == "" || password == "" {
		p.Message = "Введите имя пользователя и пароль"
		return s.render(c, 422, "login", p)
	}
	id, err := s.passwords.Authenticate(c.Request.Context(), login, password, c.ClientIP())
	if errors.Is(err, auth.ErrInvalidCredentials) {
		p.Message = "Неверное имя пользователя или пароль"
		return s.render(c, 401, "login", p)
	}
	var throttle *auth.ThrottleError
	if errors.As(err, &throttle) {
		c.Writer.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(throttle.RetryAfter.Seconds()))))
		p.Message = "Слишком много попыток входа"
		return s.render(c, 429, "login", p)
	}
	if err != nil {
		return err
	}
	if err = auth.Must(c.Request.Context()).Login(id); err != nil {
		return err
	}
	c.Writer.Header().Del("X-CSRF-Token")
	return c.Redirect(303, "/admin")
}
