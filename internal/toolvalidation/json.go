package toolvalidation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

const (
	maxSchemaBytes    = 256 << 10
	maxArgumentBytes  = 1 << 20
	maxSchemaNodes    = 4096
	maxArgumentNodes  = 8192
	maxJSONDepth      = 64
	maxNumberBytes    = 256
	maxNumberExponent = 1024
)

// decodeJSON is a bounded JSON decoder, not a schema validator. It rejects
// duplicate keys and multiple top-level values, which downstream decoders can
// interpret differently. Numbers remain json.Number, including large integers.
func decodeJSON(raw []byte, maxBytes, maxNodes int) (any, int, error) {
	if len(raw) > maxBytes {
		return nil, 0, errors.New("JSON byte limit exceeded")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	nodes := 0
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		nodes++
		if nodes > maxNodes || depth > maxJSONDepth {
			return nil, errors.New("JSON complexity limit exceeded")
		}
		token, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch token {
		case json.Delim('{'):
			object := make(map[string]any)
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("invalid JSON object key")
				}
				if _, exists := object[key]; exists {
					return nil, errors.New("duplicate JSON object key")
				}
				value, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return object, nil
		case json.Delim('['):
			array := make([]any, 0)
			for dec.More() {
				value, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return array, nil
		default:
			if number, ok := token.(json.Number); ok {
				if err := checkNumber(number); err != nil {
					return nil, err
				}
			}
			return token, nil
		}
	}
	value, err := read(0)
	if err != nil {
		return nil, nodes, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, nodes, errors.New("JSON must contain a single value")
	}
	return value, nodes, nil
}

func checkNumber(number json.Number) error {
	text := number.String()
	if len(text) > maxNumberBytes {
		return errors.New("JSON number precision limit exceeded")
	}
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(text[i+1:])
		if err != nil || exponent > maxNumberExponent || exponent < -maxNumberExponent {
			return errors.New("JSON number exponent limit exceeded")
		}
	}
	return nil
}
