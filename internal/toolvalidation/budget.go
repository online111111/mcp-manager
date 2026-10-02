package toolvalidation

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

const maxExpandedSchemas = 4096
const maxValidationWork = 1 << 20

// schemaWorkBounds walks the admitted schema graph, not the instance. It rejects
// recursive and exponentially expanded reference graphs before invoking the
// compiler. References into annotation data are not admitted as schemas.
func schemaWorkBounds(doc any) (int, error) {
	schemas := map[string]any{}
	anchors := map[string]string{}
	var collect func(string, any) error
	collect = func(path string, value any) error {
		schemas[path] = value
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		if path != "" {
			if _, ok := object["$id"]; ok {
				return errors.New("nested schema resource IDs are unsupported")
			}
		}
		if anchor, ok := object["$anchor"].(string); ok {
			if _, exists := anchors[anchor]; exists {
				return errors.New("duplicate schema anchor")
			}
			anchors[anchor] = path
		}
		for childPath, child := range schemaChildren(path, object) {
			if err := collect(childPath, child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect("", doc); err != nil {
		return 0, err
	}
	states := map[string]int{}
	costs := map[string]int{}
	var visit func(string) (int, error)
	visit = func(path string) (int, error) {
		if states[path] == 1 {
			return 0, errors.New("recursive input schemas are unsupported")
		}
		if states[path] == 2 {
			return costs[path], nil
		}
		states[path] = 1
		cost := 1
		if object, ok := schemas[path].(map[string]any); ok {
			if ref, ok := object["$ref"].(string); ok {
				target, err := url.PathUnescape(strings.TrimPrefix(ref, "#"))
				if err != nil {
					return 0, errors.New("invalid local schema reference")
				}
				if target != "" && !strings.HasPrefix(target, "/") {
					var exists bool
					target, exists = anchors[target]
					if !exists {
						return 0, errors.New("unknown local schema anchor")
					}
				}
				if _, exists := schemas[target]; !exists {
					return 0, errors.New("local reference must target an admitted subschema")
				}
				refCost, err := visit(target)
				if err != nil {
					return 0, err
				}
				cost += refCost
			}
			for childPath := range schemaChildren(path, object) {
				childCost, err := visit(childPath)
				if err != nil {
					return 0, err
				}
				cost += childCost
				if cost > maxExpandedSchemas {
					return 0, errors.New("input schema expansion limit exceeded")
				}
			}
		}
		if cost > maxExpandedSchemas {
			return 0, errors.New("input schema expansion limit exceeded")
		}
		states[path] = 2
		costs[path] = cost
		return cost, nil
	}
	return visit("")
}

func schemaChildren(path string, object map[string]any) map[string]any {
	children := map[string]any{}
	for keyword, value := range object {
		prefix := path + "/" + keyword
		switch keyword {
		case "$defs", "definitions", "properties", "patternProperties", "dependentSchemas":
			for key, child := range value.(map[string]any) {
				escaped := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
				children[prefix+"/"+escaped] = child
			}
		case "dependencies":
			for key, child := range value.(map[string]any) {
				if _, array := child.([]any); !array {
					escaped := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
					children[prefix+"/"+escaped] = child
				}
			}
		case "items":
			if entries, array := value.([]any); array {
				for index, child := range entries {
					children[prefix+"/"+strconv.Itoa(index)] = child
				}
			} else {
				children[prefix] = value
			}
		case "contains", "additionalItems", "additionalProperties", "propertyNames", "unevaluatedProperties", "unevaluatedItems", "not", "if", "then", "else":
			children[prefix] = value
		case "allOf", "anyOf", "oneOf", "prefixItems":
			for index, child := range value.([]any) {
				children[prefix+"/"+strconv.Itoa(index)] = child
			}
		}
	}
	return children
}
