package toolvalidation

import (
	"encoding/json"
	"testing"
)

func TestValidateDraft7Dialect(t *testing.T) {
	schema := json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","definitions":{"id":{"type":"integer"}},"properties":{"id":{"$ref":"#/definitions/id"},"tuple":{"type":"array","items":[{"type":"string"},{"type":"integer"}],"additionalItems":false}},"required":["id"]}`)
	if err := Validate(schema, json.RawMessage(`{"id":1,"tuple":["x",2]}`)); err != nil {
		t.Fatal(err)
	}
	if err := Validate(schema, json.RawMessage(`{"id":"bad"}`)); err == nil {
		t.Fatal("draft7 type not enforced")
	}
	// Modern-only keywords must not be silently ignored by a draft7 compiler.
	modernInOld := json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","unevaluatedProperties":false}`)
	if err := Validate(modernInOld, json.RawMessage(`{}`)); err == nil {
		t.Fatal("ignored modern keyword accepted in draft7")
	}
}
