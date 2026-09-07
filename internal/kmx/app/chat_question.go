package app

import "encoding/json"

// The other way a one-shot chat can end without an answer: the agent stops
// and asks the HUMAN a question.
//
// kagent's python runtime gives every agent a built-in `ask_user` tool. It
// is not declared in k8s/hello-world.yaml — that agent declares no tools at
// all — and the Agent CRD at kagent 0.9.12 has no field that turns it off,
// so the only lever the manifest has is the system message, which already
// forbids asking questions in so many words. A 3B model obeys that most of
// the time and occasionally does not; asked "who are you and where are you
// running?" it once answered half of it and called
// `ask_user` with "Where are you?". The task then ends
//
//	"status":{"state":"input-required", ...}
//
// with no artifacts, so the reply is empty. Nothing in a script can answer
// it, and scripts/verify-chat.py fails closed on it — correctly.
//
// A model that asked a question instead of answering is non-deterministic
// model behaviour, and a second sample of a non-deterministic process is a
// fair sample: the same test the controller's transport retry is held to —
// the CLASS of failure decides whether repeating it is honest. So a one-shot
// chat re-asks, and the conditions are deliberately narrow:
//
//   - The pending human-in-the-loop request must be for `ask_user` and
//     nothing else. A confirmation for a REAL tool is an approval decision,
//     the same input-required state carrying a human's judgement call —
//     re-asking would throw that away and, on the second sample, might get a
//     call that is never confirmed at all. Never retried.
//   - The task must contain no tool response. Then the agent did nothing but
//     ask, and re-asking repeats nothing: the only cost is model tokens.
//     (Which are metered: under a use-bounded budget grant the re-ask can be
//     denied. It then fails as it would have anyway — an exhausted grant
//     cannot turn into a false success.)
//   - Never with an explicit --session. The question is pending in that
//     session, and a fresh message there could be read as its answer.
//
// It announces every re-ask on stderr, because a silent retry turns a
// failure that happens half the time into one that is invisible.
const chatQuestionResamples = 2

// hitlTask is the part of kagent's printed A2A task these checks read.
type hitlTask struct {
	Status struct {
		State   string `json:"state"`
		Message struct {
			Parts []hitlPart `json:"parts"`
		} `json:"message"`
	} `json:"status"`
	History []struct {
		Parts []hitlPart `json:"parts"`
	} `json:"history"`
}

type hitlPart struct {
	Data struct {
		Name string `json:"name"`
		Args struct {
			Original struct {
				Name string `json:"name"`
			} `json:"originalFunctionCall"`
			Confirmation struct {
				Payload struct {
					Parts []struct {
						Original struct {
							Name string `json:"name"`
						} `json:"originalFunctionCall"`
					} `json:"hitl_parts"`
				} `json:"payload"`
			} `json:"toolConfirmation"`
		} `json:"args"`
	} `json:"data"`
	Metadata map[string]any `json:"metadata"`
}

// partType reads the part's kagent type. kagent renamed these keys from
// `adk_*` to `kagent_*`; both spellings are read so the check does not go
// quietly blind across a version bump.
func (p hitlPart) partType() string {
	for _, key := range []string{"kagent_type", "adk_type"} {
		if value, ok := p.Metadata[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

// chatShouldResample is the whole decision: re-ask only a one-shot chat
// whose agent did nothing but ask the human a question. An explicit
// --session is excluded because the question is pending IN that session and
// a fresh message there could be taken for its answer.
func chatShouldResample(session, out string) bool {
	return session == "" && agentAskedTheUser(out)
}

// agentAskedTheUser reports whether the captured output is a task that
// stopped ONLY to ask the human a question, having done nothing else.
// Anything it cannot parse, and anything with another pending confirmation
// or any tool response, is false: this decides whether to spend a second
// sample, so it fails closed.
func agentAskedTheUser(out string) bool {
	raw := lastJSONLine(out)
	if raw == "" {
		return false
	}
	var task hitlTask
	if json.Unmarshal([]byte(raw), &task) != nil {
		return false
	}
	if task.Status.State != "input-required" {
		return false
	}
	for _, message := range task.History {
		for _, part := range message.Parts {
			if part.partType() == "function_response" {
				return false
			}
		}
	}
	pending := 0
	for _, part := range task.Status.Message.Parts {
		if part.partType() != "function_call" || part.Data.Name != "adk_request_confirmation" {
			continue
		}
		names := []string{}
		if nested := part.Data.Args.Confirmation.Payload.Parts; len(nested) > 0 {
			for _, one := range nested {
				names = append(names, one.Original.Name)
			}
		} else {
			names = append(names, part.Data.Args.Original.Name)
		}
		for _, name := range names {
			if name != "ask_user" {
				return false
			}
			pending++
		}
	}
	return pending > 0
}
