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
	"golang.org/x/text/cases"
)

const (
	maxJSONBytes = 4 << 20
	maxJSONDepth = 100
)

var unicodeFold = cases.Fold()

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
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	if err := rejectMismatchedJSONFields(data, reflect.TypeOf(out)); err != nil {
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

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, 0); err != nil {
		return err
	}
	if token, err := decoder.Token(); err == nil {
		return fmt.Errorf("unexpected trailing token %v", token)
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d", maxJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			folded := unicodeFold.String(key)
			if seen[folded] {
				return fmt.Errorf("duplicate JSON key %q (keys must not differ only by case)", key)
			}
			seen[folded] = true
			if err := scanJSONValue(decoder, depth+1); err != nil {
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
			if err := scanJSONValue(decoder, depth+1); err != nil {
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

func rejectMismatchedJSONFields(data []byte, target reflect.Type) error {
	if target == nil {
		return errors.New("decode target is nil")
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	return checkJSONFieldNames(json.RawMessage(data), target)
}

func checkJSONFieldNames(raw json.RawMessage, target reflect.Type) error {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return err
		}
		fields := make(map[string]reflect.Type, target.NumField())
		for i := 0; i < target.NumField(); i++ {
			field := target.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" {
				name = field.Name
			}
			if name != "-" {
				fields[name] = field.Type
			}
		}
		for name, value := range object {
			fieldType, ok := fields[name]
			if !ok {
				return fmt.Errorf("unknown field %q", name)
			}
			if err := checkJSONFieldNames(value, fieldType); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	case reflect.Slice, reflect.Array:
		if target.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for i, value := range values {
			if err := checkJSONFieldNames(value, target.Elem()); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for name, value := range values {
			if err := checkJSONFieldNames(value, target.Elem()); err != nil {
				return fmt.Errorf("%s: %w", name, err)
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
