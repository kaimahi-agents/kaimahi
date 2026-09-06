package gateway

// The binding: what makes an approval an approval of a TRANSACTION
// rather than of a verb.
//
// A tools/call's canonical arguments are reduced to two things, both
// derived from the one canonical tree (canon.go):
//
//   - a DIGEST, which the approval request and the grant carry and which
//     the gateway re-computes on every call. A grant admits a call only
//     when the digests match, so an approval for "pay 32550 to MER-4471"
//     cannot be spent on "pay 48000 to someone else".
//   - a SUMMARY, the human-readable line an approver reads before
//     deciding, and the audit's record of what ran.
//
// Both are built from the tool's DECLARED policy fields (config), and
// the summary from nothing else: undeclared arguments are arbitrary
// business data, and the audit is in every pg_dump (`make backup`).
// internal/redact scrubs known SECRET VALUES from logs — it is not a
// business-data redactor and is deliberately not used here.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

const (
	// maxSummaryValue bounds one rendered value, maxSummary the line.
	maxSummaryValue = 64
	maxSummary      = 240
)

// CallBinding is what one tools/call binds to.
type CallBinding struct {
	// Digest is the hex sha256 an approval welds to. Never empty for a
	// real call.
	Digest string
	// Summary is the human-readable transaction line
	// ("payment_schedule: amount_cents 3255000, payee_id MER-4471").
	Summary string
	// Declared says whether the tool declared its policy-relevant fields.
	// False means the digest binds the WHOLE canonical argument object —
	// exact, but brittle, since an LLM re-emitting a semantically
	// identical call is not byte-stable (docs/tool-governance.md).
	Declared bool
}

// Bind computes the binding for one call. fields/declared come from the
// config's policy set; args is the canonical argument tree.
func Bind(tool string, args map[string]any, fields []string, declared bool) CallBinding {
	bound := map[string]any{}
	mode := "all"
	if declared {
		mode = "declared"
		for _, f := range fields {
			if v, ok := args[f]; ok {
				bound[f] = v
			}
		}
	} else {
		bound = args
	}
	// The mode is inside the digest so a whole-object binding and a
	// declared-field binding can never collide on the same bytes.
	payload, err := marshalCanonical(map[string]any{"tool": tool, "bind": mode, "args": bound})
	if err != nil {
		// A canonical tree always marshals; if it somehow does not, a
		// digest nothing can match is the fail-closed answer.
		payload = []byte(fmt.Sprintf("unmarshalable:%s:%v", tool, err))
	}
	sum := sha256.Sum256(payload)
	return CallBinding{
		Digest:   hex.EncodeToString(sum[:]),
		Summary:  summarize(tool, args, fields, declared),
		Declared: declared,
	}
}

// summarize renders the approver-facing line. It reads DECLARED fields
// only: an undeclared tool's summary names the tool and says so, and no
// undeclared argument value is ever carried into the request, the
// notification or the audit.
func summarize(tool string, args map[string]any, fields []string, declared bool) string {
	if !declared {
		return tool + ": (this tool declares no policy-relevant arguments; the approval binds the whole call)"
	}
	if len(fields) == 0 {
		return tool + ": (no policy-relevant arguments)"
	}
	var parts []string
	for _, f := range fields {
		v, ok := args[f]
		if !ok {
			continue
		}
		parts = append(parts, f+" "+renderValue(v))
	}
	if len(parts) == 0 {
		return tool + ": (no declared argument present)"
	}
	return clipTo(tool+": "+strings.Join(parts, ", "), maxSummary)
}

// renderValue prints one declared value for a human. Scalars only:
// a nested object or list is named, never inlined, so the summary cannot
// become a copy of the payload.
func renderValue(v any) string {
	switch t := v.(type) {
	case string:
		return clip(sanitize(t))
	case json.Number:
		return clip(t.String())
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	case map[string]any:
		return "(object)"
	case []any:
		return "(list)"
	}
	return "(value)"
}

// sanitize keeps a summary to one printable line: an argument value is
// agent-influenced text, and it lands in a Slack message and an audit
// row.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
}

func clip(s string) string { return clipTo(s, maxSummaryValue) }

// clipTo bounds a string to n BYTES, cutting on rune boundaries and
// counting the ellipsis against the bound. Slicing by byte index would
// cut a multibyte rune in half — the summary lands in an approval
// request, an audit row and a Slack message, none of which should carry
// invalid UTF-8 — and would also overrun n, since "…" is three bytes.
func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ellipsis = "…"
	r := []rune(s)
	for len(r) > 0 && len(string(r))+len(ellipsis) > n {
		r = r[:len(r)-1]
	}
	return string(r) + ellipsis
}

// BindArguments computes a binding from raw JSON arguments — the admin
// surface's explicit filing path (`make request KIND=tool`), which files
// a request for a call the operator names rather than one the gateway
// saw. Empty raw means the argument-less call.
func BindArguments(tool string, raw []byte, fields []string, declared bool) (CallBinding, error) {
	args := map[string]any{}
	if len(strings.TrimSpace(string(raw))) > 0 {
		_, msg, err := canonicalize([]byte(`{"params":{"arguments":` + string(raw) + `}}`))
		if err != nil {
			return CallBinding{}, err
		}
		var ok bool
		if args, ok = argumentsOf(msg); !ok {
			return CallBinding{}, fmt.Errorf("tool arguments must be a JSON object")
		}
	}
	return Bind(tool, args, fields, declared), nil
}
