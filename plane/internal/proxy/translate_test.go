package proxy_test

// The seam accepting the protocol a framework speaks and reaching an
// endpoint that speaks the other one.
//
// Every test here drives the real mux against an httptest endpoint, so
// what is asserted is what a client would receive and what the endpoint
// would receive — never the translator's own opinion of either.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// translating is the upstream shape this whole file is about: the door
// is `v1/responses`, the forwarded path is `v1/chat/completions`.
func translating(baseURL string) map[string]config.Upstream {
	return map[string]config.Upstream{
		"orka": {BaseURL: baseURL, Path: "v1/chat/completions", ClientPath: "v1/responses",
			Protocol: config.ProtocolChatCompletions, Classification: config.ClassFree},
	}
}

// chatAnswer is one plain chat completion, as an endpoint that speaks
// only chat completions would send it.
const chatAnswer = `{"id": "cmpl-7", "object": "chat.completion", "created": 1730000000,
  "model": "qwen2.5:3b",
  "choices": [{"index": 0, "finish_reason": "stop",
               "message": {"role": "assistant", "content": "three scoops"}}],
  "usage": {"prompt_tokens": 11, "completion_tokens": 16, "total_tokens": 27}}`

// The whole point, end to end: what the framework sends arrives at the
// endpoint in the shape the endpoint serves, and what comes back is
// something the framework's own SDK can parse.
func TestAResponsesRequestReachesAChatCompletionsEndpointAndComesBackAsAResponse(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "concierge"})
	var forwarded map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &forwarded))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, chatAnswer)
	}))
	t.Cleanup(srv.Close)

	mux := proxy.NewDataMux(testDeps(f, translating(srv.URL)))
	w := doChat(t, mux, "tok", "/upstream/orka/v1/responses",
		`{"model": "local/qwen2.5:3b", "instructions": "You are a concierge.",
		  "input": [{"role": "user", "content": [{"type": "input_text", "text": "how many scoops?"}]}],
		  "metadata": {"session": "s1"}, "max_output_tokens": 256, "store": false,
		  "include": ["reasoning.encrypted_content"]}`)
	require.Equal(t, 200, w.Code)

	// What the endpoint saw: chat completions, with the instructions as
	// the system turn and no Responses-only field left in the body.
	require.Equal(t, "local/qwen2.5:3b", forwarded["model"])
	require.NotContains(t, forwarded, "input")
	require.NotContains(t, forwarded, "instructions")
	require.NotContains(t, forwarded, "max_output_tokens")
	// `include` is accepted and forwarded as nothing: it asks for output a
	// chat completion never carries, and the framework this seam exists
	// for sends it on every request whatever model it is talking to.
	require.NotContains(t, forwarded, "include")
	require.EqualValues(t, 256, forwarded["max_tokens"])
	messages := forwarded["messages"].([]any)
	require.Len(t, messages, 2)
	require.Equal(t, "system", messages[0].(map[string]any)["role"])
	require.Equal(t, "You are a concierge.", messages[0].(map[string]any)["content"])
	require.Equal(t, "user", messages[1].(map[string]any)["role"])
	require.Equal(t, "how many scoops?", messages[1].(map[string]any)["content"])

	// What the client saw: a Responses envelope.
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "response", got["object"])
	require.Equal(t, "completed", got["status"])
	require.Equal(t, "qwen2.5:3b", got["model"])
	require.Equal(t, map[string]any{"session": "s1"}, got["metadata"])
	require.Equal(t, "You are a concierge.", got["instructions"])
	output := got["output"].([]any)
	require.Len(t, output, 1)
	message := output[0].(map[string]any)
	require.Equal(t, "message", message["type"])
	require.Equal(t, "assistant", message["role"])
	content := message["content"].([]any)[0].(map[string]any)
	require.Equal(t, "output_text", content["type"])
	require.Equal(t, "three scoops", content["text"])
	// Usage under the Responses API's own names, from the counts the
	// endpoint reported under the other API's names.
	usage := got["usage"].(map[string]any)
	require.EqualValues(t, 11, usage["input_tokens"])
	require.EqualValues(t, 16, usage["output_tokens"])

	// And the row is the endpoint's counts, not the envelope's.
	require.Len(t, f.ledger, 1)
	require.Equal(t, int64(11), f.ledger[0].InputTokens)
	require.Equal(t, int64(16), f.ledger[0].OutputTokens)
	require.Equal(t, "free", f.ledger[0].CostSource)
}

// A tool-calling turn is the one that decides whether a real agent can
// complete a conversation through this seam: the call goes out in one
// API's shape, the result comes back in the other's, and the identifiers
// have to survive both crossings.
func TestAToolCallingTurnSurvivesTheTranslation(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "concierge"})
	var forwarded map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &forwarded))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id": "cmpl-9", "created": 1, "model": "m",
		  "choices": [{"finish_reason": "tool_calls", "message": {"role": "assistant", "content": null,
		     "tool_calls": [{"id": "call_a", "type": "function",
		                     "function": {"name": "quote_order", "arguments": "{\"size\":\"large\"}"}}]}}],
		  "usage": {"prompt_tokens": 40, "completion_tokens": 12}}`)
	}))
	t.Cleanup(srv.Close)

	mux := proxy.NewDataMux(testDeps(f, translating(srv.URL)))
	w := doChat(t, mux, "tok", "/upstream/orka/v1/responses",
		`{"model": "m",
		  "input": [{"role": "user", "content": "one large please"},
		            {"type": "function_call", "call_id": "call_z", "name": "list_menu", "arguments": "{}"},
		            {"type": "function_call_output", "call_id": "call_z", "output": "vanilla, pistachio"}],
		  "tools": [{"type": "function", "name": "quote_order", "description": "price a sundae",
		             "parameters": {"type": "object", "properties": {}}}],
		  "tool_choice": "auto"}`)
	require.Equal(t, 200, w.Code)

	// Going out: the standalone function_call became an assistant turn
	// carrying the call, and its output became a tool message keyed by
	// the same id.
	messages := forwarded["messages"].([]any)
	require.Len(t, messages, 3)
	assistant := messages[1].(map[string]any)
	require.Equal(t, "assistant", assistant["role"])
	call := assistant["tool_calls"].([]any)[0].(map[string]any)
	require.Equal(t, "call_z", call["id"])
	require.Equal(t, "list_menu", call["function"].(map[string]any)["name"])
	toolResult := messages[2].(map[string]any)
	require.Equal(t, "tool", toolResult["role"])
	require.Equal(t, "call_z", toolResult["tool_call_id"])
	require.Equal(t, "vanilla, pistachio", toolResult["content"])
	// The tool declaration is nested where chat completions expects it.
	declared := forwarded["tools"].([]any)[0].(map[string]any)
	require.Equal(t, "function", declared["type"])
	require.Equal(t, "quote_order", declared["function"].(map[string]any)["name"])

	// Coming back: a function_call item, keyed by the id the endpoint
	// chose, so the framework can answer it on the next turn.
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	item := got["output"].([]any)[0].(map[string]any)
	require.Equal(t, "function_call", item["type"])
	require.Equal(t, "call_a", item["call_id"])
	require.Equal(t, "quote_order", item["name"])
	require.Equal(t, `{"size":"large"}`, item["arguments"])
}

// A Responses envelope restates the request it answers, and a client is
// entitled to continue from it. An envelope that answered `tool_choice:
// auto` to a request that named a function would be a lie about what was
// asked, told by the seam rather than by either end.
func TestTheEnvelopeRestatesWhatTheClientActuallyAskedFor(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "concierge"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, chatAnswer)
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, translating(srv.URL)))
	w := doChat(t, mux, "tok", "/upstream/orka/v1/responses",
		`{"model": "m", "input": "one large please",
		  "tools": [{"type": "function", "name": "quote_order", "parameters": {"type": "object"}}],
		  "tool_choice": {"type": "function", "name": "quote_order"},
		  "parallel_tool_calls": false, "max_output_tokens": 64, "temperature": 0.2, "top_p": 0.9,
		  "user": "ada", "instructions": "You are a concierge.",
		  "text": {"format": {"type": "json_object"}}}`)
	require.Equal(t, 200, w.Code)

	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, map[string]any{"type": "function", "name": "quote_order"}, got["tool_choice"])
	require.Equal(t, false, got["parallel_tool_calls"])
	require.EqualValues(t, 64, got["max_output_tokens"])
	require.EqualValues(t, 0.2, got["temperature"])
	require.EqualValues(t, 0.9, got["top_p"])
	require.Equal(t, "ada", got["user"])
	require.Equal(t, "You are a concierge.", got["instructions"])
	require.Equal(t, map[string]any{"format": map[string]any{"type": "json_object"}}, got["text"])
	tools := got["tools"].([]any)
	require.Len(t, tools, 1)
	require.Equal(t, "quote_order", tools[0].(map[string]any)["name"])

	// And a request that said none of it gets the defaults its silence
	// means, not the previous request's values.
	w = doChat(t, mux, "tok", "/upstream/orka/v1/responses", `{"model": "m", "input": "hi"}`)
	require.Equal(t, 200, w.Code)
	got = map[string]any{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "auto", got["tool_choice"])
	require.Equal(t, true, got["parallel_tool_calls"])
	require.Equal(t, []any{}, got["tools"])
	require.Nil(t, got["temperature"])
	require.Nil(t, got["instructions"])
	require.Equal(t, map[string]any{"format": map[string]any{"type": "text"}}, got["text"])
}

// A model that answers with text AND tool calls is ONE assistant turn.
// The Responses API sends it as two items; splitting it back into two
// assistant messages is a shape some OpenAI-compatible servers reject and
// others render into the prompt as two speeches nobody made.
func TestTextAndToolCallsInOneTurnStayOneAssistantMessage(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "concierge"})
	var forwarded map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &forwarded))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, chatAnswer)
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, translating(srv.URL)))
	w := doChat(t, mux, "tok", "/upstream/orka/v1/responses",
		`{"model": "m", "input": [
		   {"role": "user", "content": "one large please"},
		   {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "let me look"}]},
		   {"type": "function_call", "call_id": "call_z", "name": "quote_order", "arguments": "{}"},
		   {"type": "function_call_output", "call_id": "call_z", "output": "9.50"}]}`)
	require.Equal(t, 200, w.Code)

	messages := forwarded["messages"].([]any)
	require.Len(t, messages, 3, "the assistant's text and its tool call are one message, not two")
	assistant := messages[1].(map[string]any)
	require.Equal(t, "assistant", assistant["role"])
	require.Equal(t, "let me look", assistant["content"])
	require.Equal(t, "call_z", assistant["tool_calls"].([]any)[0].(map[string]any)["id"])
	require.Equal(t, "tool", messages[2].(map[string]any)["role"])
}

// Refusing by name is the rule this translator is built on: a field it
// cannot carry is an error naming the field, never a field dropped on
// the way past. Each of these would otherwise be a request answered as
// if it had asked for something else.
func TestAFieldTheTranslationCannotCarryIsRefusedByName(t *testing.T) {
	for _, tc := range []struct{ name, body, says string }{
		{"a field this seam has no translation for",
			`{"model": "m", "input": "hi", "conversation": "conv_1"}`, `"conversation"`},
		{"several unknown fields, named together and in the same order every time",
			`{"model": "m", "input": "hi", "zebra": 1, "aardvark": 2}`, `"aardvark", "zebra"`},
		{"a member of an object this seam looks inside",
			`{"model": "m", "input": "hi", "reasoning": {"effort": "low", "summary": "detailed"}}`,
			`"summary" inside "reasoning"`},
		{"a member of the text object",
			`{"model": "m", "input": "hi", "text": {"verbosity": "low"}}`, `"verbosity" inside "text"`},
		{"a member of a tool declaration",
			`{"model": "m", "input": "hi", "tools": [{"type": "function", "name": "t", "container": "x"}]}`,
			`"container" on a tool declaration`},
		{"a member of an input item",
			`{"model": "m", "input": [{"type": "message", "role": "user", "content": "hi", "encrypted": "x"}]}`,
			`"encrypted" on an input item`},
		{"extra output a chat completion does not carry",
			`{"model": "m", "input": "hi", "include": ["message.output_text.logprobs"]}`,
			"message.output_text.logprobs"},
		{"a conversation this seam holds no state for",
			`{"model": "m", "input": "hi", "previous_response_id": "resp_1"}`, "previous_response_id"},
		{"a response it is asked to keep",
			`{"model": "m", "input": "hi", "store": true}`, "keeps none"},
		{"an input item of a type it cannot express",
			`{"model": "m", "input": [{"type": "file_search_call", "id": "fs_1"}]}`, "file_search_call"},
		{"a content part that is not text",
			`{"model": "m", "input": [{"role": "user", "content": [{"type": "input_image", "image_url": "x"}]}]}`,
			"input_image"},
		{"a tool that is not a function",
			`{"model": "m", "input": "hi", "tools": [{"type": "web_search_preview"}]}`, "web_search_preview"},
		{"no model at all", `{"input": "hi"}`, "names no model"},
		{"no input at all", `{"model": "m"}`, "carries no input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeStore()
			f.addToken("tok", store.Credential{Name: "concierge"})
			reached := false
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
			t.Cleanup(srv.Close)
			mux := proxy.NewDataMux(testDeps(f, translating(srv.URL)))
			w := doChat(t, mux, "tok", "/upstream/orka/v1/responses", tc.body)
			require.Equal(t, 400, w.Code)
			require.Contains(t, w.Body.String(), tc.says)
			require.False(t, reached, "the endpoint must not be called for a request that could not be translated")
			// A refusal is still a row: the trail says a call was
			// attempted under this credential and denied.
			require.Len(t, f.ledger, 1)
			require.Equal(t, 400, f.ledger[0].Status)
		})
	}
}

// A serialiser that writes its unset optionals as `null` is saying
// nothing about them. Reading that as a value turns silence into a 400
// for a field the client never set.
func TestAnExplicitNullIsAFieldTheClientDidNotSet(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "concierge"})
	var forwarded map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &forwarded))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, chatAnswer)
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, translating(srv.URL)))
	w := doChat(t, mux, "tok", "/upstream/orka/v1/responses",
		`{"model": "m", "input": "hi", "tool_choice": null, "tools": null,
		  "metadata": null, "text": null, "instructions": null, "temperature": null}`)
	require.Equal(t, 200, w.Code)
	require.NotContains(t, forwarded, "tool_choice")
	require.NotContains(t, forwarded, "temperature")

	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "auto", got["tool_choice"], "a null field takes the default, not the client's null")
	require.Equal(t, map[string]any{}, got["metadata"])
	require.Equal(t, map[string]any{"format": map[string]any{"type": "text"}}, got["text"])
}

// A stream is refused BEFORE the endpoint is called. Half a translation
// cannot be taken back once the first byte has left, so the refusal has
// to happen while a refusal is still possible.
func TestAStreamedRequestIsRefusedOnATranslatingUpstream(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "concierge"})
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, translating(srv.URL)))
	w := doChat(t, mux, "tok", "/upstream/orka/v1/responses",
		`{"model": "m", "input": "hi", "stream": true}`)
	require.Equal(t, 400, w.Code)
	require.Contains(t, w.Body.String(), "does not carry a stream")
	require.False(t, reached)
}

// The door is the client path and nothing else. The forwarded path is
// not a second way in: one (method, path) per upstream is still the
// whole blast radius.
func TestTheForwardedPathIsNotASecondDoor(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "concierge"})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("nothing should reach the endpoint")
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, translating(srv.URL)))
	w := doChat(t, mux, "tok", "/upstream/orka/v1/chat/completions", chatBody)
	require.Equal(t, 403, w.Code)
	require.Contains(t, w.Body.String(), "path not allowed")
}

// An answer this seam cannot rewrite fails closed — and the row still
// carries the counts, because the call was made and the tokens were
// spent whatever happened to the reply afterwards.
func TestAnUntranslatableAnswerFailsClosedAndIsStillLedgered(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "concierge"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Metered, and unusable: a 200 with usage and no choice at all.
		_, _ = fmt.Fprint(w, `{"id": "cmpl-1", "created": 1, "model": "m", "choices": [],
		  "usage": {"prompt_tokens": 5, "completion_tokens": 0}}`)
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, translating(srv.URL)))
	w := doChat(t, mux, "tok", "/upstream/orka/v1/responses", `{"model": "m", "input": "hi"}`)
	require.Equal(t, 502, w.Code)
	require.Contains(t, w.Body.String(), "could not translate back")
	require.Len(t, f.ledger, 1)
	require.Equal(t, int64(5), f.ledger[0].InputTokens)
	require.Equal(t, 502, f.ledger[0].Status)
}

// An error from the endpoint is relayed as it stands. Both APIs carry an
// error the same way, and rewriting a diagnosis is how a diagnosis
// becomes a parse failure.
func TestAnEndpointErrorIsRelayedUntranslated(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "concierge"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error": {"message": "no provider \"\" found", "type": "invalid_request_error"}}`)
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, translating(srv.URL)))
	w := doChat(t, mux, "tok", "/upstream/orka/v1/responses", `{"model": "m", "input": "hi"}`)
	require.Equal(t, 404, w.Code)
	require.Contains(t, w.Body.String(), "no provider")
	require.Len(t, f.ledger, 1)
	require.Equal(t, 404, f.ledger[0].Status)
}

// A turn cut at the token bound says so in the envelope. Without this a
// truncated reply is indistinguishable from a model that answered badly.
func TestATurnCutAtTheBoundIsReportedAsIncomplete(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "concierge"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id": "c", "created": 1, "model": "m",
		  "choices": [{"finish_reason": "length", "message": {"role": "assistant", "content": "three sco"}}],
		  "usage": {"prompt_tokens": 4, "completion_tokens": 3}}`)
	}))
	t.Cleanup(srv.Close)
	mux := proxy.NewDataMux(testDeps(f, translating(srv.URL)))
	w := doChat(t, mux, "tok", "/upstream/orka/v1/responses", `{"model": "m", "input": "hi"}`)
	require.Equal(t, 200, w.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "incomplete", got["status"])
	require.Equal(t, map[string]any{"reason": "max_output_tokens"}, got["incomplete_details"])
}
