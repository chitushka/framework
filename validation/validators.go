package validation

import (
	"context"
	"fmt"
	"mime/multipart"
	"net/mail"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Required struct {
	AllowZero bool
	Message   string
}

func (validator Required) Validate(_ context.Context, field FieldContext) error {
	empty := isEmpty(field.Value)
	if validator.AllowZero && field.Value != nil {
		value := reflect.ValueOf(field.Value)
		empty = value.Kind() == reflect.String && value.Len() == 0 ||
			(value.Kind() == reflect.Pointer && value.IsNil())
	}
	if !empty {
		return nil
	}
	return Invalid("required", messageOr(validator.Message, field.Field+" is required"))
}

type String struct {
	Min     int
	Max     int
	Message string
}

func (validator String) Validate(_ context.Context, field FieldContext) error {
	if isEmpty(field.Value) {
		return nil
	}
	value, ok := indirect(field.Value).(string)
	if !ok {
		return Invalid("string", messageOr(validator.Message, field.Field+" must be a string"))
	}
	length := len([]rune(value))
	if validator.Min > 0 && length < validator.Min {
		return Invalid("string_min", messageOr(validator.Message, fmt.Sprintf("%s must contain at least %d characters", field.Field, validator.Min)))
	}
	if validator.Max > 0 && length > validator.Max {
		return Invalid("string_max", messageOr(validator.Message, fmt.Sprintf("%s must contain at most %d characters", field.Field, validator.Max)))
	}
	return nil
}

type Integer struct {
	Min     *int64
	Max     *int64
	Message string
}

func (validator Integer) Validate(_ context.Context, field FieldContext) error {
	if isEmpty(field.Value) {
		return nil
	}
	value, ok := integer(field.Value)
	if !ok {
		return Invalid("integer", messageOr(validator.Message, field.Field+" must be an integer"))
	}
	if validator.Min != nil && value < *validator.Min {
		return Invalid("integer_min", messageOr(validator.Message, fmt.Sprintf("%s must be at least %d", field.Field, *validator.Min)))
	}
	if validator.Max != nil && value > *validator.Max {
		return Invalid("integer_max", messageOr(validator.Message, fmt.Sprintf("%s must be at most %d", field.Field, *validator.Max)))
	}
	return nil
}

type Email struct{ Message string }

func (validator Email) Validate(_ context.Context, field FieldContext) error {
	if isEmpty(field.Value) {
		return nil
	}
	value, ok := indirect(field.Value).(string)
	if ok {
		address, err := mail.ParseAddress(value)
		ok = err == nil && address.Address == value && strings.Contains(value, "@")
	}
	if !ok {
		return Invalid("email", messageOr(validator.Message, field.Field+" must be a valid email address"))
	}
	return nil
}

type Match struct {
	Pattern *regexp.Regexp
	Message string
}

func Regex(pattern string) (Match, error) {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return Match{}, fmt.Errorf("validation: compile pattern: %w", err)
	}
	return Match{Pattern: compiled}, nil
}

func (validator Match) Validate(_ context.Context, field FieldContext) error {
	if isEmpty(field.Value) {
		return nil
	}
	value, ok := indirect(field.Value).(string)
	if validator.Pattern == nil {
		return fmt.Errorf("validation: match pattern must not be nil")
	}
	if !ok || !validator.Pattern.MatchString(value) {
		return Invalid("match", messageOr(validator.Message, field.Field+" has an invalid format"))
	}
	return nil
}

type CompareOperator string

const (
	Equal              CompareOperator = "eq"
	NotEqual           CompareOperator = "ne"
	GreaterThan        CompareOperator = "gt"
	GreaterThanOrEqual CompareOperator = "gte"
	LessThan           CompareOperator = "lt"
	LessThanOrEqual    CompareOperator = "lte"
)

type Compare struct {
	OtherField string
	Operator   CompareOperator
	Message    string
}

func (validator Compare) Validate(_ context.Context, field FieldContext) error {
	metadata, model, err := inspectModel(field.Model)
	if err != nil {
		return err
	}
	other, found := metadata.byName[validator.OtherField]
	if !found {
		return fmt.Errorf("validation: compare field %q does not exist", validator.OtherField)
	}
	comparison, comparable := compareValues(field.Value, model.FieldByIndex(other.index).Interface())
	valid := false
	switch validator.Operator {
	case Equal:
		valid = reflect.DeepEqual(indirect(field.Value), indirect(model.FieldByIndex(other.index).Interface()))
	case NotEqual:
		valid = !reflect.DeepEqual(indirect(field.Value), indirect(model.FieldByIndex(other.index).Interface()))
	case GreaterThan:
		valid = comparable && comparison > 0
	case GreaterThanOrEqual:
		valid = comparable && comparison >= 0
	case LessThan:
		valid = comparable && comparison < 0
	case LessThanOrEqual:
		valid = comparable && comparison <= 0
	default:
		return fmt.Errorf("validation: unknown compare operator %q", validator.Operator)
	}
	if !valid {
		return Invalid("compare", messageOr(validator.Message, field.Field+" has an invalid comparison"))
	}
	return nil
}

type Date struct {
	Layouts []string
	Message string
}

func (validator Date) Validate(_ context.Context, field FieldContext) error {
	if isEmpty(field.Value) {
		return nil
	}
	if _, ok := indirect(field.Value).(time.Time); ok {
		return nil
	}
	value, ok := indirect(field.Value).(string)
	if ok {
		layouts := validator.Layouts
		if len(layouts) == 0 {
			layouts = []string{time.DateOnly, time.RFC3339}
		}
		for _, layout := range layouts {
			if _, err := time.Parse(layout, value); err == nil {
				return nil
			}
		}
	}
	return Invalid("date", messageOr(validator.Message, field.Field+" must be a valid date"))
}

type File struct {
	MaxBytes     int64
	Extensions   []string
	ContentTypes []string
	Message      string
}

func (validator File) Validate(_ context.Context, field FieldContext) error {
	if isEmpty(field.Value) {
		return nil
	}
	files, ok := fileHeaders(field.Value)
	if !ok {
		return Invalid("file", messageOr(validator.Message, field.Field+" must be an uploaded file"))
	}
	for _, header := range files {
		if header == nil {
			return Invalid("file", messageOr(validator.Message, field.Field+" contains an invalid file"))
		}
		if validator.MaxBytes > 0 && header.Size > validator.MaxBytes {
			return Invalid("file_size", messageOr(validator.Message, field.Field+" exceeds the maximum size"))
		}
		if len(validator.Extensions) > 0 && !containsFold(validator.Extensions, strings.TrimPrefix(filepath.Ext(header.Filename), ".")) {
			return Invalid("file_extension", messageOr(validator.Message, field.Field+" has an invalid extension"))
		}
		if len(validator.ContentTypes) > 0 && !containsFold(validator.ContentTypes, header.Header.Get("Content-Type")) {
			return Invalid("file_type", messageOr(validator.Message, field.Field+" has an invalid content type"))
		}
	}
	return nil
}

// Safe marks a field as validated without imposing a constraint. Assignment is
// still controlled exclusively by Scenario.Fields.
type Safe struct{}

func (Safe) Validate(context.Context, FieldContext) error { return nil }

func indirect(value any) any {
	if value == nil {
		return nil
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Pointer || reflected.Kind() == reflect.Interface {
		if reflected.IsNil() {
			return nil
		}
		reflected = reflected.Elem()
	}
	return reflected.Interface()
}

func integer(value any) (int64, bool) {
	value = indirect(value)
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return 0, false
	}
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		unsigned := reflected.Uint()
		if unsigned > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(unsigned), true
	case reflect.String:
		parsed, err := strconv.ParseInt(reflected.String(), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func compareValues(left, right any) (int, bool) {
	left, right = indirect(left), indirect(right)
	if leftTime, ok := left.(time.Time); ok {
		rightTime, rightOK := right.(time.Time)
		if !rightOK {
			return 0, false
		}
		if leftTime.Before(rightTime) {
			return -1, true
		}
		if leftTime.After(rightTime) {
			return 1, true
		}
		return 0, true
	}
	if leftString, ok := left.(string); ok {
		rightString, rightOK := right.(string)
		if !rightOK {
			return 0, false
		}
		return strings.Compare(leftString, rightString), true
	}
	leftNumber, leftOK := numeric(left)
	rightNumber, rightOK := numeric(right)
	if !leftOK || !rightOK {
		return 0, false
	}
	if leftNumber < rightNumber {
		return -1, true
	}
	if leftNumber > rightNumber {
		return 1, true
	}
	return 0, true
}

func numeric(value any) (float64, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return 0, false
	}
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(reflected.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(reflected.Uint()), true
	case reflect.Float32, reflect.Float64:
		return reflected.Float(), true
	default:
		return 0, false
	}
}

func fileHeaders(value any) ([]*multipart.FileHeader, bool) {
	switch files := value.(type) {
	case *multipart.FileHeader:
		return []*multipart.FileHeader{files}, true
	case multipart.FileHeader:
		return []*multipart.FileHeader{&files}, true
	case []*multipart.FileHeader:
		return files, true
	default:
		return nil, false
	}
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func messageOr(message, fallback string) string {
	if message != "" {
		return message
	}
	return fallback
}
