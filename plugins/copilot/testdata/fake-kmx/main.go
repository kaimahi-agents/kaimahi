// Test-only stdio process: protocol/result fixtures, never a runner or evaluator.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) != 4 || os.Args[1] != "mcp" || os.Args[2] != "--root" || !filepath.IsAbs(os.Args[3]) || os.Args[3] != os.Getenv("EXPECTED_ROOT") {
		fmt.Fprintln(os.Stderr, "fixture requires explicit expected root")
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		if len(request.ID) == 0 {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			response["result"] = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake-kmx-fixture", "version": "0.0.0"}}
		case "tools/list":
			tools := []any{}
			for _, name := range []string{"create", "edit", "run", "test"} {
				tools = append(tools, map[string]any{"name": name, "inputSchema": inputSchema(name)})
			}
			response["result"] = map[string]any{"tools": tools}
		case "tools/call":
			if request.Params.Name != "run" && request.Params.Name != "test" {
				response["error"] = map[string]any{"code": -32601, "message": "fixture supports only preview result calls"}
				break
			}
			result := preview(request.Params.Name, os.Getenv("KMX_FIXTURE_CASE"))
			text, _ := json.Marshal(result)
			response["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}, "structuredContent": result, "isError": false}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "unsupported fixture method"}
		}
		if encoder.Encode(response) != nil {
			os.Exit(1)
		}
	}
	if scanner.Err() != nil {
		os.Exit(1)
	}
}

// Create/Edit mirror the proposed authoring schema, not executable operations.
// Run/Test input schemas remain fixture-only until their owner publishes them.
func inputSchema(name string) map[string]any {
	properties := map[string]any{"schemaVersion": map[string]any{"type": "integer", "enum": []int{1}}}
	required := []string{}
	if name == "create" || name == "edit" {
		properties["path"] = map[string]any{"type": "string", "maxLength": 256}
		properties["content"] = map[string]any{"type": "string", "maxLength": 256 << 10}
		required = append(required, "path", "content")
		if name == "edit" {
			properties["expectedRevision"] = map[string]any{"type": "string", "pattern": "^sha256:[a-f0-9]{64}$"}
			required = append(required, "expectedRevision")
		}
	}
	required = append(required, "schemaVersion")
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": map[string]any{"not": map[string]any{}}}
}

func preview(operation, scenario string) map[string]any {
	state, verdict, full, scope, reason := "complete", "pass", true, "full", "checks_passed"
	switch scenario {
	case "fail":
		verdict, full, reason = "fail", false, "assertion_failed"
	case "unknown":
		verdict, full, reason = "unknown", false, "judge_unknown"
	case "subset":
		full, scope = false, "subset"
	case "cancelled":
		state, verdict, full, reason = "cancelled", "unknown", false, "cancelled"
	}
	assertionReason := reason
	if verdict == "pass" {
		assertionReason = "assertion_satisfied"
	}
	if scenario == "cancelled" {
		assertionReason = "not_run"
	}
	example := map[string]any{"id": "greeting", "state": state, "verdict": verdict, "assertions": []any{map[string]any{"id": "greeting-text", "type": "contains", "verdict": verdict, "reasonCode": assertionReason, "reason": "Fixture classification."}}}
	return map[string]any{
		"schemaVersion": 1, "kind": "kmx.agent-file-preview", "operation": operation,
		"file":       map[string]any{"path": "greeter.agent.md", "revision": "sha256:" + strings.Repeat("a", 64)},
		"modelRoute": map[string]any{"provider": "fake", "model": "fake"},
		"selection":  map[string]any{"scope": scope, "caseIds": []string{"greeting"}, "totalCases": 1},
		"state":      state, "verdict": verdict, "fullPass": full,
		"reasonCode": reason, "reason": "Fixture classification.",
		"examples": []any{example}, "retention": map[string]any{"status": "not_requested"},
	}
}
