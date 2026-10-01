package toolvalidation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateSingleObject(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	for _, args := range []string{`{} {}`, `{} null`, `null`, `[]`, `{"key":1,"key":2}`} {
		if err := Validate(schema, json.RawMessage(args)); err == nil {
			t.Fatalf("accepted non-single/ambiguous object %s", args)
		}
	}
}

func TestValidateRejectsUnsupportedSchemas(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","propertiez":{"x":{"type":"string"}}}`,
		`{"type":"object","$schema":"https://example.com/custom-schema"}`,
		`{"type":"object","properties":{"x":{"format":"unknown-custom"}}}`,
		`{"type":"object","properties":{"x":{"contentEncoding":"base64"}}}`,
		`{"type":"object","properties":{"x":{"$ref":"https://example.com/schema"}}}`,
		`{"type":"object","properties":{"x":{"$ref":"file:///etc/passwd"}}}`,
		`{"type":"object","$defs":{"unused":{"type":"object","unknownKeyword":true}}}`,
	} {
		if err := Validate(json.RawMessage(schema), json.RawMessage(`{}`)); err == nil {
			t.Fatalf("accepted unsupported schema %s", schema)
		}
	}
}

func TestValidateResourceBounds(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	for _, args := range []string{
		`{"n":` + strings.Repeat("1", 300) + `}`,
		`{"n":1e9999}`,
		`{"x":"` + strings.Repeat("x", 1048577) + `"}`,
		`{"x":` + strings.Repeat("[", 70) + `0` + strings.Repeat("]", 70) + `}`,
		`{"x":[` + strings.Repeat(`0,`, 20000) + `0]}`,
	} {
		if err := Validate(schema, json.RawMessage(args)); err == nil {
			t.Fatalf("accepted arguments beyond resource bounds (len=%d)", len(args))
		}
	}
	largeSchema := json.RawMessage(`{"type":"object","description":"` + strings.Repeat("s", 262145) + `"}`)
	if err := Validate(largeSchema, json.RawMessage(`{}`)); err == nil {
		t.Fatal("accepted oversized schema")
	}
}

func TestValidateBoundsExpandedReferences(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","$defs":{"a":{"$ref":"#/$defs/a"}},"properties":{"x":{"$ref":"#/$defs/a"}}}`,
		`{"type":"object","default":{"unknownKeyword":true},"properties":{"x":{"$ref":"#/default"}}}`,
	} {
		if err := Validate(json.RawMessage(schema), json.RawMessage(`{}`)); err == nil {
			t.Fatalf("accepted unbounded/unvetted schema %s", schema)
		}
	}
}

func TestValidateRequiredAndType(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"],"additionalProperties":false}`)
	for _, args := range []string{`{}`, `{"count":"2"}`, `{"count":2,"extra":true}`} {
		if err := Validate(schema, json.RawMessage(args)); err == nil {
			t.Fatalf("accepted invalid arguments %s", args)
		}
	}
	if err := Validate(schema, json.RawMessage(`{"count":2}`)); err != nil {
		t.Fatal(err)
	}
}
