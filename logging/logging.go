// Package logging расширяет стандартный log/slog целями и fatal-уровнем.
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
)

// LevelFatal располагается выше стандартного slog.LevelError.
const LevelFatal = slog.Level(12)

// Format задаёт формат встроенного writer target.
type Format string

const (
	FormatJSON Format = "json"
	FormatText Format = "text"
)

// NewWriterHandler создаёт console/file/network target поверх io.Writer.
func NewWriterHandler(writer io.Writer, format Format, options *slog.HandlerOptions) (slog.Handler, error) {
	if writer == nil {
		return nil, errors.New("logging: writer не может быть nil")
	}
	switch format {
	case FormatJSON:
		return slog.NewJSONHandler(writer, options), nil
	case FormatText:
		return slog.NewTextHandler(writer, options), nil
	default:
		return nil, fmt.Errorf("logging: неизвестный формат %q", format)
	}
}

// OpenFileHandler открывает append-only file target. Закрытие файла принадлежит вызывающему коду.
func OpenFileHandler(path string, format Format, options *slog.HandlerOptions) (slog.Handler, io.Closer, error) {
	if path == "" {
		return nil, nil, errors.New("logging: путь файла не может быть пустым")
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, nil, fmt.Errorf("logging: открыть файл %q: %w", path, err)
	}
	handler, err := NewWriterHandler(file, format, options)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return handler, file, nil
}

// MultiHandler отправляет запись нескольким slog.Handler (console, file, DB или network).
type MultiHandler struct {
	handlers []slog.Handler
}

// NewMultiHandler создаёт fan-out handler.
func NewMultiHandler(handlers ...slog.Handler) (*MultiHandler, error) {
	for _, handler := range handlers {
		if handler == nil {
			return nil, errors.New("logging: slog.Handler не может быть nil")
		}
	}
	return &MultiHandler{handlers: slices.Clone(handlers)}, nil
}

func (handler *MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if handler == nil {
		return false
	}
	for _, target := range handler.handlers {
		if target.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (handler *MultiHandler) Handle(ctx context.Context, record slog.Record) error {
	if handler == nil {
		return errors.New("logging: MultiHandler не задан")
	}
	var result error
	for _, target := range handler.handlers {
		if target.Enabled(ctx, record.Level) {
			result = errors.Join(result, target.Handle(ctx, record.Clone()))
		}
	}
	return result
}

func (handler *MultiHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	if handler == nil {
		return handler
	}
	result := make([]slog.Handler, len(handler.handlers))
	for index, target := range handler.handlers {
		result[index] = target.WithAttrs(attributes)
	}
	return &MultiHandler{handlers: result}
}

func (handler *MultiHandler) WithGroup(name string) slog.Handler {
	if handler == nil {
		return handler
	}
	result := make([]slog.Handler, len(handler.handlers))
	for index, target := range handler.handlers {
		result[index] = target.WithGroup(name)
	}
	return &MultiHandler{handlers: result}
}

// Logger сохраняет API slog и добавляет Fatal/FatalContext.
type Logger struct {
	*slog.Logger
	exit func(int)
}

// Option настраивает Logger.
type Option func(*Logger) error

// WithExit заменяет функцию завершения; предназначено для embedding и тестов.
func WithExit(exit func(int)) Option {
	return func(logger *Logger) error {
		if exit == nil {
			return errors.New("logging: exit function не может быть nil")
		}
		logger.exit = exit
		return nil
	}
}

// New создаёт Logger поверх стандартного slog.Handler.
func New(handler slog.Handler, options ...Option) (*Logger, error) {
	if handler == nil {
		return nil, errors.New("logging: slog.Handler не может быть nil")
	}
	logger := &Logger{Logger: slog.New(handler), exit: os.Exit}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("logging: option не может быть nil")
		}
		if err := option(logger); err != nil {
			return nil, err
		}
	}
	return logger, nil
}

// Fatal записывает сообщение на fatal-уровне и завершает процесс с кодом 1.
func (logger *Logger) Fatal(message string, args ...any) {
	logger.FatalContext(context.Background(), message, args...)
}

// FatalContext записывает сообщение на fatal-уровне и завершает процесс с кодом 1.
func (logger *Logger) FatalContext(ctx context.Context, message string, args ...any) {
	if logger == nil || logger.Logger == nil {
		return
	}
	logger.Log(ctx, LevelFatal, message, args...)
	logger.exit(1)
}
