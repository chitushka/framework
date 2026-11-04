package db

import (
	"reflect"
	"strings"
	"testing"
)

func TestSecurityAuditIdentifiersAndValuesRemainSeparate(t *testing.T) {
	for _, dialect := range BuiltinDialects() {
		t.Run(dialect.Name(), func(t *testing.T) {
			identifier := `user"` + "`" + `]; DELETE FROM secrets --`
			value := `'; DROP TABLE users; --`
			query, args, err := NewQueryBuilder(dialect).Select(identifier).From("users").Where(Eq("name", value)).Build()
			if err != nil {
				t.Fatal(err)
			}
			var quoted string
			switch dialect.Name() {
			case PostgreSQL, Oracle:
				quoted = `"user""` + "`" + `]; DELETE FROM secrets --"`
			case MySQL:
				quoted = "`user\"``]; DELETE FROM secrets --`"
			case SQLServer:
				quoted = "[user\"`]]; DELETE FROM secrets --]"
			}
			want := "SELECT " + quoted + " FROM " + dialect.QuoteIdentifier("users") + " WHERE " + dialect.QuoteIdentifier("name") + " = " + dialect.Placeholder(1)
			if query != want || !reflect.DeepEqual(args, []any{value}) || strings.Contains(query, value) {
				t.Fatalf("identifier/value boundary failed: %s %v", query, args)
			}
			for _, bad := range []string{"name\x00secret", "schema..table", "*.name"} {
				if _, _, err := NewQueryBuilder(dialect).Select(bad).From("users").Build(); err == nil {
					t.Fatalf("invalid identifier accepted: %q", bad)
				}
			}
			if _, _, err := NewQueryBuilder(dialect).Select("id").From("users").Where(RawCondition("id = ?", 1, 2)).Build(); err == nil {
				t.Fatal("raw placeholder mismatch accepted")
			}
		})
	}
}
