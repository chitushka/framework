package validation

import (
	"reflect"
	"testing"
	"time"
)

func TestAssignUsesScenarioAllowlistAndConversions(t *testing.T) {
	model := &testInput{ID: 10, Role: "user"}
	created := "2026-11-01T10:00:00Z"
	result, err := testSet().Assign(model, map[string]any{
		"id": int64(999), "role": "admin", "unknown": "ignored",
		"email": "ada@example.test", "name": "Ada", "age": "37",
		"active": true, "tags": []any{"go", "web"}, "created": created,
	}, "create")
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if model.ID != 10 || model.Role != "user" {
		t.Fatalf("unsafe fields changed: %#v", model)
	}
	if model.Email != "ada@example.test" || model.Age != 37 || !model.Active || !reflect.DeepEqual(model.Tags, []string{"go", "web"}) {
		t.Fatalf("assigned model = %#v", model)
	}
	if model.Created.Format(time.RFC3339) != created {
		t.Fatalf("Created = %s", model.Created)
	}
	if !reflect.DeepEqual(result.Ignored, []string{"id", "role", "unknown"}) {
		t.Fatalf("Ignored = %#v", result.Ignored)
	}
}

func TestAssignIsAtomicOnConversionFailure(t *testing.T) {
	model := &testInput{Email: "before", Age: 30}
	_, err := testSet().Assign(model, map[string]any{"email": "after", "age": "invalid"}, "update")
	if err == nil {
		t.Fatal("Assign() error = nil")
	}
	if model.Email != "before" || model.Age != 30 {
		t.Fatalf("Assign() partially mutated model: %#v", model)
	}
}

func TestAssignRequiresExplicitScenario(t *testing.T) {
	if _, err := (Set{}).Assign(&testInput{}, map[string]any{"email": "x"}, ""); err == nil {
		t.Fatal("Assign() without allowlist error = nil")
	}
	if _, err := testSet().Assign(&testInput{}, nil, "missing"); err == nil {
		t.Fatal("Assign() unknown scenario error = nil")
	}
}
