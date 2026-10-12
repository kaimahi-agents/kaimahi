package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Decode the blocking Go Task contract, never status-message/history fallbacks.
// Errors deliberately contain no remotely supplied identifiers or payloads.
func parseKagentEvaluationResponse(raw []byte, request kagentEvaluationExecution, input string) (kagentEvaluationExecution, string, error) {
	execution := request
	unavailable := func() (kagentEvaluationExecution, string, error) {
		return execution, "", fmt.Errorf("Kagent evaluation response unavailable")
	}
	if len(raw) > 4<<20 || !utf8.Valid(raw) || !unambiguousSessionsReceiptJSON(raw) {
		return unavailable()
	}
	root, ok := kagentEvaluationObject(raw, "jsonrpc", "id", "result", "error")
	if !ok || kagentEvaluationString(root["jsonrpc"]) != "2.0" || kagentEvaluationString(root["id"]) != request.RPCID {
		return unavailable()
	}
	if _, present := root["error"]; present {
		return unavailable()
	}
	task, ok := kagentEvaluationObject(root["result"], "kind", "id", "contextId", "status", "artifacts", "history", "metadata")
	if !ok || kagentEvaluationString(task["kind"]) != "task" {
		return unavailable()
	}
	taskID, contextID := kagentEvaluationString(task["id"]), kagentEvaluationString(task["contextId"])
	status, ok := kagentEvaluationObject(task["status"], "state", "timestamp", "message")
	state := kagentEvaluationString(status["state"])
	if !ok || !safeKagentEvaluationID(taskID) || !safeKagentEvaluationID(contextID) {
		return unavailable()
	}
	switch state {
	case "completed", "failed", "canceled", "rejected", "submitted", "working", "input-required", "auth-required", "unknown":
	default:
		return unavailable()
	}
	execution.TaskID, execution.ContextID, execution.State = taskID, contextID, state
	if message, present := status["message"]; present {
		if _, _, _, ok := kagentEvaluationMessage(message, execution); !ok {
			return unavailable()
		}
	}
	if history, present := task["history"]; present {
		var messages []json.RawMessage
		if json.Unmarshal(history, &messages) != nil || messages == nil {
			return unavailable()
		}
		users := 0
		for _, message := range messages {
			role, id, text, ok := kagentEvaluationMessage(message, execution)
			if !ok {
				return unavailable()
			}
			if role == "user" {
				if id != request.RequestMessageID || text != input {
					return unavailable()
				}
				// v0.10.3 repeats the submitted message in Task history.
				// The same identity/input is one turn, not prior conversation.
				users = 1
			}
		}
		if users != 1 {
			return unavailable()
		}
	}
	if state != "completed" {
		return execution, "", nil
	}
	var artifacts []json.RawMessage
	if json.Unmarshal(task["artifacts"], &artifacts) != nil || len(artifacts) == 0 {
		return unavailable()
	}
	seen := map[string]bool{}
	answer, candidates := "", 0
	for _, rawArtifact := range artifacts {
		artifact, ok := kagentEvaluationObject(rawArtifact, "artifactId", "name", "description", "parts", "metadata", "extensions")
		id := kagentEvaluationString(artifact["artifactId"])
		if !ok || !safeKagentEvaluationID(id) || seen[id] {
			return unavailable()
		}
		seen[id] = true
		text, hasText, ok := kagentEvaluationParts(artifact["parts"])
		if !ok {
			return unavailable()
		}
		if hasText {
			candidates++
			answer = text
		}
	}
	if candidates != 1 || strings.TrimSpace(answer) == "" || strings.IndexFunc(answer, func(r rune) bool { return !unicode.IsPrint(r) && r != '\n' && r != '\t' }) >= 0 {
		return unavailable()
	}
	return execution, answer, nil
}

func kagentEvaluationObject(raw json.RawMessage, fields ...string) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	for key, value := range object {
		allowed := false
		for _, field := range fields {
			if key == field {
				allowed = true
				break
			}
		}
		if !allowed || string(value) == "null" {
			return nil, false
		}
	}
	return object, true
}

func kagentEvaluationString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func kagentEvaluationMessage(raw json.RawMessage, execution kagentEvaluationExecution) (string, string, string, bool) {
	message, ok := kagentEvaluationObject(raw, "kind", "role", "messageId", "taskId", "contextId", "parts", "metadata", "extensions", "referenceTaskIds")
	role, id := kagentEvaluationString(message["role"]), kagentEvaluationString(message["messageId"])
	if !ok || kagentEvaluationString(message["kind"]) != "message" || (role != "agent" && role != "user") || !safeKagentEvaluationID(id) {
		return "", "", "", false
	}
	for _, identity := range []struct{ key, want string }{{"taskId", execution.TaskID}, {"contextId", execution.ContextID}} {
		if value, present := message[identity.key]; present && kagentEvaluationString(value) != identity.want {
			return "", "", "", false
		}
	}
	text, _, ok := kagentEvaluationParts(message["parts"])
	return role, id, text, ok
}

func kagentEvaluationParts(raw json.RawMessage) (string, bool, bool) {
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return "", false, false
	}
	var texts []string
	for _, rawPart := range parts {
		part, ok := kagentEvaluationObject(rawPart, "kind", "text", "data", "metadata")
		if !ok {
			return "", false, false
		}
		switch kagentEvaluationString(part["kind"]) {
		case "text":
			if _, present := part["data"]; present {
				return "", false, false
			}
			var text string
			if value, present := part["text"]; !present || json.Unmarshal(value, &text) != nil {
				return "", false, false
			}
			texts = append(texts, text)
		case "data":
			if _, present := part["text"]; present {
				return "", false, false
			}
			var data map[string]json.RawMessage
			if json.Unmarshal(part["data"], &data) != nil || data == nil {
				return "", false, false
			}
		default:
			return "", false, false
		}
	}
	return strings.Join(texts, "\n"), len(texts) > 0, true
}
