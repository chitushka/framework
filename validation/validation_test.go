package validation

import (
	"context"
	"errors"
	"mime/multipart"
	"net/textproto"
	"regexp"
	"testing"
	"time"
)

type testInput struct {
	ID       int64                 `json:"id"`
	Email    string                `json:"email"`
	Name     string                `form:"name" json:"display_name"`
	Age      int                   `json:"age"`
	Password string                `json:"password"`
	Confirm  string                `json:"confirm"`
	Birthday string                `json:"birthday"`
	Avatar   *multipart.FileHeader `json:"avatar"`
	Role     string                `json:"role"`
	Active   bool                  `json:"active"`
	Tags     []string              `json:"tags"`
	Created  time.Time             `json:"created"`
}

type providedInput struct {
	Email string `json:"email"`
}

func (*providedInput) Validation() Set {
	return Set{
		Scenarios: map[string]Scenario{"create": {Fields: []string{"email"}}},
		Rules:     []Rule{Field("email", Required{})},
	}
}

func testSet() Set {
	minimumAge, maximumAge := int64(18), int64(120)
	return Set{
		Scenarios: map[string]Scenario{
			"create": {Fields: []string{"email", "name", "age", "password", "confirm", "birthday", "avatar", "active", "tags", "created"}},
			"update": {Fields: []string{"email", "name", "age", "active", "tags"}},
		},
		Rules: []Rule{
			For([]string{"email", "password"}, Required{}).In("create"),
			For([]string{"email"}, Email{}),
			For([]string{"name"}, String{Min: 2, Max: 20}),
			For([]string{"age"}, Integer{Min: &minimumAge, Max: &maximumAge}),
			For([]string{"password"}, Match{Pattern: regexp.MustCompile(`^[a-zA-Z0-9]{8,}$`)}).In("create"),
			For([]string{"confirm"}, Compare{OtherField: "password", Operator: Equal}).In("create"),
			For([]string{"birthday"}, Date{}).In("create"),
			For([]string{"avatar"}, File{MaxBytes: 1024, Extensions: []string{"png"}, ContentTypes: []string{"image/png"}}).In("create"),
			For([]string{"active", "tags", "created"}, Safe{}),
		},
	}
}

func TestValidateScenariosAndBuiltInRules(t *testing.T) {
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", "image/png")
	valid := &testInput{
		Email: "ada@example.test", Name: "Ada", Age: 37,
		Password: "password1", Confirm: "password1", Birthday: "1815-12-10",
		Avatar: &multipart.FileHeader{Filename: "avatar.PNG", Size: 100, Header: header},
	}
	errors_, err := testSet().Validate(context.Background(), valid, "create")
	if err != nil || !errors_.Valid() {
		t.Fatalf("Validate(valid) = %#v, %v", errors_, err)
	}

	invalid := &testInput{
		Email: "not-an-email", Name: "x", Age: 10,
		Password: "short", Confirm: "different", Birthday: "not-date",
		Avatar: &multipart.FileHeader{Filename: "avatar.exe", Size: 2048, Header: textproto.MIMEHeader{}},
	}
	errors_, err = testSet().Validate(context.Background(), invalid, "create")
	if err != nil {
		t.Fatalf("Validate(invalid) operational error = %v", err)
	}
	for _, field := range []string{"email", "name", "age", "password", "confirm", "birthday", "avatar"} {
		if _, found := errors_.First(field); !found {
			t.Errorf("Validate(invalid) has no error for %s: %#v", field, errors_)
		}
	}

	update := &testInput{Name: "Valid", Age: 20}
	errors_, err = testSet().Validate(context.Background(), update, "update")
	if err != nil || !errors_.Valid() {
		t.Fatalf("Validate(update) = %#v, %v", errors_, err)
	}
}

func TestValidateCustomRulesAndFailures(t *testing.T) {
	called := false
	set := Set{Rules: []Rule{
		For([]string{"email"}, ValidatorFunc(func(_ context.Context, field FieldContext) error {
			called = field.Scenario == DefaultScenario
			return Invalid("custom", "custom failure")
		})),
	}}
	errors_, err := set.Validate(context.Background(), &testInput{}, "")
	if err != nil || !called {
		t.Fatalf("Validate() = %#v, %v, called=%v", errors_, err, called)
	}
	if violation, found := errors_.First("email"); !found || violation.Code != "custom" {
		t.Fatalf("First(email) = %#v, %v", violation, found)
	}

	operational := errors.New("service unavailable")
	set.Rules = []Rule{For([]string{"email"}, ValidatorFunc(func(context.Context, FieldContext) error { return operational }))}
	if _, err := set.Validate(context.Background(), &testInput{}, ""); !errors.Is(err, operational) {
		t.Fatalf("operational error = %v", err)
	}

	set.Rules = []Rule{For([]string{"email"}, Safe{}).ExceptIn(DefaultScenario)}
	errors_, err = set.Validate(context.Background(), &testInput{}, "")
	if err != nil || !errors_.Valid() {
		t.Fatalf("ExceptIn Validate() = %#v, %v", errors_, err)
	}
}

func TestValidateConfigurationErrors(t *testing.T) {
	tests := []Set{
		{Rules: []Rule{{Fields: []string{"email"}}}},
		{Rules: []Rule{For([]string{"missing"}, Safe{})}},
		{Scenarios: map[string]Scenario{"create": {Fields: []string{"missing"}}}},
	}
	for i, set := range tests {
		if i == 2 {
			if _, err := set.Assign(&testInput{}, nil, "create"); err == nil {
				t.Fatalf("test %d error = nil", i)
			}
		} else if _, err := set.Validate(context.Background(), &testInput{}, ""); err == nil {
			t.Fatalf("test %d error = nil", i)
		}
	}
	if _, err := testSet().Validate(context.Background(), &testInput{}, "missing"); err == nil {
		t.Fatal("unknown scenario error = nil")
	}
	if _, err := (Set{}).Validate(context.Background(), testInput{}, ""); err == nil {
		t.Fatal("non-pointer model error = nil")
	}
}

func TestProviderHelpers(t *testing.T) {
	model := &providedInput{}
	if _, err := AssignModel(model, map[string]any{"email": "user@example.test"}, "create"); err != nil {
		t.Fatalf("AssignModel() error = %v", err)
	}
	validationErrors, err := ValidateModel(context.Background(), model, "create")
	if err != nil || !validationErrors.Valid() || model.Email == "" {
		t.Fatalf("ValidateModel() = %#v, %v, model=%#v", validationErrors, err, model)
	}
}
