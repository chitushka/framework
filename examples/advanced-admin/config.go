package main

import (
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/web"
)

type configuration struct {
	Environment, Address, PublicDir string
	Database                        db.Config
	Proxy                           web.ProxyConfig
	AdminHashFile, EditorHashFile   string
}

func (c configuration) production() bool { return c.Environment == "production" }
func loadConfig(getenv func(string) string) (configuration, error) {
	c := configuration{Environment: getenv("APP_ENV"), Address: getenv("ADMIN_ADDRESS"), PublicDir: getenv("ADMIN_PUBLIC_DIR"), AdminHashFile: getenv("ADMIN_SEED_ADMIN_HASH_FILE"), EditorHashFile: getenv("ADMIN_SEED_EDITOR_HASH_FILE")}
	if c.Environment == "" {
		c.Environment = "development"
	}
	if c.Environment != "development" && !c.production() {
		return c, errors.New("admin: APP_ENV must be development or production")
	}
	if c.Address == "" {
		c.Address = "127.0.0.1:8081"
	}
	_, port, err := net.SplitHostPort(c.Address)
	number, numberErr := strconv.Atoi(port)
	if err != nil || numberErr != nil || number < 1 || number > 65535 {
		return c, errors.New("admin: ADMIN_ADDRESS must be host:numeric-port")
	}
	if c.PublicDir == "" {
		c.PublicDir = "public"
	}
	c.Database = db.Config{Driver: getenv("ADMIN_DB_DRIVER"), DSN: getenv("ADMIN_DB_DSN"), MaxOpenConns: 8, MaxIdleConns: 4, ConnMaxLifetime: 30 * time.Minute, ConnMaxIdleTime: 5 * time.Minute, PingTimeout: 5 * time.Second}
	if c.Database.Driver == "" {
		c.Database.Driver = "pgx"
	}
	switch c.Database.Driver {
	case "pgx":
		c.Database.Dialect = db.PostgreSQL
	case "mysql":
		c.Database.Dialect = db.MySQL
	default:
		return c, errors.New("admin: ADMIN_DB_DRIVER must be pgx or mysql")
	}
	if c.Database.DSN == "" {
		return c, errors.New("admin: ADMIN_DB_DSN is required")
	}
	c.Proxy.AllowedHosts = splitList(getenv("ADMIN_ALLOWED_HOSTS"))
	c.Proxy.TrustedProxies = splitList(getenv("ADMIN_TRUSTED_PROXIES"))
	if len(c.Proxy.TrustedProxies) > 0 {
		c.Proxy.ForwardedHeader = "X-Forwarded"
	}
	if len(c.Proxy.AllowedHosts) == 0 {
		if c.production() {
			return c, errors.New("admin: ADMIN_ALLOWED_HOSTS is required in production")
		}
		c.Proxy.AllowedHosts = []string{"localhost:" + port, "127.0.0.1:" + port, "[::1]:" + port}
	}
	if _, err := web.NewProxyPolicy(c.Proxy); err != nil {
		return c, errors.New("admin: invalid proxy/host policy")
	}
	return c, nil
}
func splitList(input string) []string {
	var list []string
	for _, v := range strings.Split(input, ",") {
		if v = strings.TrimSpace(v); v != "" {
			list = append(list, v)
		}
	}
	return list
}
func readHashFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("admin: cannot open seed hash file")
	}
	defer func() { _ = f.Close() }()
	value, err := io.ReadAll(io.LimitReader(f, 258))
	if err != nil {
		return "", err
	}
	if len(value) >= 258 {
		return "", errors.New("admin: seed hash file too large")
	}
	return strings.TrimSpace(string(value)), nil
}
