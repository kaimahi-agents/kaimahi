package proxy

// Translating the Responses API onto chat completions, at the seam.
//
// Why this is here at all: the wire shape a framework SENDS and the wire
// shape an endpoint SERVES have stopped being the same thing. Microsoft
// Agent Framework's OpenAI chat client is the Responses client and posts
// `v1/responses`; several endpoints — including the orchestration platform
// this seam was built to sit in front of — serve chat completions and have
// no Responses route at all. Neither side can be reconfigured: the
// framework offers no switch, and the endpoint's route table is not the
// adopter's to change. Everything in between is a 404, or worse — one
// endpoint answers the unrouted path with its dashboard's HTML under a
// 200, which its own metrics then count as a success.
//
// So the translation happens in the one place that is already reading both
// bodies: the seam that meters them.
//
// Three rules hold this file together, and they are the same three the
// meter is built on:
//
//  1. REFUSE WHAT CANNOT BE HONOURED. Every field this translator does not
//     understand is an error naming the field, not a field dropped on the
//     floor. A request that asked for stored state, or for a modality this
//     cannot express, comes back as a 400 that says so — because the
//     alternative is an answer that silently was not what was asked for.
//     The set below grew from what a real framework actually sends; it is
//     meant to grow the same way.
//
//  2. THE METER IS NOT IN THE PATH OF THE TRANSLATION. Token counts are
//     read out of the upstream's own body under the upstream's own
//     protocol, before or after this file runs and never through it. A
//     translation bug can produce a wrong ANSWER; it cannot produce a
//     wrong ROW.
//
//  3. NOTHING IS INVENTED. Every value in the envelope going back is one
//     the upstream reported, one the client itself sent — echoed verbatim,
//     see translatedRequest — or a constant that states this seam's own
//     behaviour (`"store": false` — it stores nothing; `"truncation":
//     "disabled"` — it truncates nothing).

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// responsesRequest is the half of a Responses request this seam
// understands. Decoding into a struct rather than a map is deliberate:
// the fields named here are the contract, and the scans against
// requestFields and nestedFields report everything else BY NAME rather
// than letting it through.
type responsesRequest struct {
	Model             string          `json:"model"`
	Input             json.RawMessage `json:"input"`
	Instructions      *string         `json:"instructions"`
	Tools             []responsesTool `json:"tools"`
	ToolChoice        json.RawMessage `json:"tool_choice"`
	Temperature       *float64        `json:"temperature"`
	TopP              *float64        `json:"top_p"`
	MaxOutputTokens   *int64          `json:"max_output_tokens"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls"`
	Metadata          json.RawMessage `json:"metadata"`
	User              *string         `json:"user"`
	Text              *responsesText  `json:"text"`
	Reasoning         *struct {
		Effort *string `json:"effort"`
	} `json:"reasoning"`
	Truncation         *string  `json:"truncation"`
	Store              *bool    `json:"store"`
	Stream             *bool    `json:"stream"`
	PreviousResponseID *string  `json:"previous_response_id"`
	Include            []string `json:"include"`
}

// includeNothingAChatCompletionCarries are the `include` values this seam
// accepts, and the list is one entry long on purpose.
//
// `include` asks for OPTIONAL extra output. A Responses endpoint omits
// what the model did not produce, and the client is built to cope with
// that — Microsoft Agent Framework appends `reasoning.encrypted_content`
// to EVERY request it makes, whatever model it is talking to, so refusing
// it would mean no application built on that framework could ever use
// this seam. A chat completion carries no reasoning item at all, so
// answering with none of it is the same answer that endpoint would give
// for a model that does no reasoning — not a field dropped in transit.
//
// Everything else is refused by name. Some of the other values name data
// chat completions genuinely could carry (logprobs) and some name results
// of tools this seam does not carry at all; either way, quietly returning
// an answer without what was asked for is the failure this file exists to
// remove.
var includeNothingAChatCompletionCarries = map[string]bool{
	"reasoning.encrypted_content": true,
}

// responsesTool is a Responses tool declaration: flat, where chat
// completions nests the same four fields under "function".
type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict"`
}

type responsesText struct {
	Format json.RawMessage `json:"format"`
}

// translatedRequest is what the forwarded body is, plus what the answer
// has to carry back.
//
// A Responses envelope restates the request it answers — its
// `instructions`, its `tools`, its `tool_choice`, its sampling settings —
// and clients are entitled to read them back, because the API is designed
// so the response object alone is enough to continue from. So this is not
// bookkeeping: an envelope that answered "tool_choice: auto" to a request
// that said "call quote_order" would be a lie about what was asked, told
// by the seam rather than by either end.
type translatedRequest struct {
	Body []byte
	// Echoed are the client's own values for the keys the envelope
	// restates, exactly as they arrived. Absent keys take the documented
	// default in echo(), which is a statement about the request rather
	// than an invention.
	Echoed map[string]json.RawMessage
}

// echo returns the client's value for a restated key, or the default that
// says what the request meant by leaving it out.
func (t translatedRequest) echo(key string, missing any) any {
	if raw, ok := t.Echoed[key]; ok && sent(raw) {
		return raw
	}
	return missing
}

// sent reports whether a raw field carries a value the client actually
// set. An explicit `null` does not: it is how a serialiser writes an
// unset optional, and treating it as a value turns "I said nothing about
// this" into a refusal.
func sent(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

// requestFields is every top-level key a Responses request may carry
// through this seam. A key outside it is refused by name.
var requestFields = []string{
	"model", "input", "instructions", "tools", "tool_choice", "temperature",
	"top_p", "max_output_tokens", "parallel_tool_calls", "metadata", "user",
	"text", "reasoning", "truncation", "store", "stream", "previous_response_id",
	"include",
}

// echoedFields are the keys the answer restates from the request. They are
// a subset of requestFields and are echoed VERBATIM: reformatting a
// client's own value on the way back would be a second place for the two
// APIs to disagree.
var echoedFields = []string{
	"instructions", "tools", "tool_choice", "temperature", "top_p",
	"max_output_tokens", "parallel_tool_calls", "metadata", "user", "text",
}

// nestedFields are the objects this translator looks inside, and every
// member it understands there. The top-level scan is not enough on its
// own: `reasoning: {"summary": "detailed"}` and `text: {"verbosity":
// "low"}` are real, current fields, and each would decode into a struct
// that ignores them — dropping in transit exactly what the top-level
// scan exists to refuse.
var nestedFields = map[string][]string{
	"reasoning": {"effort"},
	"text":      {"format"},
}

// toolFields, itemFields and partFields are the same rule one level
// further in. `id` and `status` are accepted and ignored on an input item
// deliberately: a client replaying its own conversation sends back the
// identifiers this seam gave it, and they carry no instruction.
var (
	toolFields = []string{"type", "name", "description", "parameters", "strict"}
	itemFields = []string{"type", "role", "content", "call_id", "name", "arguments", "output", "id", "status"}
	partFields = []string{"type", "text", "annotations"}
)

// unknownIn names every member of an object this translator would not
// act on, sorted, so a refusal names the same field every time it is
// asked rather than whichever one Go's map iteration reached first.
func unknownIn(raw json.RawMessage, known []string) []string {
	var object map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &object) != nil {
		return nil
	}
	var unknown []string
	for key := range object {
		if !slices.Contains(known, key) {
			unknown = append(unknown, key)
		}
	}
	slices.Sort(unknown)
	return unknown
}

// responsesToChat rewrites a Responses request body as a chat completions
// request body.
func responsesToChat(body []byte) (translatedRequest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return translatedRequest{}, fmt.Errorf("request body is not a JSON object")
	}
	if unknown := unknownIn(body, requestFields); len(unknown) > 0 {
		return translatedRequest{}, fmt.Errorf("this seam translates the Responses API onto chat completions "+
			"and has no translation for %s — refused rather than forwarded without it", quoteEach(unknown))
	}
	for object, known := range nestedFields {
		if unknown := unknownIn(raw[object], known); len(unknown) > 0 {
			return translatedRequest{}, fmt.Errorf("this seam has no translation for %s inside %q — "+
				"refused rather than forwarded without it", quoteEach(unknown), object)
		}
	}
	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return translatedRequest{}, fmt.Errorf("request body is not a Responses request: %v", err)
	}
	if strings.TrimSpace(req.Model) == "" {
		return translatedRequest{}, fmt.Errorf("request names no model")
	}
	if req.PreviousResponseID != nil && *req.PreviousResponseID != "" {
		return translatedRequest{}, fmt.Errorf("previous_response_id asks this seam to continue a conversation it " +
			"holds no state for — send the whole conversation in `input` instead")
	}
	if req.Store != nil && *req.Store {
		return translatedRequest{}, fmt.Errorf("store: true asks this seam to keep the response; it keeps none " +
			"(the ledger records counts and a cost, never content)")
	}
	for _, want := range req.Include {
		if !includeNothingAChatCompletionCarries[want] {
			return translatedRequest{}, fmt.Errorf("include %q asks for output a chat completion does not carry, "+
				"and this seam will not answer without it and say nothing", want)
		}
	}
	if req.Truncation != nil && *req.Truncation != "" && *req.Truncation != "disabled" {
		return translatedRequest{}, fmt.Errorf("truncation %q is not something this seam can carry onto chat "+
			"completions", *req.Truncation)
	}

	messages, err := inputMessages(req.Input, req.Instructions)
	if err != nil {
		return translatedRequest{}, err
	}
	out := map[string]any{"model": req.Model, "messages": messages}
	if len(req.Tools) > 0 {
		tools, err := chatTools(raw["tools"])
		if err != nil {
			return translatedRequest{}, err
		}
		out["tools"] = tools
	}
	// An explicit `null` is a field the client did NOT set. Most of the
	// optional fields here decode to a nil pointer either way, but a
	// json.RawMessage holds the four bytes `null` and would otherwise be
	// carried into chatToolChoice, which would refuse it as an object with
	// no type — a 400 for a field nobody set. Plenty of serialisers write
	// their unset optionals out as null.
	if sent(req.ToolChoice) {
		choice, err := chatToolChoice(req.ToolChoice)
		if err != nil {
			return translatedRequest{}, err
		}
		out["tool_choice"] = choice
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if req.MaxOutputTokens != nil {
		// The same bound under the other API's name. `max_tokens` is
		// deprecated in favour of `max_completion_tokens` on newer
		// OpenAI-compatible servers, but it is the one every server that
		// speaks this protocol accepts, including the ones this seam
		// exists to reach.
		out["max_tokens"] = *req.MaxOutputTokens
	}
	if req.ParallelToolCalls != nil {
		out["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if req.User != nil {
		out["user"] = *req.User
	}
	if req.Reasoning != nil && req.Reasoning.Effort != nil {
		out["reasoning_effort"] = *req.Reasoning.Effort
	}
	if req.Text != nil && len(req.Text.Format) > 0 {
		format, err := chatResponseFormat(req.Text.Format)
		if err != nil {
			return translatedRequest{}, err
		}
		if format != nil {
			out["response_format"] = format
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return translatedRequest{}, err
	}
	echoed := make(map[string]json.RawMessage, len(echoedFields))
	for _, key := range echoedFields {
		if value, ok := raw[key]; ok {
			echoed[key] = value
		}
	}
	return translatedRequest{Body: encoded, Echoed: echoed}, nil
}

// quoteEach renders a refusal's field list, so one unknown field reads
// as `"include"` and three read as `"a", "b", "c"` rather than as
// whichever one was reached first.
func quoteEach(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, strconv.Quote(n))
	}
	return strings.Join(quoted, ", ")
}

// inputMessages turns `instructions` plus `input` into a chat message
// list. `input` is either a bare string — the whole user turn — or the
// conversation as a list of items.
func inputMessages(input json.RawMessage, instructions *string) ([]map[string]any, error) {
	var messages []map[string]any
	if instructions != nil && *instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": *instructions})
	}
	if len(input) == 0 {
		return nil, fmt.Errorf("request carries no input")
	}
	var text string
	if json.Unmarshal(input, &text) == nil {
		return append(messages, map[string]any{"role": "user", "content": text}), nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, fmt.Errorf("input must be a string or a list of items")
	}
	// Consecutive function_call items are one assistant turn on this
	// side of the translation: chat completions hangs tool calls off an
	// assistant message, where the Responses API lets each one stand
	// alone. Merging the run — rather than emitting one assistant
	// message per call — is what keeps a parallel tool call parallel.
	//
	// The run is also attached to an assistant message that came
	// immediately before it, rather than emitted as a second assistant
	// turn. A model that answers with text AND tool calls is one turn on
	// both sides of this translation, and the Responses API sends it as
	// two items; splitting it into two assistant messages is a shape some
	// OpenAI-compatible servers reject and others render into the prompt
	// as two speeches nobody made.
	var pending []map[string]any
	flush := func() {
		if len(pending) == 0 {
			return
		}
		if n := len(messages) - 1; n >= 0 {
			if last := messages[n]; last["role"] == "assistant" && last["tool_calls"] == nil {
				last["tool_calls"] = pending
				pending = nil
				return
			}
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": nil, "tool_calls": pending})
		pending = nil
	}
	for i, item := range items {
		converted, calls, err := inputItem(item)
		if err != nil {
			return nil, fmt.Errorf("input item %d: %w", i, err)
		}
		if calls != nil {
			pending = append(pending, calls)
			continue
		}
		flush()
		messages = append(messages, converted)
	}
	flush()
	if len(messages) == 0 {
		return nil, fmt.Errorf("request carries no input")
	}
	return messages, nil
}

// inputItem converts one item. It returns either a message or one tool
// call to be merged into the assistant turn being accumulated.
func inputItem(raw json.RawMessage) (message map[string]any, toolCall map[string]any, err error) {
	var item struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		CallID  string          `json:"call_id"`
		Name    string          `json:"name"`
		Args    string          `json:"arguments"`
		Output  json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, nil, fmt.Errorf("want an object, got %s", firstBytes(raw))
	}
	// The type is checked first so an item this seam cannot express is
	// refused by its type rather than by whichever of its members the
	// scan below happened to see.
	switch item.Type {
	case "function_call", "function_call_output", "", "message":
		if unknown := unknownIn(raw, itemFields); len(unknown) > 0 {
			return nil, nil, fmt.Errorf("no translation for %s on an input item", quoteEach(unknown))
		}
	default:
		return nil, nil, fmt.Errorf("no translation for an input item of type %q", item.Type)
	}
	switch item.Type {
	case "function_call":
		if item.CallID == "" || item.Name == "" {
			return nil, nil, fmt.Errorf("a function_call needs call_id and name")
		}
		return nil, map[string]any{
			"id": item.CallID, "type": "function",
			"function": map[string]any{"name": item.Name, "arguments": item.Args},
		}, nil
	case "function_call_output":
		if item.CallID == "" {
			return nil, nil, fmt.Errorf("a function_call_output needs call_id")
		}
		// The output is a string on both sides, but a client that sent
		// structured JSON gets it forwarded as the JSON it sent rather
		// than as Go's rendering of a decoded value.
		content := string(item.Output)
		var s string
		if json.Unmarshal(item.Output, &s) == nil {
			content = s
		}
		return map[string]any{"role": "tool", "tool_call_id": item.CallID, "content": content}, nil, nil
	case "", "message":
		if item.Role == "" {
			return nil, nil, fmt.Errorf("a message needs a role")
		}
		content, err := messageContent(item.Content)
		if err != nil {
			return nil, nil, err
		}
		role := item.Role
		if role == "developer" {
			// chat completions has no developer role; system is what it
			// was called before the Responses API renamed it.
			role = "system"
		}
		return map[string]any{"role": role, "content": content}, nil, nil
	}
	return nil, nil, fmt.Errorf("no translation for an input item of type %q", item.Type)
}

// messageContent flattens the content of one message. Text is the whole
// vocabulary this seam carries; anything else is refused by name rather
// than dropped, because a dropped image is a question answered about
// something the model never saw.
func messageContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("a message needs content")
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("message content must be a string or a list of parts")
	}
	var b strings.Builder
	for _, rawPart := range parts {
		var part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(rawPart, &part); err != nil {
			return "", fmt.Errorf("a content part must be an object")
		}
		switch part.Type {
		case "input_text", "output_text", "text":
			if unknown := unknownIn(rawPart, partFields); len(unknown) > 0 {
				return "", fmt.Errorf("no translation for %s on a content part", quoteEach(unknown))
			}
			b.WriteString(part.Text)
		default:
			return "", fmt.Errorf("no translation for a content part of type %q", part.Type)
		}
	}
	return b.String(), nil
}

func chatTools(rawTools json.RawMessage) ([]map[string]any, error) {
	var declarations []json.RawMessage
	if err := json.Unmarshal(rawTools, &declarations); err != nil {
		return nil, fmt.Errorf("tools must be a list of tool declarations")
	}
	tools := make([]responsesTool, 0, len(declarations))
	for _, declaration := range declarations {
		if unknown := unknownIn(declaration, toolFields); len(unknown) > 0 {
			return nil, fmt.Errorf("no translation for %s on a tool declaration", quoteEach(unknown))
		}
		var t responsesTool
		if err := json.Unmarshal(declaration, &t); err != nil {
			return nil, fmt.Errorf("a tool declaration must be an object")
		}
		tools = append(tools, t)
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		if t.Type != "function" {
			return nil, fmt.Errorf("no translation for a tool of type %q — this seam carries function tools", t.Type)
		}
		if t.Name == "" {
			return nil, fmt.Errorf("a function tool needs a name")
		}
		fn := map[string]any{"name": t.Name}
		if t.Description != nil {
			fn["description"] = *t.Description
		}
		if len(t.Parameters) > 0 {
			fn["parameters"] = t.Parameters
		}
		if t.Strict != nil {
			fn["strict"] = *t.Strict
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out, nil
}

// chatToolChoice carries the choice across. The two APIs agree on the
// three words and disagree on the object: theirs names the function
// inline, chat completions nests it.
func chatToolChoice(raw json.RawMessage) (any, error) {
	var word string
	if json.Unmarshal(raw, &word) == nil {
		switch word {
		case "auto", "none", "required":
			return word, nil
		}
		return nil, fmt.Errorf("no translation for tool_choice %q", word)
	}
	var object struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("tool_choice must be a word or an object")
	}
	if object.Type != "function" || object.Name == "" {
		return nil, fmt.Errorf("no translation for a tool_choice of type %q", object.Type)
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": object.Name}}, nil
}

// chatResponseFormat carries a structured-output request across. A plain
// text format is the default on both sides and is left unsaid.
func chatResponseFormat(raw json.RawMessage) (any, error) {
	var format struct {
		Type   string          `json:"type"`
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
		Strict *bool           `json:"strict"`
	}
	if err := json.Unmarshal(raw, &format); err != nil {
		return nil, fmt.Errorf("text.format must be an object")
	}
	switch format.Type {
	case "", "text":
		return nil, nil
	case "json_object":
		return map[string]any{"type": "json_object"}, nil
	case "json_schema":
		if format.Name == "" || len(format.Schema) == 0 {
			return nil, fmt.Errorf("a json_schema format needs a name and a schema")
		}
		schema := map[string]any{"name": format.Name, "schema": format.Schema}
		if format.Strict != nil {
			schema["strict"] = *format.Strict
		}
		return map[string]any{"type": "json_schema", "json_schema": schema}, nil
	}
	return nil, fmt.Errorf("no translation for a text format of type %q", format.Type)
}

// chatCompletion is the half of a chat completion the envelope is built
// from.
type chatCompletion struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   *string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

// chatToResponses rewrites a chat completion as a Responses envelope.
//
// The envelope is written out in full rather than as the handful of
// fields a particular client happens to read. A client's SDK parses this
// into a typed object, and the failure mode of a missing field is not a
// missing field — it is a parse error three frames from anything the
// adopter can see. That failure is exactly what this whole path exists to
// stop, so it is not one to reproduce from the other side.
func chatToResponses(raw []byte, from translatedRequest) ([]byte, error) {
	var completion chatCompletion
	if err := json.Unmarshal(raw, &completion); err != nil {
		return nil, fmt.Errorf("upstream body is not a chat completion: %v", err)
	}
	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("upstream returned no choice to translate")
	}
	choice := completion.Choices[0]

	output := []any{}
	if choice.Message.Content != nil && *choice.Message.Content != "" {
		output = append(output, map[string]any{
			"type": "message", "id": "msg_" + completion.ID, "status": "completed",
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "output_text", "text": *choice.Message.Content, "annotations": []any{},
			}},
		})
	}
	for i, call := range choice.Message.ToolCalls {
		output = append(output, map[string]any{
			"type": "function_call", "id": fmt.Sprintf("fc_%s_%d", completion.ID, i),
			"call_id": call.ID, "name": call.Function.Name,
			"arguments": call.Function.Arguments, "status": "completed",
		})
	}

	// `status` says whether the turn finished, and `incomplete_details`
	// says why it did not. A length stop is the one an adopter has to be
	// able to see: a reply cut at the token bound looks like a model
	// that answered badly unless the envelope says otherwise.
	status, incomplete := "completed", any(nil)
	if choice.FinishReason == "length" {
		status, incomplete = "incomplete", map[string]any{"reason": "max_output_tokens"}
	}
	if choice.FinishReason == "content_filter" {
		status, incomplete = "incomplete", map[string]any{"reason": "content_filter"}
	}

	envelope := map[string]any{
		"id":         "resp_" + completion.ID,
		"object":     "response",
		"created_at": completion.Created,
		"status":     status,
		"model":      completion.Model,
		"output":     output,
		// Stated rather than echoed: this seam holds no response and
		// truncates nothing, so these two say what it DID, whatever the
		// request asked for.
		"store":              false,
		"truncation":         "disabled",
		"incomplete_details": incomplete,
		"error":              nil,
		// Restated from the request — verbatim where the client sent a
		// value, and as the default its absence means where it did not.
		// A Responses envelope is meant to be enough to continue from, so
		// a client that reads `tools` or `tool_choice` back off the answer
		// has to find what it actually asked for.
		"instructions":        from.echo("instructions", nil),
		"tools":               from.echo("tools", []any{}),
		"tool_choice":         from.echo("tool_choice", "auto"),
		"parallel_tool_calls": from.echo("parallel_tool_calls", true),
		"max_output_tokens":   from.echo("max_output_tokens", nil),
		"temperature":         from.echo("temperature", nil),
		"top_p":               from.echo("top_p", nil),
		"user":                from.echo("user", nil),
		"metadata":            from.echo("metadata", map[string]any{}),
		"text":                from.echo("text", map[string]any{"format": map[string]any{"type": "text"}}),
		// Nothing here holds a conversation or does reasoning, and a
		// request asking for either is refused on the way in rather than
		// dropped, so these are the only values they can honestly take.
		"previous_response_id": nil,
		"reasoning":            nil,
	}
	// Usage is echoed under the Responses names when the upstream
	// reported it, and is absent when it did not — never zeroed. The
	// forward path refuses an unmeterable success before this runs, so
	// the absent case is a non-2xx body; writing zeros here would put a
	// number on a call nobody counted.
	if completion.Usage != nil {
		if u, ok := completion.Usage.counts(); ok {
			envelope["usage"] = map[string]any{
				"input_tokens":          u.InputTokens,
				"output_tokens":         u.OutputTokens,
				"total_tokens":          u.InputTokens + u.OutputTokens,
				"input_tokens_details":  map[string]any{"cached_tokens": 0},
				"output_tokens_details": map[string]any{"reasoning_tokens": 0},
			}
		}
	}
	return json.Marshal(envelope)
}

// firstBytes bounds an untrusted fragment quoted back in an error.
func firstBytes(raw []byte) string {
	const max = 60
	if len(raw) > max {
		return string(raw[:max]) + "…"
	}
	return string(raw)
}
