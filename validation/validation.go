// Package validation provides scenario-aware model validation and safe mass assignment.
package validation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

const DefaultScenario = "default"

// FieldContext contains the model and current field visible to a Validator.
type FieldContext struct {
	Model    any
	Field    string
	Value    any
	Scenario string
}

type Validator interface {
	Validate(context.Context, FieldContext) error
}

type ValidatorFunc func(context.Context, FieldContext) error

func (function ValidatorFunc) Validate(ctx context.Context, field FieldContext) error {
	return function(ctx, field)
}

// Violation is a user-correctable validation failure. Other errors are treated
// as operational failures and returned separately by Set.Validate.
type Violation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (violation Violation) Error() string { return violation.Message }

func Invalid(code, message string) error { return Violation{Code: code, Message: message} }

// Errors groups all validation violations by external field name.
type Errors map[string][]Violation

func (validationErrors Errors) Valid() bool { return len(validationErrors) == 0 }

func (validationErrors Errors) First(field string) (Violation, bool) {
	items := validationErrors[field]
	if len(items) == 0 {
		return Violation{}, false
	}
	return items[0], true
}

type Rule struct {
	Fields    []string
	Validator Validator
	On        []string
	Except    []string
}

func For(fields []string, validator Validator) Rule {
	return Rule{Fields: append([]string(nil), fields...), Validator: validator}
}

func Field(name string, validator Validator) Rule { return For([]string{name}, validator) }

func Fields(validator Validator, names ...string) Rule { return For(names, validator) }

func (rule Rule) In(scenarios ...string) Rule {
	rule.On = append([]string(nil), scenarios...)
	return rule
}

func (rule Rule) ExceptIn(scenarios ...string) Rule {
	rule.Except = append([]string(nil), scenarios...)
	return rule
}

type Scenario struct {
	// Fields is the explicit allowlist for mass assignment and validation.
	Fields []string
}

type Set struct {
	Rules     []Rule
	Scenarios map[string]Scenario
}

// Provider lets a model own its declarative validation contract.
type Provider interface{ Validation() Set }

func ValidateModel(ctx context.Context, model Provider, scenario string) (Errors, error) {
	if model == nil {
		return nil, errors.New("validation: provider must not be nil")
	}
	return model.Validation().Validate(ctx, model, scenario)
}

func AssignModel(model Provider, input map[string]any, scenario string) (Assignment, error) {
	if model == nil {
		return Assignment{}, errors.New("validation: provider must not be nil")
	}
	return model.Validation().Assign(model, input, scenario)
}

func (set Set) Validate(ctx context.Context, model any, scenario string) (Errors, error) {
	if model == nil {
		return nil, errors.New("validation: model must not be nil")
	}
	if scenario == "" {
		scenario = DefaultScenario
	}
	metadata, value, err := inspectModel(model)
	if err != nil {
		return nil, err
	}
	activeFields, err := set.activeFields(scenario, false)
	if err != nil {
		return nil, err
	}
	validationErrors := make(Errors)
	for index, rule := range set.Rules {
		if rule.Validator == nil {
			return validationErrors, fmt.Errorf("validation: rule %d has nil validator", index)
		}
		if !ruleActive(rule, scenario) {
			continue
		}
		for _, name := range rule.Fields {
			if activeFields != nil {
				if _, active := activeFields[name]; !active {
					continue
				}
			}
			field, found := metadata.byName[name]
			if !found {
				return validationErrors, fmt.Errorf("validation: model has no field %q", name)
			}
			err := rule.Validator.Validate(ctx, FieldContext{
				Model: model, Field: name, Value: value.FieldByIndex(field.index).Interface(), Scenario: scenario,
			})
			if err == nil {
				continue
			}
			var violation Violation
			if errors.As(err, &violation) {
				validationErrors[name] = append(validationErrors[name], violation)
				continue
			}
			return validationErrors, fmt.Errorf("validation: validate %s: %w", name, err)
		}
	}
	return validationErrors, nil
}

func (set Set) activeFields(scenario string, assignment bool) (map[string]struct{}, error) {
	if len(set.Scenarios) == 0 {
		if assignment {
			return nil, errors.New("validation: mass assignment requires an explicit scenario allowlist")
		}
		return nil, nil
	}
	configuration, found := set.Scenarios[scenario]
	if !found {
		return nil, fmt.Errorf("validation: unknown scenario %q", scenario)
	}
	fields := make(map[string]struct{}, len(configuration.Fields))
	for _, field := range configuration.Fields {
		if strings.TrimSpace(field) == "" {
			return nil, fmt.Errorf("validation: scenario %q contains an empty field", scenario)
		}
		fields[field] = struct{}{}
	}
	return fields, nil
}

func ruleActive(rule Rule, scenario string) bool {
	if contains(rule.Except, scenario) {
		return false
	}
	return len(rule.On) == 0 || contains(rule.On, scenario)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func isEmpty(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return true
		}
		reflected = reflected.Elem()
	}
	switch reflected.Kind() {
	case reflect.String, reflect.Array, reflect.Slice, reflect.Map:
		return reflected.Len() == 0
	default:
		return reflected.IsZero()
	}
}
