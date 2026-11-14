package db

import (
	"reflect"
	"testing"
)

func FuzzQueryParameters(f *testing.F) {
	f.Add("users", "name", "' OR 1=1; --")
	f.Add("schema.users", "u.name", "\x00?$")
	f.Add("users\x00", "*", "")
	f.Add("a\"; DROP TABLE users;--", "a`b]", "value")
	f.Fuzz(func(t *testing.T, table, column, value string) {
		if len(table)+len(column)+len(value) > 4096 {
			t.Skip()
		}
		for _, dialect := range BuiltinDialects() {
			base := NewQueryBuilder(dialect).Select("id").From(table)
			query, args, err := base.Where(Eq(column, value)).Build()
			if err != nil {
				if query != "" || len(args) != 0 {
					t.Fatal("failed build exposed partial SQL")
				}
				continue
			}
			other, otherArgs, err := base.Where(Eq(column, value+"'?")).Build()
			if err != nil || query != other || !reflect.DeepEqual(args, []any{value}) || !reflect.DeepEqual(otherArgs, []any{value + "'?"}) {
				t.Fatalf("%s interpolated a bound value or mutated a query branch", dialect.Name())
			}
			_, baseArgs, err := base.Build()
			if err != nil || len(baseArgs) != 0 {
				t.Fatal("building a branch mutated its base")
			}
		}
	})
}

func FuzzRawPlaceholders(f *testing.F) {
	f.Add("x = ? AND y = ??", uint8(1))
	f.Add("???", uint8(1))
	f.Add("", uint8(0))
	f.Add("x = ?", uint8(0))
	f.Fuzz(func(t *testing.T, fragment string, count uint8) {
		if len(fragment) > 4096 {
			t.Skip()
		}
		args := make([]any, int(count)%16)
		for i := range args {
			args[i] = i
		}
		for _, dialect := range BuiltinDialects() {
			builder := NewQueryBuilder(dialect).Select("id").Where(RawCondition(fragment, args...))
			query, got, err := builder.Build()
			if err != nil {
				if query != "" || len(got) != 0 {
					t.Fatal("placeholder error returned partial output")
				}
				continue
			}
			if len(got) != len(args) {
				t.Fatal("placeholder count differs from bound arguments")
			}
			again, next, err := builder.Build()
			if err != nil || query != again || !reflect.DeepEqual(got, next) {
				t.Fatal("placeholder parsing is not repeatable")
			}
		}
	})
}
