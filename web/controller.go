package web

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"

	"github.com/chitushka/framework/di"
)

var (
	controllerType = reflect.TypeFor[Controller]()
	contextType    = reflect.TypeFor[*Context]()
	errorType      = reflect.TypeFor[error]()
)

// Action describes the controller action currently being executed.
type Action struct {
	ID string
}

// Controller participates in the action lifecycle.
// Embedding BaseController is the simplest way to opt in without custom hooks.
type Controller interface {
	BeforeAction(*Context, Action) error
	AfterAction(*Context, Action, error) error
}

// BaseController provides no-op controller lifecycle hooks.
type BaseController struct{}

// BeforeAction permits action execution by default.
func (BaseController) BeforeAction(*Context, Action) error { return nil }

// AfterAction leaves the action result unchanged by default.
func (BaseController) AfterAction(*Context, Action, error) error { return nil }

// NewControllerAction adapts a controller method expression to Handler.
// The function must have the form func(*Controller, *Context, Dependencies...) error.
// The controller and all action dependencies are resolved through the DI container
// for every request; their lifetime follows the container registration. *Context is
// supplied by the router.
func NewControllerAction(container *di.Container, function any) (Handler, error) {
	if container == nil {
		return nil, errors.New("web: DI container for controller action cannot be nil")
	}

	value := reflect.ValueOf(function)
	if !value.IsValid() || value.Kind() != reflect.Func || value.IsNil() {
		return nil, errors.New("web: controller action must be a non-nil function")
	}
	typeOf := value.Type()
	if typeOf.NumIn() < 2 {
		return nil, errors.New("web: controller action must accept a controller and *web.Context")
	}
	if !typeOf.In(0).Implements(controllerType) {
		return nil, fmt.Errorf("web: first action argument %s does not implement web.Controller", typeOf.In(0))
	}
	contextArguments := 0
	for i := 1; i < typeOf.NumIn(); i++ {
		if typeOf.In(i) == contextType {
			contextArguments++
		}
	}
	if contextArguments != 1 {
		return nil, errors.New("web: controller action must accept exactly one *web.Context argument")
	}
	if typeOf.NumOut() != 1 || typeOf.Out(0) != errorType {
		return nil, errors.New("web: controller action must return error")
	}

	action := Action{ID: actionID(value)}
	return func(context *Context) error {
		arguments := make([]reflect.Value, typeOf.NumIn())
		for i := range arguments {
			if typeOf.In(i) == contextType {
				arguments[i] = reflect.ValueOf(context)
				continue
			}
			target := reflect.New(typeOf.In(i))
			if err := container.Resolve(target.Interface()); err != nil {
				return fmt.Errorf("web: resolve argument %d of action %q: %w", i, action.ID, err)
			}
			arguments[i] = target.Elem()
		}

		controller := arguments[0].Interface().(Controller)
		if err := controller.BeforeAction(context, action); err != nil {
			return fmt.Errorf("web: before action %q: %w", action.ID, err)
		}

		result := value.Call(arguments)[0]
		var actionErr error
		if !result.IsNil() {
			actionErr = result.Interface().(error)
		}
		afterErr := controller.AfterAction(context, action, actionErr)
		if afterErr != nil {
			afterErr = fmt.Errorf("web: after action %q: %w", action.ID, afterErr)
		}
		return errors.Join(actionErr, afterErr)
	}, nil
}

func actionID(function reflect.Value) string {
	info := runtime.FuncForPC(function.Pointer())
	if info == nil {
		return "action"
	}
	name := strings.TrimSuffix(info.Name(), "-fm")
	if index := strings.LastIndex(name, "."); index >= 0 {
		name = name[index+1:]
	}
	return name
}
