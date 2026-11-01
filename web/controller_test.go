package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/chitushka/framework/di"
	"github.com/chitushka/framework/web"
)

type greetingService interface {
	Greeting() string
}

type greetingServiceImpl struct{}

func (*greetingServiceImpl) Greeting() string { return "hello" }

type lifecycleLog struct {
	calls       []string
	controllers int
	actionErr   error
}

type testController struct {
	web.BaseController
	log *lifecycleLog
}

func newTestController(log *lifecycleLog) *testController {
	log.controllers++
	return &testController{log: log}
}

func (controller *testController) BeforeAction(_ *web.Context, action web.Action) error {
	controller.log.calls = append(controller.log.calls, "before:"+action.ID)
	if action.ID == "Blocked" {
		return errBlocked
	}
	return nil
}

func (controller *testController) AfterAction(_ *web.Context, action web.Action, actionErr error) error {
	controller.log.calls = append(controller.log.calls, "after:"+action.ID)
	controller.log.actionErr = actionErr
	if action.ID == "Failed" {
		return errAfter
	}
	return nil
}

func (controller *testController) Show(context *web.Context, service greetingService) error {
	controller.log.calls = append(controller.log.calls, "action:Show")
	return context.JSON(http.StatusOK, map[string]string{
		"id":       context.Param("id"),
		"greeting": service.Greeting(),
	})
}

func (controller *testController) Failed(*web.Context) error {
	controller.log.calls = append(controller.log.calls, "action:Failed")
	return errAction
}

func (controller *testController) Blocked(*web.Context) error {
	controller.log.calls = append(controller.log.calls, "action:Blocked")
	return nil
}

var (
	errAction  = errors.New("action failed")
	errAfter   = errors.New("after failed")
	errBlocked = errors.New("action blocked")
)

func newControllerContainer(t *testing.T, log *lifecycleLog) *di.Container {
	t.Helper()
	container := di.New()
	if err := container.Instance(log); err != nil {
		t.Fatal(err)
	}
	if err := container.Provide(newTestController); err != nil {
		t.Fatal(err)
	}
	if err := container.Provide(func() *greetingServiceImpl { return &greetingServiceImpl{} }); err != nil {
		t.Fatal(err)
	}
	if err := container.Bind((*greetingService)(nil), (*greetingServiceImpl)(nil)); err != nil {
		t.Fatal(err)
	}
	return container
}

func TestControllerActionLifecycleDIAndNamedRoute(t *testing.T) {
	log := &lifecycleLog{}
	container := newControllerContainer(t, log)
	router := web.NewRouter()
	err := router.Group("/api", func(group *web.Group) error {
		return group.GETAction("/users/{id}", container, (*testController).Show, web.WithName("users.show"))
	})
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/users/42", nil))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"greeting":"hello","id":"42"}` {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
	wantCalls := []string{"before:Show", "action:Show", "after:Show"}
	if !reflect.DeepEqual(log.calls, wantCalls) {
		t.Fatalf("unexpected lifecycle: %v", log.calls)
	}
	if log.controllers != 1 {
		t.Fatalf("expected one controller, got %d", log.controllers)
	}
	generated, err := router.URL("users.show", map[string]string{"id": "42"})
	if err != nil || generated != "/api/users/42" {
		t.Fatalf("unexpected generated URL: %q, %v", generated, err)
	}
}

func TestControllerActionCombinesActionAndAfterErrors(t *testing.T) {
	log := &lifecycleLog{}
	container := newControllerContainer(t, log)
	handler, err := web.NewControllerAction(container, (*testController).Failed)
	if err != nil {
		t.Fatal(err)
	}
	err = handler(&web.Context{
		Writer:  httptest.NewRecorder(),
		Request: httptest.NewRequest(http.MethodGet, "/failed", nil),
	})
	if !errors.Is(err, errAction) || !errors.Is(err, errAfter) {
		t.Fatalf("expected joined action and after errors, got %v", err)
	}
	if !errors.Is(log.actionErr, errAction) {
		t.Fatalf("AfterAction did not receive action error: %v", log.actionErr)
	}
}

func TestControllerBeforeActionCanBlockExecution(t *testing.T) {
	log := &lifecycleLog{}
	container := newControllerContainer(t, log)
	handler, err := web.NewControllerAction(container, (*testController).Blocked)
	if err != nil {
		t.Fatal(err)
	}
	err = handler(&web.Context{
		Writer:  httptest.NewRecorder(),
		Request: httptest.NewRequest(http.MethodGet, "/blocked", nil),
	})
	if !errors.Is(err, errBlocked) {
		t.Fatalf("expected BeforeAction error, got %v", err)
	}
	if want := []string{"before:Blocked"}; !reflect.DeepEqual(log.calls, want) {
		t.Fatalf("blocked action continued execution: %v", log.calls)
	}
}

func TestControllerActionValidation(t *testing.T) {
	container := di.New()
	var nilAction func(*testController, *web.Context) error
	for name, err := range map[string]error{
		"nil container": func() error { _, err := web.NewControllerAction(nil, (*testController).Show); return err }(),
		"not function":  func() error { _, err := web.NewControllerAction(container, 42); return err }(),
		"nil function":  func() error { _, err := web.NewControllerAction(container, nilAction); return err }(),
		"no controller": func() error {
			_, err := web.NewControllerAction(container, func(*web.Context) error { return nil })
			return err
		}(),
		"no context": func() error {
			_, err := web.NewControllerAction(container, func(*testController, string) error { return nil })
			return err
		}(),
		"invalid result": func() error {
			_, err := web.NewControllerAction(container, func(*testController, *web.Context) {})
			return err
		}(),
		"duplicate context": func() error {
			_, err := web.NewControllerAction(container, func(*testController, *web.Context, *web.Context) error { return nil })
			return err
		}(),
	} {
		if err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}
}
