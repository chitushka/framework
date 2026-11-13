package console

import (
	"context"
	"errors"
	"io"
	"testing"
)

type brokenOutput struct{}

func (brokenOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestHelpPropagatesOutputErrors(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"help", "example"}} {
		application := New(nil)
		application.Out = brokenOutput{}
		if err := application.Register(CommandFunc{CommandName: "example", Execute: func(context.Context, *Context, []string) error { return nil }}); err != nil {
			t.Fatal(err)
		}
		if err := application.Run(t.Context(), args); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("output error lost: %v", err)
		}
	}
}
