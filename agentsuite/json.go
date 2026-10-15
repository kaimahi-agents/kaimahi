package agentsuite

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
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

// Marshal serializes value as JSON Canonicalization Scheme (JCS) JSON.
func Marshal(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	canonical, err := jcs.Transform(data)
	if err != nil {
		return nil, fmt.Errorf("canonicalize JSON: %w", err)
	}
	if err := validateCanonicalEquivalence(data, canonical); err != nil {
		return nil, err
	}
	return canonical, nil
}

// Unmarshal decodes one strict AgentSuite JSON document.
//
// It rejects duplicate or unknown fields, invalid UTF-8, lossy JCS number
// representations, trailing values, and documents exceeding the format's
// declared implementation limits.
func Unmarshal(data []byte, out any) error {
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

func decodeStrict(data []byte, out any) error {
	return Unmarshal(data, out)
}

func validateJSONTokens(data []byte) error {
	return validateJSONTokensWithLimits(data, maxJSONObjectMembers, maxJSONDocumentMembers, 0)
}

// rootArrayLimit bounds arrays directly inside the root object before typed
// decoding; zero leaves array counts unrestricted for Suite documents.
func validateJSONTokensWithLimits(data []byte, objectLimit, documentLimit, rootArrayLimit int) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	members := 0
	if err := scanJSONValue(decoder, 0, &members, objectLimit, documentLimit, rootArrayLimit); err != nil {
		return err
	}
	if token, err := decoder.Token(); err == nil {
		return fmt.Errorf("unexpected trailing token %v", token)
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int, documentMembers *int, objectLimit, documentLimit, rootArrayLimit int) error {
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
			if len(seen) >= objectLimit || *documentMembers >= documentLimit {
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
			if err := scanJSONValue(decoder, depth+1, documentMembers, objectLimit, documentLimit, rootArrayLimit); err != nil {
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
		entries := 0
		for decoder.More() {
			if depth == 1 && rootArrayLimit > 0 && entries >= rootArrayLimit {
				return errors.New("JSON array entry limit exceeded")
			}
			entries++
			if err := scanJSONValue(decoder, depth+1, documentMembers, objectLimit, documentLimit, rootArrayLimit); err != nil {
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
	return sameJSONValueWithNumbers(a, b, func(left, right json.Number) bool {
		if leftInteger, err := left.Int64(); err == nil {
			rightInteger, err := right.Int64()
			return err == nil && leftInteger == rightInteger
		}
		leftFloat, leftErr := left.Float64()
		rightFloat, rightErr := right.Float64()
		return leftErr == nil && rightErr == nil && leftFloat == rightFloat
	})
}

func sameExactJSONValue(a, b any) bool {
	return sameJSONValueWithNumbers(a, b, func(left, right json.Number) bool {
		leftValue, leftOK := new(big.Rat).SetString(left.String())
		rightValue, rightOK := new(big.Rat).SetString(right.String())
		return leftOK && rightOK && leftValue.Cmp(rightValue) == 0
	})
}

func sameJSONValueWithNumbers(a, b any, sameNumber func(json.Number, json.Number) bool) bool {
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
		return ok && sameNumber(a, other)
	case []any:
		other, ok := b.([]any)
		if !ok || len(a) != len(other) {
			return false
		}
		for i := range a {
			if !sameJSONValueWithNumbers(a[i], other[i], sameNumber) {
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
			if !ok || !sameJSONValueWithNumbers(value, otherValue, sameNumber) {
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

// Digest returns the SHA-256 digest of the JCS representation of data.
func Digest(data []byte) (string, error) {
	canonical, err := jcs.Transform(data)
	if err != nil {
		return "", fmt.Errorf("canonicalize JSON: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func canonicalDigest(data []byte) (string, error) {
	return Digest(data)
}

// canonicalSize returns the byte length of the JCS-canonical representation
// of data, describing the same canonical bytes as canonicalDigest.
func canonicalSize(data []byte) (int64, error) {
	canonical, err := jcs.Transform(data)
	if err != nil {
		return 0, fmt.Errorf("canonicalize JSON: %w", err)
	}
	return int64(len(canonical)), nil
}

// descriptorSizeAndDigestMatch performs the cheap length and raw-byte digest
// comparison against descriptor, without canonicalizing data at all. It lets
// a caller reject a size/digest-mismatched document before investing in full
// JSON parsing (strict decoding, canonicalization, or reflection-based field
// checks). It does not confirm that data is itself stored in canonical form;
// callers that parse data after this check still need
// MatchesCanonicalDescriptor to enforce that invariant.
func descriptorSizeAndDigestMatch(data []byte, descriptor Descriptor) bool {
	return DescriptorSizeAndDigestMatch(data, descriptor)
}

// DescriptorSizeAndDigestMatch performs the cheap length and raw-byte digest
// comparison against descriptor, without canonicalizing data at all. It lets
// a caller reject a size/digest-mismatched document before investing in full
// JSON parsing (strict decoding, canonicalization, or reflection-based field
// checks). It does not confirm that data is itself stored in canonical form;
// callers that parse data after this check still need
// MatchesCanonicalDescriptor to enforce that invariant.
func DescriptorSizeAndDigestMatch(data []byte, descriptor Descriptor) bool {
	return int64(len(data)) == descriptor.Size && digestBytes(data) == descriptor.Digest
}

// matchesCanonicalDescriptor reports whether data is itself stored as its
// own RFC 8785 JCS canonical bytes, and whether descriptor's digest and size
// describe that same canonical representation. A descriptor binding
// canonical identity is satisfied only when the stored bytes are already
// canonical: differently formatted (for example pretty-printed) but
// semantically equivalent JSON is rejected, since a consumer must be able to
// load the referenced bytes directly without re-canonicalizing them first.
func matchesCanonicalDescriptor(data []byte, descriptor Descriptor) bool {
	return MatchesCanonicalDescriptor(data, descriptor)
}

// MatchesCanonicalDescriptor reports whether data is itself stored as its
// own RFC 8785 JCS canonical bytes, and whether descriptor's digest and size
// describe that same canonical representation. A descriptor binding
// canonical identity is satisfied only when the stored bytes are already
// canonical: differently formatted (for example pretty-printed) but
// semantically equivalent JSON is rejected, since a consumer must be able to
// load the referenced bytes directly without re-canonicalizing them first.
// Callers outside this package (for example builders that re-read pinned
// child manifests) should use this instead of re-deriving digest/size checks
// by hand, so every consumer enforces the same stored-byte identity rule.
func MatchesCanonicalDescriptor(data []byte, descriptor Descriptor) bool {
	// Reject on raw length and raw-byte digest first: cheap checks that avoid
	// canonicalizing data at all when it plainly cannot match. If data is
	// itself already canonical (the required storage form), its raw SHA-256
	// digest equals the canonical digest, so this is not merely a shortcut
	// for the common case, it rules out any non-canonically stored document
	// too, since such a document's raw bytes cannot hash to the canonical
	// digest of a differently formatted equivalent.
	if int64(len(data)) != descriptor.Size || digestBytes(data) != descriptor.Digest {
		return false
	}
	canonical, err := jcs.Transform(data)
	return err == nil && bytes.Equal(data, canonical)
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// IsValidDigest reports whether value is a canonical AgentSuite SHA-256 digest.
func IsValidDigest(value string) bool {
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
