package agentsuite

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
)

const (
	maxJSONBytes           = 4 << 20
	maxJSONDepth           = 100
	maxJSONObjectMembers   = 1024
	maxJSONDocumentMembers = 100_000
)

func decodeStrict(data []byte, out any) error {
	if len(data) == 0 {
		return errors.New("document is empty")
	}
	if len(data) > maxJSONBytes {
		return fmt.Errorf("document exceeds %d bytes", maxJSONBytes)
	}
	if !utf8.Valid(data) {
		return errors.New("document is not valid UTF-8")
	}
	if err := validateJSONTokens(data); err != nil {
		return err
	}
	canonical, err := jcs.Transform(data)
	if err != nil {
		return fmt.Errorf("document is not valid JCS JSON: %w", err)
	}
	if err := validateCanonicalEquivalence(data, canonical); err != nil {
		return err
	}
	if err := validateExactFields(data, reflect.TypeOf(out)); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("document contains multiple JSON values")
		}
		return fmt.Errorf("document has invalid trailing data: %w", err)
	}
	return nil
}

func validateJSONTokens(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	members := 0
	if err := scanJSONValue(decoder, 0, &members); err != nil {
		return err
	}
	if token, err := decoder.Token(); err == nil {
		return fmt.Errorf("unexpected trailing token %v", token)
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int, documentMembers *int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		if value, ok := token.(string); ok {
			return validateJSONString(value)
		}
		return nil
	}
	if depth >= maxJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d", maxJSONDepth)
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			if len(seen) >= maxJSONObjectMembers || *documentMembers >= maxJSONDocumentMembers {
				return errors.New("JSON object member limit exceeded")
			}
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if err := validateJSONString(key); err != nil {
				return fmt.Errorf("JSON object key: %w", err)
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			*documentMembers++
			if err := scanJSONValue(decoder, depth+1, documentMembers); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1, documentMembers); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return nil
}

func validateJSONString(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("JSON string is not valid Unicode")
	}
	return nil
}

func validateCanonicalEquivalence(data, canonical []byte) error {
	original, err := decodeJSONValue(data)
	if err != nil {
		return err
	}
	transformed, err := decodeJSONValue(canonical)
	if err != nil {
		return err
	}
	if !sameJSONValue(original, transformed) {
		return errors.New("document value changes during JCS canonicalization")
	}
	return nil
}

func decodeJSONValue(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func sameJSONValue(a, b any) bool {
	switch a := a.(type) {
	case nil:
		return b == nil
	case bool:
		other, ok := b.(bool)
		return ok && a == other
	case string:
		other, ok := b.(string)
		return ok && a == other
	case json.Number:
		other, ok := b.(json.Number)
		if !ok {
			return false
		}
		if left, err := a.Int64(); err == nil {
			right, err := other.Int64()
			return err == nil && left == right
		}
		left, leftErr := a.Float64()
		right, rightErr := other.Float64()
		return leftErr == nil && rightErr == nil && left == right
	case []any:
		other, ok := b.([]any)
		if !ok || len(a) != len(other) {
			return false
		}
		for i := range a {
			if !sameJSONValue(a[i], other[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		other, ok := b.(map[string]any)
		if !ok || len(a) != len(other) {
			return false
		}
		for key, value := range a {
			otherValue, ok := other[key]
			if !ok || !sameJSONValue(value, otherValue) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

var rawMessageType = reflect.TypeFor[json.RawMessage]()

func validateExactFields(data []byte, target reflect.Type) error {
	if target == nil {
		return nil
	}
	return validateJSONType(json.RawMessage(data), target)
}

func validateJSONType(raw json.RawMessage, target reflect.Type) error {
	wasPointer := false
	for target.Kind() == reflect.Pointer {
		wasPointer = true
		target = target.Elem()
	}
	if target == rawMessageType || target.Kind() == reflect.Interface {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if wasPointer || target.Kind() == reflect.Map || target.Kind() == reflect.Slice {
			return nil
		}
		return errors.New("null is not allowed")
	}
	switch target.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return err
		}
		fields := make(map[string]reflect.Type)
		for _, field := range reflect.VisibleFields(target) {
			if !field.IsExported() {
				continue
			}
			name := field.Name
			if tag, ok := field.Tag.Lookup("json"); ok {
				name = strings.Split(tag, ",")[0]
				if name == "-" {
					continue
				}
				if name == "" {
					name = field.Name
				}
			}
			fields[name] = field.Type
		}
		for name, value := range object {
			fieldType, ok := fields[name]
			if !ok {
				return fmt.Errorf("unknown field %q", name)
			}
			if err := validateJSONType(value, fieldType); err != nil {
				return fmt.Errorf("field %q: %w", name, err)
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for i, value := range values {
			if err := validateJSONType(value, target.Elem()); err != nil {
				return fmt.Errorf("element %d: %w", i, err)
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for name, value := range values {
			if err := validateJSONType(value, target.Elem()); err != nil {
				return fmt.Errorf("map value %q: %w", name, err)
			}
		}
	}
	return nil
}

func canonicalDigest(data []byte) (string, error) {
	canonical, err := jcs.Transform(data)
	if err != nil {
		return "", fmt.Errorf("canonicalize JSON: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return false
		}
	}
	return true
}
