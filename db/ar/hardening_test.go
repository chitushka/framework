package ar

import (
	"errors"
	"math"
	"testing"
)

func TestVersionOverflowDoesNotWriteOrMutate(t *testing.T) {
	c, recorder := openARConnection(t)
	t.Cleanup(func() { _ = c.Close() })
	r, err := NewRepository[testUser](c, Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := &testUser{ID: 1, Version: math.MaxInt64}
	if err := r.Update(t.Context(), m); !errors.Is(err, ErrVersionOverflow) {
		t.Fatal(err)
	}
	if m.Version != math.MaxInt64 || len(recorder.execCalls) != 0 {
		t.Fatal("overflow mutated model/database")
	}
}

func TestRelationKeysUseSQLValue(t *testing.T) {
	a, b := int32(7), int64(7)
	if relationKey(&a) != relationKey(&b) || relationKey([]byte("x")) != relationKey("x") {
		t.Fatal("SQL-equivalent keys differ")
	}
}
