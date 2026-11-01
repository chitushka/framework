package validation

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func TestConvertAssignmentTypes(t *testing.T) {
	tests := []struct {
		input  any
		target reflect.Type
		want   any
	}{
		{"true", reflect.TypeFor[bool](), true},
		{"-12", reflect.TypeFor[int16](), int16(-12)},
		{"12", reflect.TypeFor[uint16](), uint16(12)},
		{"1.5", reflect.TypeFor[float32](), float32(1.5)},
		{float64(3), reflect.TypeFor[int](), 3},
		{int32(4), reflect.TypeFor[float64](), float64(4)},
		{"text", reflect.TypeFor[*string](), pointer("text")},
		{[]any{"1", "2"}, reflect.TypeFor[[]int](), []int{1, 2}},
		{"2026-11-01T10:00:00Z", reflect.TypeFor[time.Time](), time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)},
		{nil, reflect.TypeFor[*string](), (*string)(nil)},
	}
	for _, test := range tests {
		value, err := convertAssignment(test.input, test.target)
		if err != nil {
			t.Fatalf("convertAssignment(%#v, %s) error = %v", test.input, test.target, err)
		}
		if !reflect.DeepEqual(value.Interface(), test.want) {
			t.Fatalf("convertAssignment(%#v, %s) = %#v, want %#v", test.input, test.target, value.Interface(), test.want)
		}
	}
}

func TestConvertAssignmentRejectsUnsafeConversions(t *testing.T) {
	tests := []struct {
		input  any
		target reflect.Type
	}{
		{nil, reflect.TypeFor[int]()},
		{1.5, reflect.TypeFor[int]()},
		{-1, reflect.TypeFor[uint]()},
		{math.MaxInt64, reflect.TypeFor[int8]()},
		{"not-bool", reflect.TypeFor[bool]()},
		{42, reflect.TypeFor[string]()},
		{"bad-time", reflect.TypeFor[time.Time]()},
		{"not-list", reflect.TypeFor[[]string]()},
		{[]any{"ok", 2}, reflect.TypeFor[[]string]()},
	}
	for _, test := range tests {
		if _, err := convertAssignment(test.input, test.target); err == nil {
			t.Fatalf("convertAssignment(%#v, %s) error = nil", test.input, test.target)
		}
	}
}

func pointer[T any](value T) *T { return &value }
