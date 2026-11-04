package logging_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/chitushka/framework/logging"
)

func TestProductionLogsRedactSensitiveGroupsPreboundFieldsAndObjects(t *testing.T) {
	const secret = "private-password-value"
	var output bytes.Buffer
	handler, err := logging.NewProductionHandler(&output)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(handler).With("password", secret).WithGroup("request")
	logger.Debug("hidden debug", "debug-value", secret)
	logger.Info("login result", "count", 3, "Authorization", secret, "database_dsn", secret,
		"database_url", secret, "db.statement", secret, "query_params", secret,
		"opaque", struct{ Value string }{secret}, "object", map[string]string{"nested": secret},
		"error", errors.New(secret), slog.Group("credentials", slog.String("user", secret)),
		slog.Group("headers", slog.String("Set-Cookie", secret)))
	slog.New(handler).WithGroup("access_tokens").With("value", secret).Info("bound group")
	if strings.Contains(output.String(), secret) || strings.Contains(output.String(), "hidden debug") || !strings.Contains(output.String(), `"count":3`) || !strings.Contains(output.String(), "[REDACTED]") {
		t.Fatalf("unsafe logs: %s", output.String())
	}
	if _, err := logging.NewProductionHandler(nil); err == nil {
		t.Fatal("nil log writer accepted")
	}
}
