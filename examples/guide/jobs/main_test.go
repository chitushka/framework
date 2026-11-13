package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestJobCompletesAndCancellationIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := process(ctx, t.TempDir(), &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "processed monthly-report\n" {
		t.Fatal(output.String())
	}
	ctx, cancel = context.WithCancel(t.Context())
	cancel()
	if err := process(ctx, t.TempDir(), &output); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if strings.Count(output.String(), "processed") != 1 {
		t.Fatal("cancelled producer executed work")
	}
}
