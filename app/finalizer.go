package app

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
)

// SignalContext cancels on interrupt or SIGTERM. The caller must call stop.
func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

type finalizer struct {
	id   string
	stop func(context.Context) error
}

// NewFinalizer adapts resource cleanup (e.g. an exporter) to Module. Add it before
// producers so reverse shutdown flushes it after HTTP and workers have drained.
// Register/Boot acquire nothing; the caller owns cleanup until this module boots.
func NewFinalizer(id string, stop func(context.Context) error) (Module, error) {
	if id == "" || stop == nil {
		return nil, errors.New("app: finalizer ID and callback required")
	}
	return &finalizer{id: id, stop: stop}, nil
}

func (f *finalizer) ID() string                               { return f.id }
func (f *finalizer) Register(*Application) error              { return nil }
func (f *finalizer) Boot(context.Context, *Application) error { return nil }
func (f *finalizer) Shutdown(ctx context.Context) error       { return f.stop(ctx) }
