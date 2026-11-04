package logging

import (
	"errors"
	"io"
	"log/slog"
	"strings"
)

// NewProductionHandler creates an Info-level JSON handler that redacts sensitive
// attribute keys/groups and opaque objects (including error values). Messages
// and otherwise innocently named strings must not contain credentials.
func NewProductionHandler(writer io.Writer) (slog.Handler, error) {
	if writer == nil {
		return nil, errors.New("logging: production writer is required")
	}
	return slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: slog.LevelInfo, ReplaceAttr: func(groups []string, attribute slog.Attr) slog.Attr {
		secret := sensitiveKey(attribute.Key)
		for _, group := range groups {
			secret = secret || sensitiveKey(group)
		}
		if !secret && attribute.Key == slog.LevelKey && attribute.Value.Kind() == slog.KindAny {
			if level, ok := attribute.Value.Any().(slog.Level); ok {
				return slog.String(attribute.Key, level.String())
			}
		}
		if secret || attribute.Value.Kind() == slog.KindAny {
			return slog.String(attribute.Key, "[REDACTED]")
		}
		return attribute
	}}), nil
}

func sensitiveKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, key)
	for _, word := range []string{"password", "secret", "token", "credential", "authorization", "cookie", "apikey", "privatekey", "signingkey", "sessionid", "connectionstring", "connectionurl", "accesskey", "dsn", "error", "exception", "stacktrace", "statement", "query", "sql", "args"} {
		if strings.Contains(normalized, word) {
			return true
		}
	}
	if strings.HasSuffix(normalized, "url") || strings.HasSuffix(normalized, "uri") {
		return true
	}
	switch normalized {
	case "dsn", "key", "err", "error", "sql", "query", "statement", "args":
		return true
	}
	return false
}
