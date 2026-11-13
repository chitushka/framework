package ar

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	frameworkdb "github.com/chitushka/framework/db"
)

// Finder is an immutable fluent query for a Repository.
type Finder[T any] struct {
	repository *Repository[T]
	builder    *frameworkdb.QueryBuilder
	err        error
}

func (finder *Finder[T]) clone() *Finder[T] {
	if finder == nil {
		return &Finder[T]{err: errors.New("ar: finder must not be nil")}
	}
	copy := *finder
	return &copy
}

func (finder *Finder[T]) Where(condition frameworkdb.Condition) *Finder[T] {
	result := finder.clone()
	if result.builder != nil {
		result.builder = result.builder.Where(condition)
	}
	return result
}

func (finder *Finder[T]) AndWhere(condition frameworkdb.Condition) *Finder[T] {
	result := finder.clone()
	if result.builder != nil {
		result.builder = result.builder.AndWhere(condition)
	}
	return result
}

func (finder *Finder[T]) OrderBy(column string, direction frameworkdb.SortDirection) *Finder[T] {
	result := finder.clone()
	if result.builder != nil {
		result.builder = result.builder.OrderBy(column, direction)
	}
	return result
}

func (finder *Finder[T]) Limit(limit int) *Finder[T] {
	result := finder.clone()
	if result.builder != nil {
		result.builder = result.builder.Limit(limit)
	}
	return result
}

func (finder *Finder[T]) Offset(offset int) *Finder[T] {
	result := finder.clone()
	if result.builder != nil {
		result.builder = result.builder.Offset(offset)
	}
	return result
}

func (finder *Finder[T]) All(ctx context.Context) ([]*T, error) {
	if finder == nil || finder.err != nil {
		if finder == nil {
			return nil, errors.New("ar: finder must not be nil")
		}
		return nil, finder.err
	}
	rows, err := finder.builder.QueryContext(ctx, finder.repository.executor)
	if err != nil {
		return nil, fmt.Errorf("ar: find %s: %w", finder.repository.table, err)
	}
	defer func() { _ = rows.Close() }()
	models := make([]*T, 0)
	for rows.Next() {
		model := new(T)
		value := reflect.ValueOf(model).Elem()
		destinations := make([]any, len(finder.repository.metadata.fields))
		for i := range finder.repository.metadata.fields {
			destinations[i] = finder.repository.metadata.fieldValue(value, i).Addr().Interface()
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("ar: scan %s: %w", finder.repository.table, err)
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ar: iterate %s: %w", finder.repository.table, err)
	}
	return models, nil
}

func (finder *Finder[T]) One(ctx context.Context) (*T, error) {
	models, err := finder.Limit(1).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, ErrNotFound
	}
	return models[0], nil
}
