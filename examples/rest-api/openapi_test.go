package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// Validate the JSON Schema keywords used by this starter against actual responses.
// This intentionally small checker is not a general OpenAPI validator.
func validateSchema(doc, schema map[string]any, value any) error {
	if ref, ok := schema["$ref"].(string); ok {
		var node any = doc
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			node = node.(map[string]any)[part]
		}
		return validateSchema(doc, node.(map[string]any), value)
	}
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("expected object: %v", value)
		}
		for _, name := range anySlice(schema["required"]) {
			if _, ok := object[name.(string)]; !ok {
				return fmt.Errorf("missing %s", name)
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for name, field := range object {
			if prop, ok := props[name]; ok {
				if err := validateSchema(doc, prop.(map[string]any), field); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
			} else if schema["additionalProperties"] == false {
				return fmt.Errorf("unknown field %s", name)
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("expected array")
		}
		if limit, ok := schema["maxItems"].(float64); ok && len(array) > int(limit) {
			return fmt.Errorf("array too long")
		}
		for _, item := range array {
			if err := validateSchema(doc, schema["items"].(map[string]any), item); err != nil {
				return err
			}
		}
	case "integer":
		n, ok := value.(float64)
		if !ok || n != float64(int64(n)) {
			return fmt.Errorf("expected integer")
		}
		if min, ok := schema["minimum"].(float64); ok && n < min {
			return fmt.Errorf("below minimum")
		}
		if max, ok := schema["maximum"].(float64); ok && n > max {
			return fmt.Errorf("above maximum")
		}
	case "string":
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected string")
		}
		if min, ok := schema["minLength"].(float64); ok && utf8.RuneCountInString(s) < int(min) {
			return fmt.Errorf("string too short")
		}
		if max, ok := schema["maxLength"].(float64); ok && utf8.RuneCountInString(s) > int(max) {
			return fmt.Errorf("string too long")
		}
	}
	if values, ok := schema["enum"].([]any); ok {
		for _, allowed := range values {
			if value == allowed {
				return nil
			}
		}
		return fmt.Errorf("value not in enum")
	}
	return nil
}

func anySlice(value any) []any { result, _ := value.([]any); return result }

func TestOpenAPIResponseContracts(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(openAPI, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatal("OpenAPI version")
	}
	h, token := testAPI(t)
	for _, tc := range []struct {
		method, path, pattern, body string
		status                      int
		anonymous                   bool
	}{
		{"GET", "/openapi.json", "/openapi.json", "", 200, true},
		{"GET", "/v1/articles?limit=1", "/v1/articles", "", 200, false},
		{"POST", "/v1/articles", "/v1/articles", `{"title":"Contract"}`, 201, false},
		{"GET", "/v1/articles/2", "/v1/articles/{id}", "", 200, false},
		{"PUT", "/v1/articles/2", "/v1/articles/{id}", `{"title":"Updated"}`, 200, false},
		{"DELETE", "/v1/articles/2", "/v1/articles/{id}", "", 204, false},
		{"GET", "/v1/articles/2", "/v1/articles/{id}", "", 404, false},
		{"GET", "/v1/articles", "/v1/articles", "", 401, true},
		{"POST", "/v1/articles", "/v1/articles", `{}`, 422, false},
		{"GET", "/v1/articles?sort=secret", "/v1/articles", "", 400, false},
	} {
		tokenValue := token
		if tc.anonymous {
			tokenValue = ""
		}
		w := call(h, tokenValue, tc.method, tc.path, tc.body)
		requireStatus(t, w, tc.status)
		operation := doc["paths"].(map[string]any)[tc.pattern].(map[string]any)[strings.ToLower(tc.method)].(map[string]any)
		response := operation["responses"].(map[string]any)[strconv.Itoa(tc.status)].(map[string]any)
		if ref, ok := response["$ref"].(string); ok {
			response = doc["components"].(map[string]any)["responses"].(map[string]any)[strings.TrimPrefix(ref, "#/components/responses/")].(map[string]any)
		}
		if tc.status == http.StatusNoContent {
			if w.Body.Len() != 0 {
				t.Fatal("204 body")
			}
			continue
		}
		var value any
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		if err := validateSchema(doc, schema, value); err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
	}
	if err := validateSchema(doc, doc["components"].(map[string]any)["schemas"].(map[string]any)["Article"].(map[string]any), map[string]any{"id": float64(0)}); err == nil {
		t.Fatal("schema checker accepted invalid article")
	}
}
