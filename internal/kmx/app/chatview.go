package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Rendering the A2A task for a human.
//
// `kagent invoke` prints the raw A2A task as one line of JSON. That is the
// right thing for a machine and a poor thing for a person: the reply they
// asked for is buried in artifacts[].parts[].text, several hundred
// characters into a line that also carries the session id, the full message
// history and the metadata.
//
// So the shape of the output depends on who is reading:
//
//   - a terminal gets the reply, the tool calls and the token cost;
//   - a pipe gets the JSON, byte for byte, because things parse it.
//
// That second clause is not a nicety. CI captures this output in every
// end-to-end shard (`make chat | tee chat.out`, then
// scripts/verify-chat.py) — a dozen-odd places, deliberately not counted
// here because the count is what went stale last time — and
// asserts on the task's shape — status.state, the function_call and the
// function_response payload. Pretty-printing by default would break every
// one of them, and `--json` forces the raw form when a terminal wants it.

// isTerminal reports whether w is a terminal, not merely a character device.
// Anything that is not an *os.File — a test buffer, a pipe wrapper — is
// treated as not a terminal, which is the safe default: it means "print the
// machine-readable form".
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// a2aTask is the part of kagent's A2A task this renderer reads. Everything
// else in the payload is deliberately ignored rather than modelled: the
// renderer must not fail because an unrelated field changed shape.
type a2aTask struct {
	Status struct {
		State string `json:"state"`
	} `json:"status"`
	Artifacts []struct {
		Parts []taskPart `json:"parts"`
	} `json:"artifacts"`
	History []struct {
		Role     string     `json:"role"`
		Parts    []taskPart `json:"parts"`
		Metadata struct {
			Usage struct {
				Prompt     int `json:"promptTokenCount"`
				Candidates int `json:"candidatesTokenCount"`
				Total      int `json:"totalTokenCount"`
			} `json:"kagent_usage_metadata"`
		} `json:"metadata"`
	} `json:"history"`
}

type taskPart struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Data struct {
		Name     string          `json:"name"`
		Args     json.RawMessage `json:"args"`
		Response struct {
			IsError bool `json:"isError"`
		} `json:"response"`
	} `json:"data"`
}

// renderChat writes a human-readable view of one A2A task.
//
// It returns false when the output is not a task it recognises — a transport
// error, a usage message, anything at all — and the caller then prints the
// original bytes. Falling back to the raw text is always safe; guessing at a
// shape that is not there is not.
func renderChat(out io.Writer, combined string) bool {
	line := lastJSONLine(combined)
	if line == "" {
		return false
	}
	var task a2aTask
	if err := json.Unmarshal([]byte(line), &task); err != nil {
		return false
	}

	reply := firstText(task)
	tools := toolCalls(task)
	if reply == "" && len(tools) == 0 {
		// Nothing worth showing: let the caller print the raw payload
		// rather than an empty frame that hides it.
		return false
	}

	if len(tools) > 0 {
		for i := range tools {
			tools[i] = safeTerminal(tools[i])
		}
		fmt.Fprintf(out, "\ntools called: %s\n", strings.Join(tools, ", "))
	}
	if reply != "" {
		fmt.Fprintf(out, "\n%s\n", strings.TrimSpace(safeTerminal(reply)))
	}

	var trailer []string
	if state := task.Status.State; state != "" && state != "completed" {
		// "completed" is the expected case and saying so adds nothing;
		// anything else is the most important word on the screen.
		trailer = append(trailer, "state: "+safeTerminal(state))
	}
	if in, outTok := totalUsage(task); in+outTok > 0 {
		trailer = append(trailer, fmt.Sprintf("tokens: %d in, %d out", in, outTok))
	}
	if len(trailer) > 0 {
		fmt.Fprintf(out, "\n(%s)\n", strings.Join(trailer, " · "))
	}
	return true
}

// lastJSONLine returns the last line that looks like a JSON object.
//
// The captured stream is stdout and stderr combined, so the task shares it
// with the kagent CLI's own logging, and a retry can leave more than one task
// in the buffer. The last one is the one that was answered.
func lastJSONLine(combined string) string {
	var found string
	for _, l := range strings.Split(combined, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "{") && strings.HasSuffix(l, "}") {
			found = l
		}
	}
	return found
}

// firstText returns the agent's reply: the first text part of the first
// artifact that has one.
func firstText(task a2aTask) string {
	for _, a := range task.Artifacts {
		for _, p := range a.Parts {
			if p.Kind == "text" && strings.TrimSpace(p.Text) != "" {
				return p.Text
			}
		}
	}
	return ""
}

// toolCalls lists the tools the agent actually invoked, in order, without
// repeats. A failed call is marked: "it called the tool" and "the tool
// worked" are different facts and the second is the one people assume.
func toolCalls(task a2aTask) []string {
	var names []string
	failed := map[string]bool{}
	seen := map[string]bool{}
	for _, m := range task.History {
		for _, p := range m.Parts {
			if p.Kind != "data" || p.Data.Name == "" {
				continue
			}
			if p.Data.Response.IsError {
				failed[p.Data.Name] = true
			}
			if p.Data.Args == nil {
				continue // the response half of the pair, not a new call
			}
			if !seen[p.Data.Name] {
				seen[p.Data.Name] = true
				names = append(names, p.Data.Name)
			}
		}
	}
	for i, n := range names {
		if failed[n] {
			names[i] = n + " (failed)"
		}
	}
	return names
}

// totalUsage sums the per-turn token counts kagent reports.
func totalUsage(task a2aTask) (in, out int) {
	for _, m := range task.History {
		in += m.Metadata.Usage.Prompt
		out += m.Metadata.Usage.Candidates
	}
	return in, out
}
