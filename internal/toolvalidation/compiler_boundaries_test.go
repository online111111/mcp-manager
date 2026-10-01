package toolvalidation

import (
	"encoding/json"
	"testing"
)

func TestValidationRejectsCompilerNumericOverflowAndNestedDialects(t *testing.T) {
	cases := []struct{ schema, args string }{
		{`{"type":"object","properties":{"x":{"type":"string","minLength":18446744073709551616}}}`, `{"x":""}`},
		{`{"type":"object","properties":{"x":{"type":"array","minItems":18446744073709551616}}}`, `{"x":[]}`},
		{`{"type":"object","minProperties":18446744073709551616}`, `{}`},
		{`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"x":{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","dependentRequired":{"a":["b"]}}}}`, `{"x":{"a":1}}`},
	}
	for _, c := range cases {
		if err := Validate(json.RawMessage(c.schema), json.RawMessage(c.args)); err == nil {
			t.Fatalf("schema silently ignored an assertion: %s", c.schema)
		}
	}
}
