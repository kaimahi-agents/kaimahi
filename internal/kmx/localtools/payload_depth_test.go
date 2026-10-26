package localtools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
)

// nest returns n maps deep: the root is level 0 and the string leaf is level n.
func nest(n int, leaf any) any {
	value := leaf
	for i := 0; i < n; i++ {
		value = map[string]any{"k": value}
	}
	return value
}

func nestedJSON(t *testing.T, n int, leaf any) string {
	t.Helper()
	raw, err := json.Marshal(nest(n, leaf))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

var mockPaths = [][]string{{"tools", "*", "calls", "*", "arguments"}, {"tools", "*", "calls", "*", "result"}}

func mockWrapper(arguments, result any) map[string]any {
	return map[string]any{"tools": []any{map[string]any{"name": "t", "calls": []any{map[string]any{"arguments": arguments, "result": result}}}}}
}

func TestCheckJSONPayloadsResetsDepthOncePerMatchedRoot(t *testing.T) {
	allow := func(...string) error { return nil }
	// Default helper: the wrapper still consumes five levels, so 28 is refused.
	if err := CheckJSON(mockWrapper(nest(27, "x"), nest(0, "x")), allow); err != nil {
		t.Fatalf("27 below the wrapper must still pass: %v", err)
	}
	if err := CheckJSON(mockWrapper(nest(28, "x"), nest(0, "x")), allow); err == nil {
		t.Fatal("default helper must retain the wrapper-relative 32 ceiling")
	}
	for _, name := range []string{"arguments", "result"} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				depth int
				ok    bool
			}{{32, true}, {33, false}} {
				a, r := nest(0, "x"), nest(0, "x")
				if name == "arguments" {
					a = nest(tc.depth, "x")
				} else {
					r = nest(tc.depth, "x")
				}
				err := CheckJSONPayloads(mockWrapper(a, r), allow, mockPaths...)
				if (err == nil) != tc.ok {
					t.Fatalf("depth %d: %v", tc.depth, err)
				}
			}
		})
	}
	// A payload does not restart its budget at an inner mapping that happens to
	// spell a trusted path: 16 outer levels plus a nested "tools" payload at 17 fails.
	inner := mockWrapper(nest(16, "x"), nest(0, "x"))
	if err := CheckJSONPayloads(mockWrapper(map[string]any{"k": inner}, nest(0, "x")), allow, mockPaths...); err != nil {
		t.Fatalf("shallow nested lookalike: %v", err)
	}
	deep := mockWrapper(nest(20, "x"), nest(0, "x"))
	if err := CheckJSONPayloads(mockWrapper(nest(15, deep), nest(0, "x")), allow, mockPaths...); err == nil {
		t.Fatal("budget reset inside a payload")
	}
}

func TestCheckJSONPayloadsMatchesOnlyExactSequencePaths(t *testing.T) {
	allow := func(...string) error { return nil }
	deep := nest(32, "x")
	for name, value := range map[string]any{
		"mapping key star":        map[string]any{"tools": map[string]any{"*": map[string]any{"calls": []any{map[string]any{"arguments": deep}}}}},
		"name only":               map[string]any{"arguments": deep},
		"metadata sibling":        map[string]any{"tools": []any{map[string]any{"name": deep}}},
		"parameters":              map[string]any{"tools": []any{map[string]any{"parameters": deep}}},
		"calls as mapping":        map[string]any{"tools": []any{map[string]any{"calls": map[string]any{"0": map[string]any{"arguments": deep}}}}},
		"wrapper extra prefix":    map[string]any{"x": mockWrapper(deep, nil)},
		"payload below an object": map[string]any{"tools": []any{map[string]any{"calls": []any{map[string]any{"other": map[string]any{"arguments": deep}}}}}},
	} {
		if err := CheckJSONPayloads(value, allow, mockPaths...); err == nil {
			t.Fatalf("%s received a payload budget", name)
		}
	}
	if err := CheckJSONPayloads(mockWrapper(deep, deep), allow, mockPaths...); err != nil {
		t.Fatalf("exact path: %v", err)
	}
}

func TestCheckJSONPayloadsDecodesMatchedStringAtRootZero(t *testing.T) {
	allow := func(...string) error { return nil }
	paths := []string{"messages", "*", "content"}
	for _, tc := range []struct {
		depth int
		ok    bool
	}{{32, true}, {33, false}} {
		value := map[string]any{"messages": []any{map[string]any{"content": nestedJSON(t, tc.depth, "x")}}}
		if err := CheckJSONPayloads(value, allow, paths); (err == nil) != tc.ok {
			t.Fatalf("encoded depth %d: %v", tc.depth, err)
		}
	}
	// Without a selector the encoded wrapper still consumes the ordinary levels.
	if err := CheckJSON(map[string]any{"messages": []any{map[string]any{"content": nestedJSON(t, 32, "x")}}}, allow); err == nil {
		t.Fatal("default helper changed")
	}
	// Embedded JSON below the payload root remains cumulative.
	embedded := map[string]any{"k": nestedJSON(t, 32, "x")}
	value := map[string]any{"messages": []any{map[string]any{"content": nestedJSON(t, 0, "x")}}}
	value["messages"].([]any)[0].(map[string]any)["content"] = func() string { raw, _ := json.Marshal(embedded); return string(raw) }()
	if err := CheckJSONPayloads(value, allow, paths); err == nil {
		t.Fatal("embedded JSON below the payload root restarted its budget")
	}
}

func TestCheckJSONPayloadsKeepsSafetyChecksEverywhere(t *testing.T) {
	unsafe := errors.New("unsafe")
	check := func(values ...string) error {
		for _, v := range values {
			if strings.Contains(v, "canary") {
				return unsafe
			}
		}
		return nil
	}
	deepWith := func(leaf string) any { return nest(32, leaf) }
	for name, value := range map[string]any{
		"payload value": mockWrapper(deepWith("canary"), nest(0, "x")),
		"payload key":   mockWrapper(map[string]any{"canary": nest(31, "x")}, nest(0, "x")),
		"embedded":      mockWrapper(map[string]any{"k": `{"v":"canary"}`}, nest(0, "x")),
		"metadata":      map[string]any{"tools": []any{map[string]any{"name": "canary"}}},
		"result":        mockWrapper(nest(0, "x"), deepWith("canary")),
	} {
		if err := CheckJSONPayloads(value, check, mockPaths...); !errors.Is(err, unsafe) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A matched encoded string is checked raw before it is decoded.
	if err := CheckJSONPayloads(map[string]any{"a": `{"x":"canary"}`}, check, []string{"a"}); !errors.Is(err, unsafe) {
		t.Fatalf("encoded payload: %v", err)
	}
	// Duplicate keys inside an encoded payload are refused.
	if err := CheckJSONPayloads(map[string]any{"a": `{"x":1,"x":2}`}, func(...string) error { return nil }, []string{"a"}); err == nil {
		t.Fatal("duplicate keys escaped")
	}
	if err := CheckJSONPayloads(map[string]any{}, nil, []string{"a"}); err == nil {
		t.Fatal("nil callback accepted")
	}
}

func TestCheckJSONPayloadsAPIStructPaths(t *testing.T) {
	allow := func(...string) error { return nil }
	call := api.ToolCall{ID: "c", Tool: "t", Args: nest(32, "x").(map[string]any)}
	if err := CheckJSONPayloads(call, allow, []string{"Args"}); err != nil {
		t.Fatal(err)
	}
	if err := CheckJSON(call, allow); err == nil {
		t.Fatal("default call scan widened")
	}
	result := api.ToolResult{ID: "c", Output: nest(33, "x").(map[string]any)}
	if err := CheckJSONPayloads(result, allow, []string{"Output"}); err == nil {
		t.Fatal("33 accepted")
	}
}
