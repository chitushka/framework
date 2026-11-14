package performance

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestFixturesAgree(t *testing.T) {
	for _, variant := range []string{"NetHTTP", "Chitu"} {
		handler, err := Routing(variant, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, route := range []string{"0", "499", "999"} {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest("GET", "http://localhost/items"+route+"/42", nil))
			var payload Payload
			if recorder.Code != 200 || recorder.Header().Get("Content-Type") != "application/json; charset=utf-8" || json.Unmarshal(recorder.Body.Bytes(), &payload) != nil || payload != (Payload{"42", Name()}) {
				t.Fatalf("%s routing differs: %d %s", variant, recorder.Code, recorder.Body)
			}
		}
	}
	for _, singleton := range []bool{false, true} {
		manual, framework, err := DI(singleton)
		if err != nil {
			t.Fatal(err)
		}
		for _, resolve := range []Resolve{manual, framework} {
			first, err := resolve()
			if err != nil || first.Repository.Config.Label != "catalog" {
				t.Fatal("invalid DI graph", err)
			}
			second, err := resolve()
			if err != nil || (first == second) != singleton {
				t.Fatal("DI lifetime differs", err)
			}
		}
	}
	manual, framework, err := Rendering()
	if err != nil {
		t.Fatal(err)
	}
	one, err := manual()
	if err != nil {
		t.Fatal(err)
	}
	two, err := framework()
	if err != nil || !bytes.Equal(one, two) || !strings.Contains(string(two), "&lt;item &amp;&gt;") || strings.Count(string(two), "<p data-id=") != 20 {
		t.Fatal("view/layout data or escaping differs", err)
	}
	for _, dialect := range []string{"postgresql", "mysql"} {
		manual, framework, err := SQLBuild(dialect)
		if err != nil {
			t.Fatal(err)
		}
		got, err := framework()
		if err != nil || !reflect.DeepEqual(got, manual()) || len(got.Args) != 2 || strconv.Itoa(got.Args[0].(int)) != "42" {
			t.Fatalf("SQL fixture differs for %s: %+v %v", dialect, got, err)
		}
	}
}
