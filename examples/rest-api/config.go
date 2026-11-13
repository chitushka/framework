package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/web"
	"github.com/golang-jwt/jwt/v5"
)

type configuration struct {
	Address                              string
	Proxy                                web.ProxyConfig
	ClientRate, SubjectRate, MaxArticles int
}

func defaultConfig() configuration {
	return configuration{Address: "127.0.0.1:8082", ClientRate: 120, SubjectRate: 60, MaxArticles: 10000}
}

func loadConfig(getenv func(string) string) (configuration, *auth.JWTVerifier, error) {
	c := defaultConfig()
	if value := getenv("REST_ADDRESS"); value != "" {
		c.Address = value
	}
	_, port, err := net.SplitHostPort(c.Address)
	n, parseErr := strconv.Atoi(port)
	if err != nil || parseErr != nil || n < 1 || n > 65535 {
		return c, nil, errors.New("rest-api: REST_ADDRESS must be host:port")
	}
	for _, setting := range []struct {
		name   string
		target *int
		max    int
	}{
		{"REST_CLIENT_RATE", &c.ClientRate, 100000}, {"REST_SUBJECT_RATE", &c.SubjectRate, 100000}, {"REST_MAX_ARTICLES", &c.MaxArticles, 100000},
	} {
		if value := getenv(setting.name); value != "" {
			limit, err := strconv.Atoi(value)
			if err != nil || limit < 1 || limit > setting.max {
				return c, nil, fmt.Errorf("rest-api: invalid %s", setting.name)
			}
			*setting.target = limit
		}
	}
	for _, setting := range []struct {
		name   string
		target *[]string
	}{
		{"REST_ALLOWED_HOSTS", &c.Proxy.AllowedHosts}, {"REST_TRUSTED_PROXIES", &c.Proxy.TrustedProxies},
	} {
		if value := getenv(setting.name); value != "" {
			for _, item := range strings.Split(value, ",") {
				*setting.target = append(*setting.target, strings.TrimSpace(item))
			}
		}
	}
	if len(c.Proxy.TrustedProxies) > 0 {
		c.Proxy.ForwardedHeader = "X-Forwarded"
	}
	if len(c.Proxy.AllowedHosts) == 0 {
		c.Proxy.AllowedHosts = []string{"localhost:" + port, "127.0.0.1:" + port, "[::1]:" + port}
		if getenv("APP_ENV") == "production" {
			return c, nil, errors.New("rest-api: REST_ALLOWED_HOSTS is required in production")
		}
	}
	if env := getenv("APP_ENV"); env != "" && env != "development" && env != "production" {
		return c, nil, errors.New("rest-api: invalid APP_ENV")
	}
	if _, err := web.NewProxyPolicy(c.Proxy); err != nil {
		return c, nil, err
	}
	file, err := os.Open(getenv("JWT_PUBLIC_KEY_FILE"))
	if err != nil {
		return c, nil, fmt.Errorf("rest-api: public key: %w", err)
	}
	defer func() { _ = file.Close() }()
	pem, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(pem) > 16384 {
		return c, nil, errors.New("rest-api: public key file exceeds limit or cannot be read")
	}
	key, err := jwt.ParseEdPublicKeyFromPEM(pem)
	if err != nil {
		return c, nil, errors.New("rest-api: invalid Ed25519 public key PEM")
	}
	verifier, err := auth.NewJWTVerifier(auth.JWTConfig{Issuer: getenv("JWT_ISSUER"), Audience: getenv("JWT_AUDIENCE"), Algorithms: []string{"EdDSA"}}, []auth.JWTKey{{ID: getenv("JWT_KEY_ID"), Algorithm: "EdDSA", PublicKey: key}})
	return c, verifier, err
}
