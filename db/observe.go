package db

import (
	"context"
	"time"
)

// QueryEvent описывает завершившуюся операцию database/sql.
type QueryEvent struct {
	Operation string
	Query     string
	Args      []any
	Duration  time.Duration
	Err       error
}

// QueryObserver получает диагностические события SQL.
type QueryObserver interface {
	ObserveQuery(context.Context, QueryEvent)
}

type queryObserverContextKey struct{}

// WithQueryObserver возвращает context с observer для последующих DB-вызовов.
func WithQueryObserver(ctx context.Context, observer QueryObserver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, queryObserverContextKey{}, observer)
}

func observeQuery(ctx context.Context, operation, query string, args []any, started time.Time, err error) {
	if ctx == nil {
		return
	}
	observer, ok := ctx.Value(queryObserverContextKey{}).(QueryObserver)
	if !ok || observer == nil {
		return
	}
	observer.ObserveQuery(ctx, QueryEvent{
		Operation: operation,
		Query:     query,
		Args:      append([]any(nil), args...),
		Duration:  time.Since(started),
		Err:       err,
	})
}
