package behavior_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/chitushka/framework/behavior"
)

type greeter interface{ Greet() string }
type baseGreeter struct{}

func (baseGreeter) Greet() string { return "hello" }

type prefixGreeter struct {
	prefix string
	next   greeter
}

func (g prefixGreeter) Greet() string { return g.prefix + g.next.Greet() }

func TestApplyDecoratesInDeclarationOrder(t *testing.T) {
	decorate := func(prefix string) behavior.Decorator[greeter] {
		return func(next greeter) (greeter, error) {
			return prefixGreeter{prefix: prefix, next: next}, nil
		}
	}
	result, err := behavior.Apply[greeter](baseGreeter{}, decorate("one:"), decorate("two:"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Greet() != "two:one:hello" {
		t.Fatalf("неверная цепочка: %s", result.Greet())
	}
}

func TestApplyReportsDecoratorError(t *testing.T) {
	want := errors.New("attach failed")
	result, err := behavior.Apply[greeter](baseGreeter{}, func(greeter) (greeter, error) {
		return nil, want
	})
	if !errors.Is(err, want) || result != nil {
		t.Fatalf("получено: %#v, %v", result, err)
	}
	_, err = behavior.Apply[greeter](baseGreeter{}, nil)
	if err == nil || !strings.Contains(err.Error(), "декоратор 0") {
		t.Fatalf("ожидалась ошибка nil-декоратора, получено %v", err)
	}
}

func TestApplyWithoutDecoratorsReturnsBase(t *testing.T) {
	base := baseGreeter{}
	result, err := behavior.Apply(base)
	if err != nil || result != base {
		t.Fatalf("получено: %#v, %v", result, err)
	}
}
