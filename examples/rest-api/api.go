package main

import (
	_ "embed"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/web"
)

//go:embed openapi.json
var openAPI []byte

func newHandler(verifier auth.TokenVerifier) (http.Handler, error) {
	return configuredHandler(verifier, defaultConfig())
}

func configuredHandler(verifier auth.TokenVerifier, config configuration) (http.Handler, error) {
	if verifier == nil {
		return nil, errors.New("rest-api: token verifier is required")
	}
	if config.ClientRate < 1 || config.SubjectRate < 1 || config.MaxArticles < 1 {
		return nil, errors.New("rest-api: resource limits must be positive")
	}
	store := newArticles(config.MaxArticles)
	router := web.NewRouter()
	if err := router.SetRequestLimits(web.RequestLimits{MaxBodyBytes: 4096}); err != nil {
		return nil, err
	}
	if err := router.Use(web.Recover); err != nil {
		return nil, err
	}
	if err := router.GET("/openapi.json", func(ctx *web.Context) error {
		ctx.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, err := ctx.Writer.Write(openAPI)
		return err
	}); err != nil {
		return nil, err
	}
	if err := router.Group("/v1", func(api *web.Group) error {
		if err := api.Use(auth.Bearer(verifier), newRateLimiter(config.SubjectRate).subjects, strictRequest); err != nil {
			return err
		}
		for _, route := range []struct {
			method, path string
			handler      web.Handler
		}{
			{"GET", "/articles", func(ctx *web.Context) error {
				opts, err := parseList(ctx)
				if err != nil {
					return err
				}
				return ctx.JSON(200, store.list(opts))
			}},
			{"POST", "/articles", store.create}, {"GET", "/articles/{id}", store.show},
			{"PUT", "/articles/{id}", store.update}, {"DELETE", "/articles/{id}", store.remove},
		} {
			if err := api.Handle(route.method, route.path, route.handler); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// Resolve the trusted origin before limiting clients. Production mode is always
	// enabled so errors/debug surfaces have identical behavior in both environments.
	handler, err := web.NewProxyHandler(newRateLimiter(config.ClientRate).clients(router), config.Proxy)
	if err != nil {
		return nil, err
	}
	handler, err = web.NewProductionHandler(handler)
	if err != nil {
		return nil, err
	}
	return errorContract(handler), nil
}

func strictRequest(next web.Handler) web.Handler {
	return func(ctx *web.Context) error {
		r := ctx.Request
		if len(r.URL.RawQuery) > 2048 || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("Content-Encoding") != "" {
			return web.NewHTTPError(400, "", nil)
		}
		isList := (r.Method == "GET" || r.Method == "HEAD") && ctx.RoutePattern() == "/v1/articles"
		if r.URL.RawQuery != "" && !isList {
			return web.NewHTTPError(400, "", nil)
		}
		return next(ctx)
	}
}

func listQuery(raw string) (listOptions, error) {
	opts := listOptions{limit: 20, sort: "id"}
	query, err := url.ParseQuery(raw)
	if err != nil {
		return opts, web.NewHTTPError(400, "", err)
	}
	for name, values := range query {
		if len(values) != 1 {
			return opts, web.NewHTTPError(400, "", nil)
		}
		value := values[0]
		switch name {
		case "limit", "offset":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || (name == "limit" && (n < 1 || n > 100)) || (name == "offset" && n > 1000000) {
				return opts, web.NewHTTPError(400, "", nil)
			}
			if name == "limit" {
				opts.limit = n
			} else {
				opts.offset = n
			}
		case "title":
			if len(value) > 200 {
				return opts, web.NewHTTPError(400, "", nil)
			}
			opts.title = value
		case "sort":
			if value != "id" && value != "-id" && value != "title" && value != "-title" {
				return opts, web.NewHTTPError(400, "", nil)
			}
			opts.sort = value
		default:
			return opts, web.NewHTTPError(400, "", nil)
		}
	}
	return opts, nil
}
