// Package kagentcompat is the compatibility layer for the PINNED kagent CLI
// v0.10.1's A2A sends.
//
// v0.10.1 builds its outgoing `protocol.Message` without ever setting
// MessageID, so every `invoke` — streaming and not — puts
//
//	"params":{"message":{"kind":"message","messageId":"", ...}}
//
// on the wire. The agent's A2A endpoint answers that with JSON-RPC -32602,
// `message ID is required`; the same request with an ID succeeds. The CLI's
// error decoder then loses the real cause and reports
//
//	failed to decode response: json: cannot unmarshal object into Go struct
//	field ClientResponse.error.data of type []*errordetails.Typed
//
// which is why this reads as a transport fault rather than a missing field.
// Both proofs are recorded beside this change.
//
// The binary is checksum-pinned and not ours to patch, and kmx parses its
// output, so the fix is a hop rather than a fork: a loopback listener that
// forwards to one fixed controller forward and adds a unique `messageId` to
// exactly the sends that have none. It is deliberately NOT a general proxy —
// the target is fixed at construction, proxy-form requests are refused, and
// request bodies are bounded and fail closed.
//
// The rewrite is confined to the legacy A2A invoke paths (see
// legacyA2APrefixes). Every other request — including any POST a later
// controller may add — is forwarded untouched and unbuffered, so this shim
// can never become a second, divergent validator of the controller's API.
//
// Requests that already carry an ID — kmx's own HITL continuations — are
// forwarded byte for byte: rewriting one would submit a new message instead
// of the decision the controller is waiting on.
package kagentcompat

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
)

// MaxRequestBody bounds a body the hop is willing to read and rewrite.
// Rewriting means buffering, and buffering an unbounded body on the client's
// say-so is the one way a loopback helper can hurt the machine it runs on.
// A2A sends are prompts and decisions; 4 MiB is far above any of them. It
// applies only to the A2A invoke posts that are actually rewritten.
const MaxRequestBody = 4 << 20

// NewMessageID returns a fresh A2A message ID.
//
// The controller keys a turn on this value, so it has to be unique per send
// rather than merely present: a repeated ID is the same bug wearing a
// different hat. Random, not a timestamp — two sends inside one clock tick
// are ordinary in a scripted chat.
func NewMessageID() string { return "kmx-" + rand.Text() }

// InjectMessageID returns body with a generated `messageId` on a
// `message/send` or `message/stream` whose message has none, reporting
// whether it changed anything.
//
// Every other request is returned unchanged, byte for byte. The split is
// narrow on purpose: a body this cannot PARSE is an error (the only client
// on this hop speaks JSON-RPC, so unreadable bytes are a fault, not
// traffic), while a body it can parse but does not recognise is simply not
// its business — protocol validation belongs to the controller, not to a
// compatibility shim that would then be a second, divergent validator.
func InjectMessageID(body []byte, newID func() string) ([]byte, bool, error) {
	if newID == nil {
		newID = NewMessageID
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, false, fmt.Errorf("not a JSON-RPC request: %w", err)
	}
	var method string
	if err := json.Unmarshal(envelope["method"], &method); err != nil {
		return body, false, nil
	}
	if method != "message/send" && method != "message/stream" {
		return body, false, nil
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(envelope["params"], &params); err != nil {
		return body, false, nil
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(params["message"], &message); err != nil {
		return body, false, nil
	}
	if raw, ok := message["messageId"]; ok {
		var existing string
		if err := json.Unmarshal(raw, &existing); err != nil {
			// Present but not a string: the controller's to reject, not the
			// hop's to overwrite.
			return body, false, nil
		}
		// v0.10.1 sends the key with an EMPTY value, so "already present" is
		// not the test — "already usable" is.
		if strings.TrimSpace(existing) != "" {
			return body, false, nil
		}
	}
	id, err := marshal(newID())
	if err != nil {
		return nil, false, err
	}
	message["messageId"] = id
	// Rebuilt one level at a time from json.RawMessage, so every value the
	// caller sent — the parts, the metadata, the task and context IDs —
	// reaches the controller exactly as it was written.
	if params["message"], err = marshal(message); err != nil {
		return nil, false, err
	}
	if envelope["params"], err = marshal(params); err != nil {
		return nil, false, err
	}
	rewritten, err := marshal(envelope)
	if err != nil {
		return nil, false, err
	}
	return rewritten, true, nil
}

// marshal encodes without JSON's HTML escaping, so a prompt containing `<`
// is forwarded as the caller wrote it rather than re-encoded in passing.
func marshal(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}
