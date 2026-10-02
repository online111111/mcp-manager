// Package toolvalidation validates progressive tool arguments without modifying
// the raw JSON sent to the downstream. It never fetches schema resources.
package toolvalidation

import (
	"encoding/json"
	"errors"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Validate checks arguments against a tool's input schema. The caller retains
// ownership of arguments; validation never applies defaults or rewrites JSON.
func Validate(inputSchema any, arguments json.RawMessage) error {
	raw, err := json.Marshal(inputSchema)
	if err != nil {
		return errors.New("input schema cannot be encoded")
	}
	doc, _, err := decodeJSON(raw, maxSchemaBytes, maxSchemaNodes)
	if err != nil {
		return errors.New("input schema is not valid JSON")
	}
	if err := checkSchema(doc); err != nil {
		return err
	}
	work, err := schemaWorkBounds(doc)
	if err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(noExternalLoader{})
	compiler.AssertFormat()
	const location = "https://mcp-manager.invalid/input-schema"
	if err := compiler.AddResource(location, doc); err != nil {
		return errors.New("input schema cannot be registered")
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		return errors.New("input schema is invalid or unsupported")
	}
	instance, instanceNodes, err := decodeJSON(arguments, maxArgumentBytes, maxArgumentNodes)
	if err != nil {
		return errors.New("arguments must be a single unambiguous JSON object within resource limits")
	}
	if _, ok := instance.(map[string]any); !ok {
		return errors.New("arguments must be a single JSON object")
	}
	if work*instanceNodes > maxValidationWork {
		return errors.New("input schema and arguments exceed validation work limit")
	}
	if err := schema.Validate(instance); err != nil {
		return errors.New("arguments do not match input schema")
	}
	return nil
}
