package runtime

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxJSONUniqueDepth bounds nesting for JSONKeysUnique.
const maxJSONUniqueDepth = 32

// JSONKeysUnique reports whether data is one valid UTF-8 JSON value, nested at
// most 32 levels deep, with no trailing data and no object holding two keys
// that encoding/json would match to the same field. Keys are compared with
// strings.EqualFold semantics, so case-insensitive aliases count as
// duplicates: encoding/json keeps the last of them, which lets a later
// occurrence shadow an earlier tool call, finish reason or identity. It does
// not restrict which keys appear.
func JSONKeysUnique(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > maxJSONUniqueDepth {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delimiter {
		case '{':
			keys := map[string]struct{}{}
			for decoder.More() {
				token, err := decoder.Token()
				if err != nil {
					return false
				}
				key, ok := token.(string)
				if !ok {
					return false
				}
				folded := foldJSONKey(key)
				if _, seen := keys[folded]; seen {
					return false
				}
				keys[folded] = struct{}{}
				if !walk(depth + 1) {
					return false
				}
			}
		case '[':
			for decoder.More() {
				if !walk(depth + 1) {
					return false
				}
			}
		default:
			return false
		}
		end, err := decoder.Token()
		return err == nil && ((delimiter == '{' && end == json.Delim('}')) || (delimiter == '[' && end == json.Delim(']')))
	}
	if !walk(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

// foldJSONKey maps each rune to the smallest rune in its unicode.SimpleFold
// orbit. Two keys fold to the same string exactly when strings.EqualFold
// reports them equal, so case-insensitive duplicates share one set key.
func foldJSONKey(key string) string {
	var folded strings.Builder
	folded.Grow(len(key))
	for _, r := range key {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		folded.WriteRune(least)
	}
	return folded.String()
}
