package di_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/chitushka/framework/di"
)

type repository interface{ Name() string }
type memoryRepository struct{ name string }

func (r *memoryRepository) Name() string { return r.name }

type service struct{ repository repository }

func TestContainerResolvesConstructorAndInterface(t *testing.T) {
	container := di.New()
	if err := container.Provide(func() *memoryRepository { return &memoryRepository{name: "memory"} }, di.Singleton()); err != nil {
		t.Fatal(err)
	}
	if err := container.Bind((*repository)(nil), (*memoryRepository)(nil)); err != nil {
		t.Fatal(err)
	}
	if err := container.Provide(func(repo repository) *service { return &service{repository: repo} }); err != nil {
		t.Fatal(err)
	}

	var resolved *service
	if err := container.Resolve(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved.repository.Name() != "memory" {
		t.Fatalf("получено имя %q", resolved.repository.Name())
	}
}

func TestContainerSingleton(t *testing.T) {
	container := di.New()
	created := 0
	if err := container.Provide(func() *memoryRepository {
		created++
		return &memoryRepository{}
	}, di.Singleton()); err != nil {
		t.Fatal(err)
	}

	var first, second *memoryRepository
	if err := container.Resolve(&first); err != nil {
		t.Fatal(err)
	}
	if err := container.Resolve(&second); err != nil {
		t.Fatal(err)
	}
	if first != second || created != 1 {
		t.Fatalf("singleton создан %d раз", created)
	}
}

func TestContainerAutowiresTaggedFields(t *testing.T) {
	type controller struct {
		Repository repository `di:"inject"`
	}
	container := di.New()
	_ = container.Provide(func() *memoryRepository { return &memoryRepository{name: "auto"} })
	_ = container.Bind((*repository)(nil), (*memoryRepository)(nil))

	var resolved *controller
	if err := container.Resolve(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved.Repository.Name() != "auto" {
		t.Fatal("поле не внедрено")
	}
}

func TestContainerPropagatesConstructorError(t *testing.T) {
	want := errors.New("не удалось создать")
	container := di.New()
	_ = container.Provide(func() (*memoryRepository, error) { return nil, want })
	var resolved *memoryRepository
	if err := container.Resolve(&resolved); !errors.Is(err, want) {
		t.Fatalf("получена ошибка %v", err)
	}
}

func TestContainerDetectsCycle(t *testing.T) {
	type first struct{}
	type second struct{}
	container := di.New()
	_ = container.Provide(func(*second) *first { return &first{} })
	_ = container.Provide(func(*first) *second { return &second{} })
	var resolved *first
	if err := container.Resolve(&resolved); err == nil || !strings.Contains(err.Error(), "циклическая") {
		t.Fatalf("ожидалась ошибка цикла, получено %v", err)
	}
}

func TestContainerInstanceAndInvoke(t *testing.T) {
	container := di.New()
	repository := &memoryRepository{name: "instance"}
	if err := container.Instance(repository); err != nil {
		t.Fatal(err)
	}
	results, err := container.Invoke(func(value *memoryRepository) string { return value.Name() })
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0] != "instance" {
		t.Fatalf("неверный результат: %v", results)
	}
}

func TestContainerRejectsInvalidRegistrations(t *testing.T) {
	container := di.New()
	if err := container.Provide(42); err == nil {
		t.Fatal("ожидалась ошибка конструктора")
	}
	if err := container.Provide(func() (*memoryRepository, string) { return nil, "bad" }); err == nil {
		t.Fatal("ожидалась ошибка сигнатуры")
	}
	if err := container.Instance(nil); err == nil {
		t.Fatal("ожидалась ошибка nil instance")
	}
	if err := container.Bind((*repository)(nil), (*service)(nil)); err == nil {
		t.Fatal("ожидалась ошибка несовместимой реализации")
	}
	var value *memoryRepository
	if err := container.Resolve(value); err == nil {
		t.Fatal("ожидалась ошибка target")
	}
	if _, err := container.Invoke(42); err == nil {
		t.Fatal("ожидалась ошибка Invoke")
	}
}
