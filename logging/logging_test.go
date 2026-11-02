package logging_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/chitushka/framework/logging"
)

func TestMultiHandlerFanoutAttrsAndLevels(t *testing.T) {
	var first, second bytes.Buffer
	debugLevel := new(slog.LevelVar)
	debugLevel.Set(slog.LevelDebug)
	multi, err := logging.NewMultiHandler(
		slog.NewJSONHandler(&first, &slog.HandlerOptions{Level: debugLevel}),
		slog.NewTextHandler(&second, &slog.HandlerOptions{Level: slog.LevelInfo}),
	)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(multi).With("service", "api").WithGroup("request")
	logger.Debug("hidden from second", "id", 1)
	logger.Info("served", "id", 2)
	if !strings.Contains(first.String(), `"service":"api"`) || !strings.Contains(first.String(), `"id":1`) || !strings.Contains(first.String(), `"id":2`) {
		t.Fatalf("first target missed records: %s", first.String())
	}
	if strings.Contains(second.String(), "hidden") || !strings.Contains(second.String(), "served") || !strings.Contains(second.String(), "service=api") {
		t.Fatalf("second target has wrong records: %s", second.String())
	}
	if !multi.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug should be enabled by one target")
	}
}

func TestFatalAndValidation(t *testing.T) {
	var output bytes.Buffer
	exitCode := 0
	logger, err := logging.New(slog.NewTextHandler(&output, nil), logging.WithExit(func(code int) { exitCode = code }))
	if err != nil {
		t.Fatal(err)
	}
	logger.Fatal("unrecoverable", "component", "worker")
	if exitCode != 1 || !strings.Contains(output.String(), "level=ERROR+4") || !strings.Contains(output.String(), "unrecoverable") {
		t.Fatalf("unexpected fatal result: code=%d log=%s", exitCode, output.String())
	}
	if _, err := logging.New(nil); err == nil {
		t.Fatal("expected nil handler error")
	}
	if _, err := logging.New(slog.NewTextHandler(&output, nil), nil); err == nil {
		t.Fatal("expected nil option error")
	}
	if _, err := logging.New(slog.NewTextHandler(&output, nil), logging.WithExit(nil)); err == nil {
		t.Fatal("expected nil exit error")
	}
	if _, err := logging.NewMultiHandler(slog.NewTextHandler(&output, nil), nil); err == nil {
		t.Fatal("expected nil target error")
	}
	var nilMulti *logging.MultiHandler
	if nilMulti.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("nil handler must be disabled")
	}
	if err := nilMulti.Handle(context.Background(), slog.Record{}); err == nil {
		t.Fatal("expected nil handler error")
	}
}

func TestWriterAndFileHandlers(t *testing.T) {
	var output bytes.Buffer
	handler, err := logging.NewWriterHandler(&output, logging.FormatJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	slog.New(handler).Info("json target")
	if !strings.Contains(output.String(), `"msg":"json target"`) {
		t.Fatalf("unexpected JSON target: %s", output.String())
	}
	if _, err := logging.NewWriterHandler(nil, logging.FormatJSON, nil); err == nil {
		t.Fatal("expected nil writer error")
	}
	if _, err := logging.NewWriterHandler(&output, logging.Format("xml"), nil); err == nil {
		t.Fatal("expected format error")
	}
	path := t.TempDir() + "/application.log"
	fileHandler, closer, err := logging.OpenFileHandler(path, logging.FormatText, nil)
	if err != nil {
		t.Fatal(err)
	}
	slog.New(fileHandler).Warn("file target")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(payload), "file target") {
		t.Fatalf("unexpected file log: %s, %v", payload, err)
	}
	if _, _, err := logging.OpenFileHandler("", logging.FormatText, nil); err == nil {
		t.Fatal("expected empty path error")
	}
}
