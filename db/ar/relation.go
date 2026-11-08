package ar

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"

	frameworkdb "github.com/chitushka/framework/db"
)

// Relation links a parent column to a related model column.
type Relation struct {
	LocalColumn   string
	ForeignColumn string
}

func HasOne[P, R any](ctx context.Context, parent *P, related *Repository[R], relation Relation) (*R, error) {
	if parent == nil || related == nil {
		return nil, errors.New("ar: parent and related repository must not be nil")
	}
	metadata, err := metadataFor[P]()
	if err != nil {
		return nil, err
	}
	value, err := metadata.columnValue(reflect.ValueOf(parent).Elem(), relation.LocalColumn)
	if err != nil {
		return nil, err
	}
	return related.Find().Where(frameworkdb.Eq(relation.ForeignColumn, value)).One(ctx)
}

func HasMany[P, R any](ctx context.Context, parent *P, related *Repository[R], relation Relation) ([]*R, error) {
	if parent == nil || related == nil {
		return nil, errors.New("ar: parent and related repository must not be nil")
	}
	metadata, err := metadataFor[P]()
	if err != nil {
		return nil, err
	}
	value, err := metadata.columnValue(reflect.ValueOf(parent).Elem(), relation.LocalColumn)
	if err != nil {
		return nil, err
	}
	return related.Find().Where(frameworkdb.Eq(relation.ForeignColumn, value)).All(ctx)
}

// LoadHasOne eagerly loads one relation for every parent using one IN query.
// The result is aligned with parents; a missing related record produces nil.
func LoadHasOne[P, R any](ctx context.Context, parents []*P, related *Repository[R], relation Relation) ([]*R, error) {
	groups, err := loadRelated(ctx, parents, related, relation)
	if err != nil {
		return nil, err
	}
	result := make([]*R, len(groups))
	for i, group := range groups {
		if len(group) > 0 {
			result[i] = group[0]
		}
	}
	return result, nil
}

// LoadHasMany eagerly loads all related records using one IN query.
func LoadHasMany[P, R any](ctx context.Context, parents []*P, related *Repository[R], relation Relation) ([][]*R, error) {
	return loadRelated(ctx, parents, related, relation)
}

func loadRelated[P, R any](ctx context.Context, parents []*P, related *Repository[R], relation Relation) ([][]*R, error) {
	if related == nil {
		return nil, errors.New("ar: related repository must not be nil")
	}
	parentMetadata, err := metadataFor[P]()
	if err != nil {
		return nil, err
	}
	if _, found := related.metadata.byName[relation.ForeignColumn]; !found {
		return nil, fmt.Errorf("ar: related model has no column %q", relation.ForeignColumn)
	}
	values := make([]any, 0, len(parents))
	keys := make([]string, len(parents))
	seen := make(map[string]struct{}, len(parents))
	for i, parent := range parents {
		if parent == nil {
			continue
		}
		value, err := parentMetadata.columnValue(reflect.ValueOf(parent).Elem(), relation.LocalColumn)
		if err != nil {
			return nil, err
		}
		key := relationKey(value)
		keys[i] = key
		if _, found := seen[key]; !found {
			seen[key] = struct{}{}
			values = append(values, value)
		}
	}
	result := make([][]*R, len(parents))
	if len(values) == 0 {
		return result, nil
	}
	models, err := related.Find().Where(frameworkdb.In(relation.ForeignColumn, values...)).All(ctx)
	if err != nil {
		return nil, err
	}
	groups := make(map[string][]*R)
	for _, model := range models {
		value, _ := related.metadata.columnValue(reflect.ValueOf(model).Elem(), relation.ForeignColumn)
		key := relationKey(value)
		groups[key] = append(groups[key], model)
	}
	for i, key := range keys {
		if key != "" {
			result[i] = groups[key]
		}
	}
	return result, nil
}

func relationKey(value any) string {
	// Match the SQL representation rather than Go pointer addresses or int widths.
	if converted, err := driver.DefaultParameterConverter.ConvertValue(value); err == nil {
		value = converted
	}
	if bytes, ok := value.([]byte); ok {
		value = string(bytes)
	}
	return fmt.Sprintf("%T:%#v", value, value)
}
