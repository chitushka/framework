package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func publicKeyFile(t *testing.T, key ed25519.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "public.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigurationFailsClosed(t *testing.T) {
	_, key := exampleTokens(t)
	valid := map[string]string{"JWT_PUBLIC_KEY_FILE": publicKeyFile(t, key.Public().(ed25519.PublicKey)), "JWT_KEY_ID": "test", "JWT_ISSUER": "https://issuer.example", "JWT_AUDIENCE": "articles"}
	getenv := func(key string) string { return valid[key] }
	c, v, err := loadConfig(getenv)
	if err != nil || v == nil || c.Address != "127.0.0.1:8082" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	for _, tc := range []struct{ name, value string }{
		{"JWT_PUBLIC_KEY_FILE", ""}, {"JWT_KEY_ID", ""}, {"JWT_ISSUER", ""}, {"JWT_AUDIENCE", ""},
		{"REST_ADDRESS", ":0"}, {"REST_ADDRESS", "bad"}, {"REST_CLIENT_RATE", "0"}, {"REST_SUBJECT_RATE", "-1"}, {"REST_MAX_ARTICLES", "100001"},
		{"REST_ALLOWED_HOSTS", "bad/host"}, {"REST_ALLOWED_HOSTS", "api.example,"}, {"REST_TRUSTED_PROXIES", "bad"}, {"APP_ENV", "staging"}, {"APP_ENV", "production"},
	} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			_, _, err := loadConfig(func(name string) string {
				if name == tc.name {
					return tc.value
				}
				return valid[name]
			})
			if err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	valid["APP_ENV"] = "production"
	valid["REST_ALLOWED_HOSTS"] = "api.example"
	valid["REST_TRUSTED_PROXIES"] = "127.0.0.1/32"
	c, _, err = loadConfig(getenv)
	if err != nil || c.Proxy.ForwardedHeader != "X-Forwarded" {
		t.Fatal(err)
	}
	if err := os.WriteFile(valid["JWT_PUBLIC_KEY_FILE"], make([]byte, 16385), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadConfig(getenv); err == nil {
		t.Fatal("oversized key accepted")
	}
}
