// Package behavior реализует расширение компонентов через явные Go-декораторы.
package behavior

import (
	"errors"
	"fmt"
)

// Decorator оборачивает компонент того же интерфейсного типа.
type Decorator[T any] func(T) (T, error)

// Apply последовательно применяет декораторы в порядке объявления.
func Apply[T any](base T, decorators ...Decorator[T]) (T, error) {
	current := base
	for index, decorator := range decorators {
		if decorator == nil {
			var zero T
			return zero, fmt.Errorf("behavior: декоратор %d: %w", index, errors.New("декоратор не может быть nil"))
		}
		decorated, err := decorator(current)
		if err != nil {
			var zero T
			return zero, fmt.Errorf("behavior: декоратор %d: %w", index, err)
		}
		current = decorated
	}
	return current, nil
}
