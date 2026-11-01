package validation

import (
	"context"
	"math"
	"mime/multipart"
	"net/textproto"
	"regexp"
	"testing"
	"time"
)

func TestScalarValidators(t *testing.T) {
	ctx := context.Background()
	required := Required{}
	if err := required.Validate(ctx, FieldContext{Field: "name", Value: ""}); err == nil {
		t.Fatal("Required empty error = nil")
	}
	if err := (Required{AllowZero: true}).Validate(ctx, FieldContext{Field: "count", Value: 0}); err != nil {
		t.Fatalf("Required AllowZero error = %v", err)
	}

	stringValidator := String{Min: 2, Max: 3}
	for _, value := range []any{"a", "four", 42} {
		if err := stringValidator.Validate(ctx, FieldContext{Field: "name", Value: value}); err == nil {
			t.Fatalf("String(%#v) error = nil", value)
		}
	}
	value := "да"
	if err := stringValidator.Validate(ctx, FieldContext{Field: "name", Value: &value}); err != nil {
		t.Fatalf("String unicode error = %v", err)
	}

	minimum, maximum := int64(2), int64(4)
	integerValidator := Integer{Min: &minimum, Max: &maximum}
	for _, test := range []struct {
		value any
		valid bool
	}{{"3", true}, {int32(2), true}, {1, false}, {5, false}, {"x", false}, {uint64(math.MaxUint64), false}} {
		err := integerValidator.Validate(ctx, FieldContext{Field: "count", Value: test.value})
		if (err == nil) != test.valid {
			t.Fatalf("Integer(%#v) error = %v, valid=%v", test.value, err, test.valid)
		}
	}

	for _, test := range []struct {
		value any
		valid bool
	}{{"user@example.com", true}, {"User <user@example.com>", false}, {"bad", false}, {42, false}, {"", true}} {
		err := (Email{}).Validate(ctx, FieldContext{Field: "email", Value: test.value})
		if (err == nil) != test.valid {
			t.Fatalf("Email(%#v) error = %v", test.value, err)
		}
	}
}

func TestMatchDateAndCompareValidators(t *testing.T) {
	ctx := context.Background()
	match, err := Regex(`^[a-z]+$`)
	if err != nil || match.Validate(ctx, FieldContext{Field: "code", Value: "valid"}) != nil {
		t.Fatalf("Regex valid = %#v, %v", match, err)
	}
	if _, err := Regex("["); err == nil {
		t.Fatal("Regex invalid error = nil")
	}
	if err := (Match{Pattern: regexp.MustCompile(`^x$`)}).Validate(ctx, FieldContext{Field: "code", Value: "y"}); err == nil {
		t.Fatal("Match mismatch error = nil")
	}
	if err := (Match{}).Validate(ctx, FieldContext{Field: "code", Value: "x"}); err == nil {
		t.Fatal("Match nil pattern error = nil")
	}

	date := Date{Layouts: []string{"02.01.2006"}}
	if date.Validate(ctx, FieldContext{Field: "date", Value: "01.11.2026"}) != nil ||
		date.Validate(ctx, FieldContext{Field: "date", Value: time.Now()}) != nil ||
		date.Validate(ctx, FieldContext{Field: "date", Value: "bad"}) == nil {
		t.Fatal("Date validation result is invalid")
	}

	type compared struct {
		Left  int       `json:"left"`
		Right int       `json:"right"`
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
	}
	model := &compared{Left: 3, Right: 2, Start: time.Now(), End: time.Now().Add(time.Hour)}
	for _, test := range []struct {
		field string
		value any
		other string
		op    CompareOperator
		valid bool
	}{
		{"left", 3, "right", GreaterThan, true},
		{"left", 3, "right", GreaterThanOrEqual, true},
		{"right", 2, "left", LessThan, true},
		{"right", 2, "left", LessThanOrEqual, true},
		{"left", 3, "left", Equal, true},
		{"left", 3, "right", NotEqual, true},
		{"start", model.Start, "end", LessThan, true},
		{"left", 3, "right", LessThan, false},
	} {
		err := (Compare{OtherField: test.other, Operator: test.op}).Validate(ctx, FieldContext{Model: model, Field: test.field, Value: test.value})
		if (err == nil) != test.valid {
			t.Fatalf("Compare(%s %s %s) error = %v", test.field, test.op, test.other, err)
		}
	}
	if err := (Compare{OtherField: "missing", Operator: Equal}).Validate(ctx, FieldContext{Model: model, Value: 1}); err == nil {
		t.Fatal("Compare missing field error = nil")
	}
	if err := (Compare{OtherField: "right", Operator: "bad"}).Validate(ctx, FieldContext{Model: model, Value: 1}); err == nil {
		t.Fatal("Compare invalid operator error = nil")
	}
}

func TestFileValidator(t *testing.T) {
	ctx := context.Background()
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", "image/png")
	valid := &multipart.FileHeader{Filename: "photo.PNG", Size: 10, Header: header}
	validator := File{MaxBytes: 20, Extensions: []string{"png"}, ContentTypes: []string{"image/png"}}
	if err := validator.Validate(ctx, FieldContext{Field: "file", Value: []*multipart.FileHeader{valid}}); err != nil {
		t.Fatalf("File valid error = %v", err)
	}
	tests := []any{
		"not-file",
		[]*multipart.FileHeader{nil},
		&multipart.FileHeader{Filename: "photo.png", Size: 21, Header: header},
		&multipart.FileHeader{Filename: "photo.exe", Size: 10, Header: header},
		&multipart.FileHeader{Filename: "photo.png", Size: 10, Header: textproto.MIMEHeader{}},
	}
	for _, value := range tests {
		if err := validator.Validate(ctx, FieldContext{Field: "file", Value: value}); err == nil {
			t.Fatalf("File(%#v) error = nil", value)
		}
	}
}
