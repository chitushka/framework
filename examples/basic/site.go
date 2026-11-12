package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
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
	"github.com/chitushka/framework/security"
	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/validation"
	"github.com/chitushka/framework/web"
)

//go:embed templates/*.html
var templates embed.FS

type site struct {
	http.Handler
	root *os.Root
}

func (s *site) Close() error { return s.root.Close() }

type pageData struct {
	Title, CSRF, Username, Message string
	Errors                         validation.Errors
	Demo                           bool
}
type credentials struct {
	Username string `form:"username"`
	Password string `form:"password"`
}

var loginRules = validation.Set{Rules: []validation.Rule{
	validation.Field("username", validation.Required{Message: "Введите имя пользователя."}),
	validation.Field("username", validation.String{Max: 256, Message: "Имя пользователя слишком длинное."}),
	validation.Field("password", validation.Required{Message: "Введите пароль."}),
	validation.Field("password", validation.String{Max: 1024, Message: "Пароль слишком длинный."}),
}}

func newSite(ctx context.Context, config configuration) (_ *site, err error) {
	root, err := os.OpenRoot(config.PublicDir)
	if err != nil {
		return nil, fmt.Errorf("basic: open public directory (run node scripts/basic.mjs build): %w", err)
	}
	defer func() {
		if err != nil {
			_ = root.Close()
		}
	}()
	manifest, err := asset.LoadViteManifest(root.FS(), ".vite/manifest.json")
	if err != nil {
		return nil, fmt.Errorf("basic: frontend build required (node scripts/basic.mjs build): %w", err)
	}
	bundle, err := manifest.Bundle("src/basic.js", "/")
	if err != nil {
		return nil, err
	}
	if len(bundle.Styles) == 0 || len(bundle.Scripts) == 0 {
		return nil, errors.New("basic: manifest must provide Basic JavaScript and CSS")
	}
	for _, resources := range [][]asset.Resource{bundle.Styles, bundle.Scripts, bundle.ModulePreloads} {
		for _, resource := range resources {
			name := strings.TrimPrefix(resource.URL, "/")
			if !fs.ValidPath(name) || !strings.HasPrefix(name, "assets/") {
				return nil, errors.New("basic: manifest resources must be local files in assets/")
			}
			info, statErr := fs.Stat(root.FS(), name)
			if statErr != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("basic: manifest asset %q is missing or not a regular file", name)
			}
		}
	}
	assets := asset.NewRegistry()
	if err = assets.Register("basic", bundle); err != nil {
		return nil, err
	}
	views, err := fs.Sub(templates, "templates")
	if err != nil {
		return nil, err
	}
	renderer, err := web.NewRenderer(views, web.WithDefaultLayout("layout"), web.WithTemplateFuncs(assets.TemplateFuncs("basic")))
	if err != nil {
		return nil, err
	}
	hasher, err := auth.NewPasswordHasher(auth.PasswordConfig{})
	if err != nil {
		return nil, err
	}
	encoded := config.PasswordHash
	if encoded == "" {
		if config.production() {
			return nil, errors.New("basic: production requires an explicit password hash")
		}
		encoded, err = hasher.Hash(ctx, "changeme")
		if err != nil {
			return nil, err
		}
	} else {
		_, verifyErr := hasher.Verify(ctx, "configuration-validation", encoded)
		if verifyErr != nil && !errors.Is(verifyErr, auth.ErrInvalidCredentials) {
			return nil, errors.New("basic: invalid Argon2id password hash")
		}
	}
	store := &accountStore{username: config.Username, hash: encoded}
	limiter, err := auth.NewMemoryAttemptLimiter(auth.AttemptLimitConfig{})
	if err != nil {
		return nil, err
	}
	passwords, err := auth.NewPasswordAuthenticator(store, hasher, limiter)
	if err != nil {
		return nil, err
	}
	cookieName := "basic_session"
	if config.production() {
		cookieName = "__Host-basic_session"
	}
	sessions, err := session.New(newBoundedSessions(config.MaxSessions), session.Config{Name: cookieName, Secure: config.production(), TTL: 30 * time.Minute, AbsoluteTTL: 8 * time.Hour})
	if err != nil {
		return nil, err
	}
	router := web.NewRouter()
	if err = router.SetRequestLimits(web.RequestLimits{MaxBodyBytes: 8 << 10, MaxFormBytes: 8 << 10, MaxFormValues: 8}); err != nil {
		return nil, err
	}
	if err = router.Use(web.Recover, sessions.Middleware, security.CSRF(security.CSRFConfig{}), auth.Middleware(store)); err != nil {
		return nil, err
	}
	render := func(c *web.Context, status int, name string, data pageData) error {
		data.CSRF = c.Writer.Header().Get("X-CSRF-Token")
		data.Demo = !config.production() && config.Username == "admin" && config.PasswordHash == ""
		return c.Render(status, renderer, name, data)
	}
	if err = router.GET("/", func(c *web.Context) error {
		if auth.Must(c.Request.Context()).IsGuest() {
			return c.Redirect(303, "/login")
		}
		return c.Redirect(303, "/dashboard")
	}); err != nil {
		return nil, err
	}
	if err = router.GET("/login", func(c *web.Context) error {
		if !auth.Must(c.Request.Context()).IsGuest() {
			return c.Redirect(303, "/dashboard")
		}
		return render(c, 200, "login", pageData{Title: "Вход"})
	}); err != nil {
		return nil, err
	}
	if err = router.POST("/login", func(c *web.Context) error {
		if err := requireForm(c, "username", "password"); err != nil {
			return err
		}
		if !auth.Must(c.Request.Context()).IsGuest() {
			return c.Redirect(303, "/dashboard")
		}
		input := credentials{Username: strings.TrimSpace(c.Request.PostForm.Get("username")), Password: c.Request.PostForm.Get("password")}
		data := pageData{Title: "Вход", Username: input.Username}
		problems, err := loginRules.Validate(c.Request.Context(), &input, "")
		if err != nil {
			return err
		}
		if !problems.Valid() {
			data.Errors = problems
			return render(c, 422, "login", data)
		}
		id, err := passwords.Authenticate(c.Request.Context(), input.Username, input.Password, c.ClientIP())
		if errors.Is(err, auth.ErrInvalidCredentials) {
			data.Message = "Неверное имя пользователя или пароль."
			return render(c, 401, "login", data)
		}
		var throttled *auth.ThrottleError
		if errors.As(err, &throttled) {
			c.Writer.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(throttled.RetryAfter.Seconds()))))
			data.Message = "Слишком много попыток. Попробуйте через минуту."
			return render(c, 429, "login", data)
		}
		if err != nil {
			return err
		}
		if err = auth.Must(c.Request.Context()).Login(id); err != nil {
			return err
		}
		c.Writer.Header().Del("X-CSRF-Token")
		return c.Redirect(303, "/dashboard")
	}); err != nil {
		return nil, err
	}
	if err = router.GET("/dashboard", func(c *web.Context) error {
		user := auth.Must(c.Request.Context())
		if user.IsGuest() {
			return c.Redirect(303, "/login")
		}
		return render(c, 200, "dashboard", pageData{Title: "Панель управления", Username: user.ID()})
	}); err != nil {
		return nil, err
	}
	if err = router.POST("/logout", func(c *web.Context) error {
		if err := requireForm(c); err != nil {
			return err
		}
		if err := auth.Must(c.Request.Context()).Logout(); err != nil {
			return err
		}
		c.Writer.Header().Del("X-CSRF-Token")
		return c.Redirect(303, "/login")
	}); err != nil {
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
		name := strings.TrimPrefix(r.URL.Path, "/assets/")
		if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:\x00") {
			http.NotFound(w, r)
			return
		}
		for _, part := range strings.Split(name, "/") {
			if strings.HasPrefix(part, ".") {
				http.NotFound(w, r)
				return
			}
		}
		info, err := fs.Stat(static, name)
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if config.production() {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		mux.ServeHTTP(w, r)
	})
	handler, err = web.NewProxyHandler(handler, config.Proxy)
	if err != nil {
		return nil, err
	}
	if config.production() {
		handler, err = web.NewProductionHandler(handler)
		if err != nil {
			return nil, err
		}
	}
	return &site{Handler: handler, root: root}, nil
}

func requireForm(c *web.Context, fields ...string) error {
	media, _, err := mime.ParseMediaType(c.Request.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" {
		return web.NewHTTPError(415, "Use an HTML form", err)
	}
	if err = c.Request.ParseForm(); err != nil {
		return web.NewHTTPError(400, "Invalid form", err)
	}
	allowed := map[string]bool{"_csrf": true}
	for _, name := range fields {
		allowed[name] = true
	}
	for name, values := range c.Request.PostForm {
		if !allowed[name] || len(values) != 1 {
			return web.NewHTTPError(400, "Invalid form fields", nil)
		}
	}
	return nil
}
