package toolvalidation

import (
	"encoding/json"
	"os"
	"testing"
)

func TestLiveSchemaCompatibility(t *testing.T) {
	path := os.Getenv("MCP_SCHEMA_AUDIT_FILE")
	if path == "" {
		t.Skip("set MCP_SCHEMA_AUDIT_FILE for read-only production schema audit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tools []struct {
		Name        string          `json:"name"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal(data, &tools); err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		doc, _, err := decodeJSON(tool.InputSchema, maxSchemaBytes, maxSchemaNodes)
		if err != nil {
			t.Errorf("%s: %v", tool.Name, err)
			continue
		}
		if err := checkSchema(doc); err != nil {
			t.Errorf("%s: %v", tool.Name, err)
			continue
		}
		if _, err := schemaWorkBounds(doc); err != nil {
			t.Errorf("%s: %v", tool.Name, err)
		}
	}
	t.Logf("audited %d real tool schemas", len(tools))
}

func TestValidateExactLargeIntegerAndComposition(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","$defs":{"id":{"type":"integer","const":9007199254740993}},"properties":{"id":{"$ref":"#/$defs/id"},"values":{"type":"array","items":{"anyOf":[{"enum":["ok"]},{"type":"integer","minimum":1}]}}},"required":["id","values"],"additionalProperties":false}`)
	if err := Validate(schema, json.RawMessage(`{"id":9007199254740993,"values":["ok",2]}`)); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`{"id":9007199254740992,"values":["ok"]}`, `{"id":9007199254740993,"values":[0]}`} {
		if err := Validate(schema, json.RawMessage(value)); err == nil {
			t.Fatalf("precision/composition invalid value admitted: %s", value)
		}
	}
}
