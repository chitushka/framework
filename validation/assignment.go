package validation

import (
	"encoding"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
)

type Assignment struct {
	Assigned []string
	Ignored  []string
}

// Assign atomically assigns only fields allowlisted by the selected scenario.
// Unknown and unsafe input keys are reported in Ignored and never mutated.
func (set Set) Assign(model any, input map[string]any, scenario string) (Assignment, error) {
	if scenario == "" {
		scenario = DefaultScenario
	}
	metadata, value, err := inspectModel(model)
	if err != nil {
		return Assignment{}, err
	}
	active, err := set.activeFields(scenario, true)
	if err != nil {
		return Assignment{}, err
	}
	for name := range active {
		if _, found := metadata.byName[name]; !found {
			return Assignment{}, fmt.Errorf("validation: scenario %q references unknown field %q", scenario, name)
		}
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	type pendingAssignment struct {
		field reflect.Value
		value reflect.Value
	}
	pending := make([]pendingAssignment, 0, len(keys))
	result := Assignment{}
	for _, key := range keys {
		_, safe := active[key]
		field, found := metadata.byName[key]
		if !safe || !found {
			result.Ignored = append(result.Ignored, key)
			continue
		}
		converted, err := convertAssignment(input[key], field.type_)
		if err != nil {
			return Assignment{}, fmt.Errorf("validation: assign %s: %w", key, err)
		}
		pending = append(pending, pendingAssignment{field: value.FieldByIndex(field.index), value: converted})
		result.Assigned = append(result.Assigned, key)
	}
	for _, assignment := range pending {
		assignment.field.Set(assignment.value)
	}
	return result, nil
}

var textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()

func convertAssignment(input any, target reflect.Type) (reflect.Value, error) {
	if input == nil {
		switch target.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
			return reflect.Zero(target), nil
		default:
			return reflect.Value{}, errors.New("null is not allowed")
		}
	}
	inputValue := reflect.ValueOf(input)
	if inputValue.Type().AssignableTo(target) {
		return inputValue, nil
	}
	if target.Kind() == reflect.Pointer {
		converted, err := convertAssignment(input, target.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		pointer := reflect.New(target.Elem())
		pointer.Elem().Set(converted)
		return pointer, nil
	}
	if reflect.PointerTo(target).Implements(textUnmarshalerType) {
		text, ok := input.(string)
		if !ok {
			return reflect.Value{}, fmt.Errorf("expected text for %s", target)
		}
		value := reflect.New(target)
		if err := value.Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(text)); err != nil {
			return reflect.Value{}, err
		}
		return value.Elem(), nil
	}
	if target.Kind() == reflect.Slice {
		if inputValue.Kind() != reflect.Slice && inputValue.Kind() != reflect.Array {
			return reflect.Value{}, fmt.Errorf("expected a list for %s", target)
		}
		result := reflect.MakeSlice(target, inputValue.Len(), inputValue.Len())
		for i := 0; i < inputValue.Len(); i++ {
			converted, err := convertAssignment(inputValue.Index(i).Interface(), target.Elem())
			if err != nil {
				return reflect.Value{}, fmt.Errorf("item %d: %w", i, err)
			}
			result.Index(i).Set(converted)
		}
		return result, nil
	}
	if text, ok := input.(string); ok {
		return convertText(text, target)
	}
	switch target.Kind() {
	case reflect.String:
		return reflect.Value{}, fmt.Errorf("expected a string, got %T", input)
	case reflect.Bool:
		if value, ok := input.(bool); ok {
			return reflect.ValueOf(value).Convert(target), nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		integer, ok := numericInteger(input)
		if ok {
			result := reflect.New(target).Elem()
			if result.OverflowInt(integer) {
				return reflect.Value{}, fmt.Errorf("%d overflows %s", integer, target)
			}
			result.SetInt(integer)
			return result, nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		integer, ok := numericInteger(input)
		if ok && integer >= 0 {
			result := reflect.New(target).Elem()
			if result.OverflowUint(uint64(integer)) {
				return reflect.Value{}, fmt.Errorf("%d overflows %s", integer, target)
			}
			result.SetUint(uint64(integer))
			return result, nil
		}
	case reflect.Float32, reflect.Float64:
		if number, ok := numericFloat(input); ok {
			result := reflect.New(target).Elem()
			if result.OverflowFloat(number) {
				return reflect.Value{}, fmt.Errorf("%v overflows %s", number, target)
			}
			result.SetFloat(number)
			return result, nil
		}
	case reflect.Interface:
		return inputValue, nil
	}
	return reflect.Value{}, fmt.Errorf("cannot convert %T to %s", input, target)
}

func convertText(text string, target reflect.Type) (reflect.Value, error) {
	result := reflect.New(target).Elem()
	var err error
	switch target.Kind() {
	case reflect.String:
		result.SetString(text)
	case reflect.Bool:
		var value bool
		value, err = strconv.ParseBool(text)
		result.SetBool(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var value int64
		value, err = strconv.ParseInt(text, 10, target.Bits())
		result.SetInt(value)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		var value uint64
		value, err = strconv.ParseUint(text, 10, target.Bits())
		result.SetUint(value)
	case reflect.Float32, reflect.Float64:
		var value float64
		value, err = strconv.ParseFloat(text, target.Bits())
		result.SetFloat(value)
	default:
		return reflect.Value{}, fmt.Errorf("cannot convert text to %s", target)
	}
	if err != nil {
		return reflect.Value{}, err
	}
	return result, nil
}

func numericInteger(value any) (int64, bool) {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		unsigned := reflected.Uint()
		if unsigned > math.MaxInt64 {
			return 0, false
		}
		return int64(unsigned), true
	case reflect.Float32, reflect.Float64:
		float := reflected.Float()
		if math.Trunc(float) != float || float < math.MinInt64 || float >= math.MaxInt64 {
			return 0, false
		}
		return int64(float), true
	default:
		return 0, false
	}
}

func numericFloat(value any) (float64, bool) {
	reflected := reflect.ValueOf(value)
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
