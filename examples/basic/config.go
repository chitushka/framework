package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/chitushka/framework/web"
)

type configuration struct {
	Environment  string
	Address      string
	PublicDir    string
	Username     string
	PasswordHash string
	MaxSessions  int
	Proxy        web.ProxyConfig
}

func (c configuration) production() bool { return c.Environment == "production" }

func loadConfig(getenv func(string) string) (configuration, error) {
	c := configuration{Environment: getenv("APP_ENV"), Address: getenv("BASIC_ADDRESS"), PublicDir: getenv("BASIC_PUBLIC_DIR"), Username: getenv("BASIC_USERNAME")}
	if c.Environment == "" {
		c.Environment = "development"
	}
	if c.Environment != "development" && !c.production() {
		return c, errors.New("basic: APP_ENV must be development or production")
	}
	if c.Address == "" {
		c.Address = "127.0.0.1:8080"
	}
	_, port, addressErr := net.SplitHostPort(c.Address)
	portNumber, portErr := strconv.Atoi(port)
	if addressErr != nil || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return c, errors.New("basic: BASIC_ADDRESS must be host:port")
	}
	if c.PublicDir == "" {
		c.PublicDir = "public"
	}
	c.MaxSessions = 5000
	if raw := getenv("BASIC_MAX_SESSIONS"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100000 {
			return c, errors.New("basic: BASIC_MAX_SESSIONS must be 1–100000")
		}
		c.MaxSessions = limit
	}
	if c.Username == "" && !c.production() {
		c.Username = "admin"
	}
	if c.Username == "" || len(c.Username) > 256 || strings.TrimSpace(c.Username) != c.Username || strings.ContainsAny(c.Username, "\r\n\x00") {
		return c, errors.New("basic: BASIC_USERNAME must be a canonical login of 1–256 bytes")
	}
	c.Proxy.AllowedHosts = splitList(getenv("BASIC_ALLOWED_HOSTS"))
	c.Proxy.TrustedProxies = splitList(getenv("BASIC_TRUSTED_PROXIES"))
	if len(c.Proxy.TrustedProxies) > 0 {
		c.Proxy.ForwardedHeader = "X-Forwarded"
	}
	if len(c.Proxy.AllowedHosts) == 0 {
		if c.production() {
			return c, errors.New("basic: BASIC_ALLOWED_HOSTS is required in production")
		}
		c.Proxy.AllowedHosts = []string{"localhost:" + port, "127.0.0.1:" + port, "[::1]:" + port}
	}
	if _, err := web.NewProxyPolicy(c.Proxy); err != nil {
		return c, fmt.Errorf("basic: proxy configuration: %w", err)
	}
	hashFile := getenv("BASIC_PASSWORD_HASH_FILE")
	if hashFile == "" {
		if c.production() {
			return c, errors.New("basic: BASIC_PASSWORD_HASH_FILE is required in production")
		}
		return c, nil
	}
	f, err := os.Open(hashFile)
	if err != nil {
		return c, fmt.Errorf("basic: read password hash file: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 258))
	if err != nil {
		return c, fmt.Errorf("basic: read password hash: %w", err)
	}
	c.PasswordHash = strings.TrimSpace(string(data))
	clear(data)
	if len(c.PasswordHash) == 0 || len(c.PasswordHash) > 256 {
		return c, errors.New("basic: password hash file must contain one bounded Argon2id PHC string")
	}
	return c, nil
}

func splitList(value string) []string {
	var values []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}
