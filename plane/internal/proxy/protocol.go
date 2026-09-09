package proxy

// Reading token counts out of a model response, per protocol.
//
// The meter's whole input is two numbers, and where they live is the
// only thing the two protocols this plane speaks disagree about:
//
//	chat_completions  {"usage": {"prompt_tokens": 11, "completion_tokens": 16}}
//	responses         {"usage": {"input_tokens": 13, "output_tokens": 16}}
//
// The proxy used to read the first shape unconditionally. Pointed at a
// Responses-API upstream — which is what one current agent framework
// speaks BY DEFAULT, not as an option — it found no `prompt_tokens`,
// found no `completion_tokens`, and ledgered `0 in / 0 out` for a call
// the upstream had reported thirteen and sixteen for. Nothing said so. A
// token budget over that upstream could never be exhausted.
//
// So `found` is a field here and not an inference from zero. A response
// that reports zero tokens and a response the plane cannot read are
// different facts, and the second one is not allowed to look like the
// first: see forward() in handler.go, which refuses a success it could
// not meter rather than relaying it.

import (
	"encoding/json"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
)

// usage is one call's token counts, plus whether they were actually
// read. Zero-valued usage with found=false is "no usage envelope
// arrived"; with found=true it is "the upstream reported zero".
type usage struct {
	InputTokens  int64
	OutputTokens int64
	found        bool
}

// chatUsage / responsesUsage are the two wire shapes, named separately
// so neither can silently absorb the other's field names.
//
// The counts are POINTERS, and that is the whole cross-check. Both
// protocols call the envelope `usage`, so its mere presence proves
// nothing: decoded into the wrong struct, a Responses envelope yields a
// chatUsage of two zeros — which is the original finding exactly, with
// the reader one level deeper. A shape is recognised only when at least
// one of its OWN fields was actually there.
type chatUsage struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
}

func (u chatUsage) counts() (usage, bool) {
	if u.PromptTokens == nil && u.CompletionTokens == nil {
		return usage{}, false
	}
	return usage{InputTokens: deref(u.PromptTokens), OutputTokens: deref(u.CompletionTokens), found: true}, true
}

type responsesUsage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
}

func (u responsesUsage) counts() (usage, bool) {
	if u.InputTokens == nil && u.OutputTokens == nil {
		return usage{}, false
	}
	return usage{InputTokens: deref(u.InputTokens), OutputTokens: deref(u.OutputTokens), found: true}, true
}

func deref(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// readUsage extracts the usage envelope from a whole, non-streamed
// response body. A body that is not JSON, or carries no usage object,
// returns found=false — never a plausible zero.
func readUsage(protocol string, raw []byte) usage {
	switch protocol {
	case config.ProtocolResponses:
		var envelope struct {
			Usage *responsesUsage `json:"usage"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.Usage == nil {
			return usage{}
		}
		u, _ := envelope.Usage.counts()
		return u
	default:
		var envelope struct {
			Usage *chatUsage `json:"usage"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.Usage == nil {
			return usage{}
		}
		u, _ := envelope.Usage.counts()
		return u
	}
}

// readStreamUsage extracts usage from ONE SSE `data:` payload, and
// reports whether this payload carried it.
//
// The two protocols put it in different places as well as under
// different names. chat_completions sends a final chunk whose top-level
// `usage` is non-null (every earlier chunk sends null, which is why the
// pointer matters). The Responses API sends semantic events, and the
// terminal ones — response.completed, and response.incomplete or
// response.failed when a turn ends early — carry a whole `response`
// object with the usage inside it. A turn that ends early still spent
// tokens, so all three are read; the last one seen wins.
func readStreamUsage(protocol string, payload []byte) (usage, bool) {
	switch protocol {
	case config.ProtocolResponses:
		var event struct {
			Usage    *responsesUsage `json:"usage"`
			Response *struct {
				Usage *responsesUsage `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal(payload, &event) != nil {
			return usage{}, false
		}
		u := event.Usage
		if u == nil && event.Response != nil {
			u = event.Response.Usage
		}
		if u == nil {
			return usage{}, false
		}
		return u.counts()
	default:
		var chunk struct {
			Usage *chatUsage `json:"usage"`
		}
		if json.Unmarshal(payload, &chunk) != nil || chunk.Usage == nil {
			return usage{}, false
		}
		return chunk.Usage.counts()
	}
}

// prepareStream returns the request body to forward for a streamed call.
//
// chat_completions needs `stream_options.include_usage` set or the
// upstream sends no usage chunk at all and the call is unmeterable. The
// Responses API has no such option and its terminal event always carries
// usage — and it REFUSES unknown top-level parameters, so sending
// stream_options there would turn a governed call into a 400. The
// difference is why this is a function and not a line.
func prepareStream(protocol string, body []byte) ([]byte, error) {
	if protocol == config.ProtocolResponses {
		return body, nil
	}
	return withIncludeUsage(body)
}
