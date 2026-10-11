package agentsuite

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// Archive metadata deliberately has its own policy: OCI permits unknown fields
// and in-toto inventories need larger budgets than strict AgentSuite documents.
// Keep these limits private instead of exposing the portable parser mechanics.
const (
	maxArchiveMetadataBytes           = 4 << 20
	maxArchiveJSONDepth               = 100
	maxArchiveMetadataObjectMembers   = 1024
	maxArchiveMetadataDocumentMembers = 100_000
)

func decodeArchiveJSON(data []byte, out any) error {
	return decodeArchiveJSONWithLimits(data, out, maxArchiveMetadataBytes, maxArchiveMetadataObjectMembers, maxArchiveMetadataDocumentMembers)
}

func decodeArchiveJSONWithLimits(data []byte, out any, byteLimit int64, objectLimit, documentLimit int) error {
	// Bound every array directly inside the root object before typed allocation,
	// including case-insensitive and escaped field names. Nested predicate arrays
	// remain governed by the byte, member, nesting, and duplicate-key budgets.
	if int64(len(data)) > byteLimit {
		return fmt.Errorf("OCI JSON metadata exceeds %d bytes", byteLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	members := 0
	if err := scanArchiveJSONValue(decoder, 0, &members, objectLimit, documentLimit); err != nil {
		return err
	}
	if token, err := decoder.Token(); err == nil {
		return fmt.Errorf("unexpected trailing token %v", token)
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return json.Unmarshal(data, out)
}

func scanArchiveJSONValue(decoder *json.Decoder, depth int, documentMembers *int, objectLimit, documentLimit int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		if value, ok := token.(string); ok && !utf8.ValidString(value) {
			return errors.New("JSON string is not valid Unicode")
		}
		return nil
	}
	if depth >= maxArchiveJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d", maxArchiveJSONDepth)
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
			if !utf8.ValidString(key) {
				return errors.New("JSON object key: JSON string is not valid Unicode")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			*documentMembers++
			if err := scanArchiveJSONValue(decoder, depth+1, documentMembers, objectLimit, documentLimit); err != nil {
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
			if depth == 1 && entries >= maxArchiveIndexEntries {
				return errors.New("JSON array entry limit exceeded")
			}
			entries++
			if err := scanArchiveJSONValue(decoder, depth+1, documentMembers, objectLimit, documentLimit); err != nil {
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
