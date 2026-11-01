package app_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/di"
	"github.com/chitushka/framework/event"
)

type testModule struct {
	id          string
	log         *[]string
	registerErr error
	bootErr     error
	shutdownErr error
}

func (m *testModule) ID() string { return m.id }
func (m *testModule) Register(*di.Container) error {
	*m.log = append(*m.log, "register:"+m.id)
	return m.registerErr
}
func (m *testModule) Boot(context.Context, *app.Application) error {
	*m.log = append(*m.log, "boot:"+m.id)
	return m.bootErr
}
func (m *testModule) Shutdown(context.Context) error {
	*m.log = append(*m.log, "shutdown:"+m.id)
	return m.shutdownErr
}

func TestApplicationLifecycle(t *testing.T) {
	var log []string
	application := app.New(nil, nil)
	_ = application.AddModule(&testModule{id: "one", log: &log})
	_ = application.AddModule(&testModule{id: "two", log: &log})
	if err := application.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !application.Running() {
		t.Fatal("приложение не отмечено запущенным")
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"register:one", "register:two", "boot:one", "boot:two", "shutdown:two", "shutdown:one"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("порядок lifecycle: %v", log)
	}
}

func TestApplicationRollsBackBootedModules(t *testing.T) {
	var log []string
	wantErr := errors.New("boot error")
	application := app.New(nil, nil)
	_ = application.AddModule(&testModule{id: "one", log: &log})
	_ = application.AddModule(&testModule{id: "two", log: &log, bootErr: wantErr})
	err := application.Start(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("получена ошибка %v", err)
	}
	want := []string{"register:one", "register:two", "boot:one", "boot:two", "shutdown:one"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("откат lifecycle: %v", log)
	}
}

func TestApplicationRejectsInvalidAndDuplicateModules(t *testing.T) {
	var log []string
	application := app.New(nil, nil)
	if err := application.AddModule(nil); err == nil {
		t.Fatal("ожидалась ошибка nil-модуля")
	}
	module := &testModule{id: "one", log: &log}
	if err := application.AddModule(module); err != nil {
		t.Fatal(err)
	}
	if err := application.AddModule(module); err == nil {
		t.Fatal("ожидалась ошибка дубликата")
	}
	if err := application.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := application.AddModule(&testModule{id: "late", log: &log}); err == nil {
		t.Fatal("ожидался запрет позднего модуля")
	}
	if err := application.Start(context.Background()); err == nil {
		t.Fatal("ожидался запрет повторного запуска")
	}
}

func TestApplicationPropagatesRegisterAndShutdownErrors(t *testing.T) {
	var log []string
	registerErr := errors.New("register error")
	application := app.New(nil, nil)
	_ = application.AddModule(&testModule{id: "bad", log: &log, registerErr: registerErr})
	if err := application.Start(context.Background()); !errors.Is(err, registerErr) {
		t.Fatalf("получена ошибка %v", err)
	}

	shutdownErr := errors.New("shutdown error")
	application = app.New(nil, nil)
	_ = application.AddModule(&testModule{id: "bad", log: &log, shutdownErr: shutdownErr})
	if err := application.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := application.Shutdown(context.Background()); !errors.Is(err, shutdownErr) {
		t.Fatalf("получена ошибка %v", err)
	}
}

func TestLifecycleHandlerCanInspectApplication(t *testing.T) {
	application := app.New(nil, nil)
	_, _ = application.Events.Subscribe(app.EventBeforeStart, 0, func(context.Context, event.Event) error {
		_ = application.Running()
		return nil
	})
	if err := application.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}
