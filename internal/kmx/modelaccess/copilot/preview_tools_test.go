package copilot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

const offeredTools = `[{"type":"function","function":{"name":"lookup","description":"Read a name","parameters":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}}}]`
const initialToolMessages = `[{"role":"system","content":"Use lookup"},{"role":"user","content":"Ada"}]`
const followupToolMessages = `[{"role":"system","content":"Use lookup"},{"role":"user","content":"Ada"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"name\":\"Ada\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"{\"name\":\"Ada\"}"}]`
const responsesToolReply = `{"model":"gpt-test","status":"completed","output":[{"type":"reasoning","id":"rs_test","encrypted_content":"private-reasoning","summary":[]},{"type":"function_call","id":"fc_test","call_id":"call_1","name":"lookup","arguments":"{\"name\":\"Ada\"}","status":"completed"}]}`

func toolRequest(messages string) string {
	return `{"model":"gpt-test","stream":false,"tools":` + offeredTools + `,"messages":` + messages + `}`
}
func toolCatalog(path string, supported bool) string {
	return strings.Replace(previewProtocolCatalog(path), `"type":"chat"`, fmt.Sprintf(`"type":"chat","supports":{"tool_calls":%t}`, supported), 1)
}

func toolFixture(t *testing.T, handler http.HandlerFunc) (*Provider, *memoryStore) {
	p, store := fixture(t, handler)
	store.token = canary
	return p, store
}

func TestResponsesEncoderRejectsMalformedToolCalls(t *testing.T) {
	for _, messages := range []string{
		strings.ReplaceAll(followupToolMessages, "call_1", ""),
		strings.Replace(followupToolMessages, `"name":"lookup"`, `"name":""`, 1),
		strings.Replace(followupToolMessages, `"type":"function"`, `"type":"other"`, 1),
		strings.Replace(followupToolMessages, `{\"name\":\"Ada\"}`, `not-json`, 1),
	} {
		if _, _, err := encodeResponsesRequest([]byte(toolRequest(messages))); !errors.Is(err, ErrMalformed) {
			t.Fatalf("malformed call forwarded: %v", err)
		}
	}
}

func TestPreviewToolRoundTrip(t *testing.T) {
	for _, path := range []string{"/chat/completions", "/responses"} {
		t.Run(path, func(t *testing.T) {
			sends := 0
			p, _ := toolFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					io.WriteString(w, toolCatalog(path, true))
					return
				}
				sends++
				if r.URL.Path != path {
					t.Error("changed protocol")
				}
				var body map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid request")
				}
				var tools []map[string]json.RawMessage
				if json.Unmarshal(body["tools"], &tools) != nil || len(tools) != 1 {
					t.Error("missing definitions")
				}
				if path == "/responses" {
					if tools[0]["function"] != nil || string(tools[0]["name"]) != `"lookup"` || tools[0]["parameters"] == nil || string(tools[0]["strict"]) != "false" || string(body["store"]) != "false" {
						t.Error("Chat tool not translated")
					}
					var items []map[string]json.RawMessage
					if json.Unmarshal(body["input"], &items) != nil {
						t.Error("invalid input")
					}
					if sends == 2 {
						if len(items) != 5 {
							t.Errorf("lost continuation: %s", body["input"])
						} else {
							if string(items[2]["encrypted_content"]) != `"private-reasoning"` || string(items[3]["call_id"]) != `"call_1"` || string(items[4]["type"]) != `"function_call_output"` || string(items[4]["call_id"]) != `"call_1"` || string(items[4]["output"]) != `"{\"name\":\"Ada\"}"` {
								t.Error("uncorrelated continuation/result")
							}
						}
					}
					if sends == 1 {
						io.WriteString(w, responsesToolReply)
					} else {
						io.WriteString(w, previewResponsesText("Hello Ada"))
					}
				} else {
					if tools[0]["function"] == nil {
						t.Error("Chat definition changed")
					}
					if sends == 1 {
						io.WriteString(w, `{"model":"gpt-test","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"name\":\"Ada\"}"}}]}}]}`)
					} else {
						io.WriteString(w, `{"model":"gpt-test","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hello Ada"}}]}`)
					}
				}
			})
			a := previewAccess(t, p)
			rt, err := a.Client(selected())
			if err != nil {
				t.Fatal(err)
			}
			for i, messages := range []string{initialToolMessages, followupToolMessages} {
				resp, err := rt.RoundTrip(chatRequest(t, toolRequest(messages)))
				if err != nil {
					t.Fatal(err)
				}
				b, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(b), "private-reasoning") || strings.Contains(string(b), "rs_test") {
					t.Fatal("private continuation exported")
				}
				if i == 0 && !strings.Contains(string(b), `"tool_calls"`) {
					t.Fatal("call lost")
				}
				if i == 1 && !strings.Contains(string(b), "Hello Ada") {
					t.Fatal("answer lost")
				}
			}
			if sends != 2 {
				t.Fatalf("sends=%d", sends)
			}
		})
	}
}

func TestPreviewToolsRequireCurrentCapability(t *testing.T) {
	for _, path := range []string{"/chat/completions", "/responses"} {
		t.Run(path, func(t *testing.T) {
			sends := 0
			p, _ := toolFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					io.WriteString(w, toolCatalog(path, false))
					return
				}
				sends++
				io.WriteString(w, previewResponsesText("hello"))
			})
			a := previewAccess(t, p)
			_, err := previewSend(t, a, toolRequest(initialToolMessages))
			if !errors.Is(err, ErrToolsUnsupported) || sends != 0 {
				t.Fatalf("unsupported tool route sent: %v (%d)", err, sends)
			}
			if _, err = previewSend(t, a, previewBody); !errors.Is(err, ErrToolsUnsupported) || sends != 0 {
				t.Fatal("refusal did not stop later sends")
			}
		})
	}
}

func TestPreviewResponsesToolContinuationIsInvocationPrivate(t *testing.T) {
	for _, kind := range []string{"missing", "input", "call-id", "arguments", "credential"} {
		t.Run(kind, func(t *testing.T) {
			sends := 0
			p, _ := toolFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					io.WriteString(w, toolCatalog("/responses", true))
					return
				}
				sends++
				reply := responsesToolReply
				if kind == "credential" {
					reply = strings.Replace(reply, "private-reasoning", encoded(canary), 1)
				}
				io.WriteString(w, reply)
			})
			a := previewAccess(t, p)
			rt, err := a.Client(selected())
			if err != nil {
				t.Fatal(err)
			}
			if kind != "missing" {
				resp, err := rt.RoundTrip(chatRequest(t, toolRequest(initialToolMessages)))
				if kind == "credential" {
					if !errors.Is(err, ErrCredentialContent) || resp != nil {
						t.Fatalf("private credential escaped: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
			}
			messages := followupToolMessages
			switch kind {
			case "input":
				messages = strings.Replace(messages, `"content":"Ada"`, `"content":"Grace"`, 1)
			case "call-id":
				messages = strings.ReplaceAll(messages, "call_1", "call_other")
			case "arguments":
				messages = strings.Replace(messages, `\"Ada\"`, `\"Grace\"`, 1)
			}
			resp, err := rt.RoundTrip(chatRequest(t, toolRequest(messages)))
			wantSends := 1
			if kind == "missing" {
				wantSends = 0
			}
			if !errors.Is(err, ErrMalformed) || resp != nil || sends != wantSends {
				t.Fatalf("unmatched continuation sent: err=%v sends=%d", err, sends)
			}
		})
	}
}
