package app

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// Strict JSON bounds are shared by native evidence and runtime protocol readers.
const maxSessionsReceiptBytes = 1 << 20

func unambiguousSessionsReceiptJSON(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var scan func(int) bool
	scan = func(depth int) bool {
		token, err := decoder.Token()
		if err != nil || depth > 32 {
			return false
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return true
		}
		if delimiter != '{' && delimiter != '[' {
			return false
		}
		var keys []string
		for decoder.More() {
			if delimiter == '{' {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || len(keys) >= 64 {
					return false
				}
				for _, seen := range keys {
					if strings.EqualFold(key, seen) {
						return false
					}
				}
				keys = append(keys, key)
			}
			if !scan(depth + 1) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && (delimiter == '{' && closing == json.Delim('}') || delimiter == '[' && closing == json.Delim(']'))
	}
	if !scan(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

var evaluationEvidenceID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

func verificationDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func verificationIdentity(value string) bool {
	if len(value) == 0 || len(value) > 256 || secretshapes.Match(value) != nil {
		return false
	}
	for _, c := range value {
		if c < '!' || c > '~' {
			return false
		}
	}
	return true
}
