package migration

import (
	"errors"
	"io"
	"testing"
)

type brokenOutput struct{}

func (brokenOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCreatePropagatesOutputError(t *testing.T) {
	cli := NewCLI()
	cli.Out = brokenOutput{}
	if err := cli.Run(t.Context(), []string{"migration:create", "-dir", t.TempDir(), "output_failure"}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output failure hidden: %v", err)
	}
}
