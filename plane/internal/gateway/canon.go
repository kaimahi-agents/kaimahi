package gateway

// Canonicalization: ONE normalized representation of an inbound JSON-RPC
// message, produced once per request and used by every consumer that can
// disagree — the policy decision, the argument digest, the audit summary
// and the bytes forwarded upstream. They are all derived from the same
// tree, so no parser difference can put them out of step.
//
// While `arguments` was opaque bytes nothing inspected, canonicalizing
// the top level and one level of `params` was enough. Arguments are
// policy inputs now, so the whole message is normalized, to any depth,
// and a DUPLICATED KEY is refused rather than collapsed: Go reads
// last-wins and an upstream may read first-wins, so
// `{"amount": 42000, "amount": 48000}` is a smuggling vector, not a
// typo. No legitimate MCP client emits one; the standing guidance is to
// fail closed.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

const (
	// maxCanonDepth bounds recursion: a message nested deeper than this
	// is refused rather than walked. Far beyond any MCP tool call —
	// tools/call is two levels before arguments begin.
	maxCanonDepth = 32
	// maxCanonNodes bounds total work: every scalar, object, array and
	// member counts. maxRequestBody already bounds bytes; this bounds the
	// tree those bytes can describe.
	maxCanonNodes = 20000
)

var (
	errDuplicateKey = errors.New("duplicate JSON key")
	errTooDeep      = errors.New("JSON nesting too deep")
	errTooLarge     = errors.New("JSON message too complex")
	errNotAnObject  = errors.New("JSON-RPC message must be an object")
	errTrailing     = errors.New("trailing data after the JSON-RPC message")
)

// canonicalize parses one JSON-RPC message into the canonical tree and
// re-marshals it. The returned bytes are what goes upstream; the map is
// what policy, the digest and the summary read. An error means the
// message is refused (the caller answers 400, audited) — never a
// silently repaired message.
func canonicalize(body []byte) ([]byte, map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	nodes := 0
	v, err := decodeCanonical(dec, 1, &nodes)
	if err != nil {
		return nil, nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, errTrailing
	}
	msg, ok := v.(map[string]any)
	if !ok {
		return nil, nil, errNotAnObject
	}
	out, err := marshalCanonical(msg)
	if err != nil {
		return nil, nil, err
	}
	return out, msg, nil
}

// marshalCanonical renders a canonical tree deterministically: Go sorts
// object keys, and HTML escaping is off so a tool argument carrying
// `<`, `>` or `&` reaches the upstream as it was written.
func marshalCanonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// decodeCanonical reads one value from the token stream, refusing a
// duplicated object key at any depth and bounding depth and node count.
func decodeCanonical(dec *json.Decoder, depth int, nodes *int) (any, error) {
	if depth > maxCanonDepth {
		return nil, errTooDeep
	}
	*nodes++
	if *nodes > maxCanonNodes {
		return nil, errTooLarge
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return canonScalar(tok)
	}
	switch delim {
	case '{':
		obj := map[string]any{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyTok.(string)
			if !ok { // encoding/json guarantees a string here; belt and braces
				return nil, fmt.Errorf("object key is not a string")
			}
			if _, seen := obj[key]; seen {
				return nil, fmt.Errorf("%w: %q", errDuplicateKey, key)
			}
			val, err := decodeCanonical(dec, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			obj[key] = val
		}
		if _, err := dec.Token(); err != nil { // closing '}'
			return nil, err
		}
		return obj, nil
	case '[':
		arr := []any{}
		for dec.More() {
			val, err := decodeCanonical(dec, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
		if _, err := dec.Token(); err != nil { // closing ']'
			return nil, err
		}
		return arr, nil
	}
	return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
}

// canonScalar normalizes one scalar. Numbers keep the literal they
// arrived as, EXCEPT an integer that fits int64, which is rendered in one
// canonical form. Keeping other literals verbatim is deliberate: a
// gateway must not silently change the number an upstream will read
// (1e20 re-rendered through float64 is a different literal), and money in
// this plane is integer cents, which the int64 path covers. The
// consequence, documented in docs/tool-governance.md: for a policy field,
// 48000 and 48000.0 are different calls.
func canonScalar(tok json.Token) (any, error) {
	num, ok := tok.(json.Number)
	if !ok {
		return tok, nil // string, bool, or nil
	}
	if i, err := strconv.ParseInt(num.String(), 10, 64); err == nil {
		return json.Number(strconv.FormatInt(i, 10)), nil
	}
	return num, nil
}

// argumentsOf returns the tools/call argument object from the canonical
// tree. Absent arguments read as the empty set (a legal call with no
// arguments); anything that is not an object is refused by the caller,
// since policy cannot be evaluated on it.
func argumentsOf(msg map[string]any) (map[string]any, bool) {
	params, ok := msg["params"].(map[string]any)
	if !ok {
		return nil, false
	}
	raw, present := params["arguments"]
	if !present || raw == nil {
		return map[string]any{}, true
	}
	args, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	return args, true
}

// toolNameOf returns params.name, empty when it is missing or not a
// string (the caller then refuses the call).
func toolNameOf(msg map[string]any) string {
	params, ok := msg["params"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := params["name"].(string)
	return name
}
