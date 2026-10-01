package toolvalidation

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// This is a fail-closed capability policy, not a replacement schema validator.
// Both deployed draft-07 schemas and modern draft 2020-12 use the mature compiler.
func checkSchema(doc any) error {
	var declared any
	if object, ok := doc.(map[string]any); ok {
		declared = object["$schema"]
	}
	return checkSchemaDialectRoot(doc, "2020-12", declared, true)
}

func checkSchemaDialect(doc any, dialect string) error {
	return checkSchemaDialectRoot(doc, dialect, nil, false)
}

func checkSchemaDialectRoot(doc any, dialect string, rootDeclaration any, root bool) error {
	if _, ok := doc.(bool); ok {
		return nil
	}
	object, ok := doc.(map[string]any)
	if !ok {
		return errors.New("input schema must be an object or boolean")
	}
	if declared, exists := object["$schema"]; exists {
		if !root {
			return errors.New("nested input schema dialect changes are unsupported")
		}
		value, ok := declared.(string)
		if !ok {
			return errors.New("invalid input schema dialect")
		}
		switch strings.TrimSuffix(value, "#") {
		case "https://json-schema.org/draft/2020-12/schema", "http://json-schema.org/draft/2020-12/schema":
			dialect = "2020-12"
		case "https://json-schema.org/draft-07/schema", "http://json-schema.org/draft-07/schema":
			dialect = "draft7"
		default:
			return errors.New("unsupported input schema dialect; use draft-07 or 2020-12")
		}
	}
	for keyword, value := range object {
		// The compiler stores length/count constraints in native ints. Reject
		// overflow here instead of letting an assertion become unconstrained.
		switch keyword {
		case "minProperties", "maxProperties", "minItems", "maxItems", "minContains", "maxContains", "minLength", "maxLength":
			number, ok := value.(json.Number)
			if !ok {
				return errors.New("invalid schema count constraint")
			}
			n, err := strconv.ParseUint(number.String(), 10, 31)
			if err != nil || n > 1<<30 {
				return errors.New("schema count constraint exceeds supported range")
			}
		}
		if dialect == "draft7" {
			switch keyword {
			case "$defs", "$anchor", "dependentRequired", "dependentSchemas", "minContains", "maxContains", "unevaluatedProperties", "unevaluatedItems", "prefixItems":
				return errors.New("input schema keyword requires draft 2020-12")
			}
		} else {
			switch keyword {
			case "definitions", "dependencies", "additionalItems":
				return errors.New("input schema keyword requires draft-07")
			}
		}
		switch keyword {
		case "$schema":
		case "$ref":
			ref, ok := value.(string)
			if !ok || !strings.HasPrefix(ref, "#") {
				return errors.New("external input schema references are forbidden")
			}
		case "$id", "$anchor", "$comment", "title", "description", "default", "examples", "readOnly", "writeOnly", "deprecated",
			"type", "enum", "const", "required", "dependentRequired", "minProperties", "maxProperties",
			"minItems", "maxItems", "uniqueItems", "minContains", "maxContains", "minLength", "maxLength", "pattern",
			"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf":
		case "format":
			format, ok := value.(string)
			if !ok || !supportedFormat(format) {
				return errors.New("unsupported input schema format")
			}
		case "$defs", "definitions", "properties", "patternProperties", "dependentSchemas":
			entries, ok := value.(map[string]any)
			if !ok {
				return errors.New("invalid input schema map")
			}
			for _, entry := range entries {
				if err := checkSchemaDialect(entry, dialect); err != nil {
					return err
				}
			}
		case "dependencies":
			entries, ok := value.(map[string]any)
			if !ok {
				return errors.New("invalid dependencies")
			}
			for _, entry := range entries {
				if _, array := entry.([]any); !array {
					if err := checkSchemaDialect(entry, dialect); err != nil {
						return err
					}
				}
			}
		case "items":
			if entries, array := value.([]any); array {
				if dialect != "draft7" {
					return errors.New("tuple items require draft-07")
				}
				for _, entry := range entries {
					if err := checkSchemaDialect(entry, dialect); err != nil {
						return err
					}
				}
			} else if err := checkSchemaDialect(value, dialect); err != nil {
				return err
			}
		case "contains", "additionalItems", "additionalProperties", "propertyNames", "unevaluatedProperties", "unevaluatedItems", "not", "if", "then", "else":
			if err := checkSchemaDialect(value, dialect); err != nil {
				return err
			}
		case "allOf", "anyOf", "oneOf", "prefixItems":
			entries, ok := value.([]any)
			if !ok {
				return errors.New("invalid input schema array")
			}
			for _, entry := range entries {
				if err := checkSchemaDialect(entry, dialect); err != nil {
					return err
				}
			}
		default:
			return errors.New("unsupported input schema keyword")
		}
	}
	return nil
}

func supportedFormat(format string) bool {
	switch format {
	case "json-pointer", "relative-json-pointer", "uuid", "duration", "period", "ipv4", "ipv6", "hostname", "email", "date", "time", "date-time", "uri", "iri", "uri-reference", "iri-reference", "uri-template", "semver", "regex":
		return true
	}
	return false
}

type noExternalLoader struct{}

func (noExternalLoader) Load(string) (any, error) {
	return nil, errors.New("external input schema resources are forbidden")
}
