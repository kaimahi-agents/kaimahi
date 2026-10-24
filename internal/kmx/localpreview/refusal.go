// Package localpreview runs agent-file previews on embedded Sessions with a
// checked private SQLite journal family, including provider-checked Copilot
// routes and one Session per native judge sample. Journal creation and clearing
// use the checked filesystem foundation; interrupted clearing is recovered only
// for validated run-family names. Preview/replay grants no suite or Deploy
// authority. Native package tests qualify local NTFS storage, not the guarded
// Windows CLI.
package localpreview

import "fmt"

// Label is attached to every result. Preview and journal replay never
// qualify a deployment or produce native shipping evidence.
const Label = "local preview"

// Refusal kinds. Kind and Error text are fixed: they never quote credentials,
// prompts, answers, endpoint responses or authored values, so they are safe for
// default errors and for the Sessions journal's error record.
const (
	RefusedModelUnconfigured = "model-unconfigured"
	RefusedEndpoint          = "endpoint-invalid"
	RefusedCredential        = "credential-invalid"
	RefusedTools             = "tools-requested"
	RefusedToolCall          = "tool-call-reply"
	RefusedJudge             = "judge-unconfigured"
	RefusedHTTPStatus        = "http-status"
	RefusedCredentialEcho    = "reply-credential"
	RefusedCredentialContent = "content-credential"
	RefusedUnreachable       = "endpoint-unreachable"
	RefusedMalformed         = "reply-malformed"
	RefusedOversized         = "reply-oversized"
	RefusedTruncated         = "reply-truncated"
	RefusedRequest           = "request-invalid"
	RefusedJournal           = "journal-unavailable"
)

// Refusal reports why the preview did not send, or why a model reply was not
// accepted, and what the developer can change.
type Refusal struct {
	Kind    string
	Problem string
	Remedy  string
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("%s: %s; %s", Label, r.Problem, r.Remedy)
}

func refuse(kind, problem, remedy string) *Refusal {
	return &Refusal{Kind: kind, Problem: problem, Remedy: remedy}
}
