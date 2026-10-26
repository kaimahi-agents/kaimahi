package localtools

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var toolName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

// ValidToolName accepts a bounded identifier, never a binding or endpoint.
func ValidToolName(name string) bool { return toolName.MatchString(name) }

// CopyDefinitions validates a bounded secret-free toolset and detaches all JSON
// data. Schemas resolve locally only: offline replay has no loader authority.
func CopyDefinitions(defs []api.ToolDefinition) ([]api.ToolDefinition, error) {
	raw, err := json.Marshal(defs)
	if err != nil || len(raw) > 64*1024 || len(defs) == 0 || len(defs) > 32 || !runtime.JSONNumbersExact(raw) {
		return nil, errInvalidBinding
	}
	var copied []api.ToolDefinition
	if json.Unmarshal(raw, &copied) != nil {
		return nil, errInvalidBinding
	}
	seen := map[string]bool{}
	for _, d := range copied {
		if !ValidToolName(d.Name) || seen[d.Name] || len(d.Description) > 4096 || d.InputSchema == nil {
			return nil, errInvalidBinding
		}
		seen[d.Name] = true
		if _, err := compileSchema(d.InputSchema); err != nil {
			return nil, errInvalidBinding
		}
	}
	return copied, nil
}

func compileSchema(schema map[string]any) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft7)
	c.UseLoader(jsonschema.SchemeURLLoader{})
	if err := c.AddResource("urn:kmx:tool", schema); err != nil {
		return nil, err
	}
	return c.Compile("urn:kmx:tool")
}

// ValidateArguments requires a listed name and a schema-valid bounded object.
func ValidateArguments(defs []api.ToolDefinition, name string, args map[string]any) error {
	raw, err := json.Marshal(args)
	if err != nil || len(raw) > 8*1024 || !runtime.JSONNumbersExact(raw) {
		return errInvalidCall
	}
	// Nil arguments represent the legacy empty object, not JSON null.
	var value any = map[string]any{}
	if args != nil && json.Unmarshal(raw, &value) != nil {
		return errInvalidCall
	}
	for _, d := range defs {
		if d.Name == name {
			s, err := compileSchema(d.InputSchema)
			if err != nil || s.Validate(value) != nil {
				return errInvalidCall
			}
			return nil
		}
	}
	return errInvalidCall
}

// CheckJSON checks raw JSON and every decoded key/string before journal append.
func CheckJSON(value any, check func(...string) error) error {
	return CheckJSONPayloads(value, check)
}

// CheckJSONPayloads is CheckJSON with trusted absolute payload roots. Each path
// is a list of mapping keys or sequence indices from the marshaled value's root;
// "*" matches any sequence element, never a mapping key. The nesting budget restarts at 0 once,
// at each matched root, and never again inside that payload. A matched string
// that encodes JSON is decoded at the same restarted level, so a wire envelope
// does not shrink the payload's budget. JSON embedded deeper keeps the
// cumulative ceiling. Every raw string, key and decoded string is still passed
// to check, and no path means the original whole-value budget.
func CheckJSONPayloads(value any, check func(...string) error, payloadPaths ...[]string) error {
	raw, err := json.Marshal(value)
	if err != nil || check == nil {
		return errUnsafeContent
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return errUnsafeContent
	}
	if err := check(string(raw)); err != nil {
		return err
	}
	type step struct {
		key      string
		sequence bool
		index    int
	}
	matches := func(path []step) bool {
		for _, want := range payloadPaths {
			if len(want) != len(path) {
				continue
			}
			ok := true
			for i, s := range path {
				if s.sequence {
					if want[i] != "*" && want[i] != strconv.Itoa(s.index) {
						ok = false
						break
					}
				} else if want[i] == "*" || s.key != want[i] {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
		return false
	}
	child := func(path []step, tracking bool, s step) []step {
		if !tracking {
			return nil
		}
		return append(slices.Clip(path), s)
	}
	// tracking is false inside a payload and inside embedded JSON, where no
	// trusted path can apply.
	var walk func(v any, depth int, path []step, tracking bool) error
	walk = func(v any, depth int, path []step, tracking bool) error {
		root := false
		if tracking && len(payloadPaths) > 0 && matches(path) {
			depth, tracking, root = 0, false, true
		}
		if depth > 32 {
			return errUnsafeContent
		}
		switch v := v.(type) {
		case string:
			if err := check(v); err != nil {
				return err
			}
			var nested any
			if json.Valid([]byte(v)) && !runtime.JSONKeysUnique([]byte(v)) {
				return errUnsafeContent
			}
			if json.Unmarshal([]byte(v), &nested) == nil {
				// A matched root already restarted depth; its decoded content is
				// the same level, not one deeper. Other strings add one level.
				next := depth + 1
				if root {
					next = 0
				}
				return walk(nested, next, nil, false)
			}
		case map[string]any:
			for k, x := range v {
				if err := check(k); err != nil {
					return err
				}
				if err := walk(x, depth+1, child(path, tracking, step{key: k}), tracking); err != nil {
					return err
				}
			}
		case []any:
			for i, x := range v {
				if err := walk(x, depth+1, child(path, tracking, step{sequence: true, index: i}), tracking); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(decoded, 0, nil, len(payloadPaths) > 0)
}

// NewTrustedHost accepts only host-supplied authority. Nothing in an agent file
// can provide definitions, an executor or a credential checker.
func NewTrustedHost(defs []api.ToolDefinition, execute controller.ToolFunc, check func(...string) error) (*Host, error) {
	copied, err := CopyDefinitions(defs)
	if err != nil || execute == nil || check == nil {
		return nil, errInvalidBinding
	}
	h := &Host{definitions: copied, execute: execute, check: check}
	if CheckJSON(copied, h.Check) != nil {
		return nil, errUnsafeContent
	}
	return h, nil
}

func (h *Host) Definitions() []api.ToolDefinition {
	if h == nil {
		return nil
	}
	if h.definitions == nil {
		return nil
	} // preserves the exact legacy v1 config
	defs, _ := CopyDefinitions(h.definitions)
	return defs
}

func (h *Host) Matches(names []string) bool {
	if h == nil {
		return false
	}
	if h.definitions == nil {
		return h.binding.Matches(names)
	}
	if len(names) != len(h.definitions) {
		return false
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] || !slices.ContainsFunc(h.definitions, func(d api.ToolDefinition) bool { return d.Name == name }) {
			return false
		}
		seen[name] = true
	}
	return true
}

func (h *Host) executeTrusted(ctx context.Context, scope controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return api.ToolResult{}, err
	}
	if scope.SessionUID == "" || !callID.MatchString(call.ID) || call.IdempotencyKey == "" || call.Mediation != api.MediationControllerMediated || ValidateArguments(h.definitions, call.Tool, call.Args) != nil {
		return api.ToolResult{}, errInvalidCall
	}
	if CheckJSON(call, h.Check) != nil {
		return api.ToolResult{}, errUnsafeContent
	}
	// Detach executor arguments from the already journaled model response.
	raw, _ := json.Marshal(call.Args)
	if call.Args != nil {
		call.Args = nil
		_ = json.Unmarshal(raw, &call.Args)
	}
	result, err := h.execute(ctx, scope, call)
	if contextErr := ctx.Err(); contextErr != nil {
		return api.ToolResult{}, contextErr
	}
	if err != nil {
		return api.ToolResult{}, errReadFailed
	}
	raw, err = json.Marshal(result)
	if err != nil || len(raw) > 32*1024 || !runtime.JSONNumbersExact(raw) || result.ID != call.ID || result.IsError || result.Error != "" || result.Output == nil || result.Content != nil || result.OutputURI != "" || result.OutputDigest != "" {
		return api.ToolResult{}, errInvalidResult
	}
	var copied api.ToolResult
	if json.Unmarshal(raw, &copied) != nil || CheckJSON(copied, h.Check) != nil {
		return api.ToolResult{}, errUnsafeContent
	}
	return copied, nil
}
