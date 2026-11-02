package console

import (
	"bytes"
	"context"
	"errors"
	"testing"

	frameworkapp "github.com/chitushka/framework/app"
)

type testModule struct{ registered, booted, stopped bool }

func (module *testModule) ID() string { return "test" }
func (module *testModule) Register(application *frameworkapp.Application) error {
	module.registered = true
	return application.Components.Instance("shared", "value")
}
func (module *testModule) Boot(context.Context, *frameworkapp.Application) error {
	module.booted = true
	return nil
}
func (module *testModule) Shutdown(context.Context) error { module.stopped = true; return nil }

func TestApplicationRunsCustomCommandWithSharedComponents(t *testing.T) {
	core := frameworkapp.New(nil, nil)
	module := &testModule{}
	if err := core.AddModule(module); err != nil {
		t.Fatal(err)
	}
	application := New(core)
	var output bytes.Buffer
	application.Out = &output
	application.Err = &output
	command := CommandFunc{CommandName: "demo:run", CommandDescription: "demo command", Execute: func(ctx context.Context, console *Context, args []string) error {
		value, err := console.Application.Components.Get(ctx, "shared")
		if err != nil {
			return err
		}
		if value != "value" || len(args) != 1 || args[0] != "arg" {
			return errors.New("unexpected command context")
		}
		_, _ = console.Out.Write([]byte("done"))
		return nil
	}}
	if err := application.Register(command); err != nil {
		t.Fatal(err)
	}
	if err := application.Run(context.Background(), []string{"demo:run", "arg"}); err != nil {
		t.Fatal(err)
	}
	if output.String() != "done" || !module.registered || !module.booted || !module.stopped {
		t.Fatalf("output=%q module=%#v", output.String(), module)
	}
}

func TestHelpAndValidation(t *testing.T) {
	application := New(nil)
	var output bytes.Buffer
	application.Out = &output
	application.Err = &output
	if err := application.Register(CommandFunc{CommandName: "z:last", CommandDescription: "last", Execute: func(context.Context, *Context, []string) error { return nil }}, CommandFunc{CommandName: "a:first", CommandDescription: "first", Execute: func(context.Context, *Context, []string) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	if err := application.Run(context.Background(), []string{"help"}); err != nil {
		t.Fatal(err)
	}
	if first, last := stringsIndex(output.String(), "a:first"), stringsIndex(output.String(), "z:last"); first < 0 || last < first {
		t.Fatalf("help = %q", output.String())
	}
	if err := application.Register(CommandFunc{CommandName: "a:first"}); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := application.Run(context.Background(), []string{"missing"}); err == nil {
		t.Fatal("unknown command accepted")
	}
	if err := (*Application)(nil).Run(context.Background(), nil); err == nil {
		t.Fatal("nil application accepted")
	}
}
func stringsIndex(value, substring string) int {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return index
		}
	}
	return -1
}
