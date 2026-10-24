package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

type FilePreviewOptions struct {
	File, Operation, CaseID string
	ToolBindings            string
	Model                   string
	Route, JudgeRoute       *modelaccess.Route
	Input                   *string
	ShowContent             bool
	ShowFailedContent       bool
	// FullPath keeps MCP's requested identity inside the shared redaction boundary.
	FullPath bool
	Progress func(string) error
}

type FilePreviewResult struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Kind          string                    `json:"kind"`
	Runner        string                    `json:"runner,omitempty"`
	Operation     string                    `json:"operation"`
	File          FilePreviewIdentity       `json:"file"`
	ModelRoute    FilePreviewRoute          `json:"modelRoute"`
	JudgeRoute    *FilePreviewRoute         `json:"judgeRoute,omitempty"`
	Diagnostic    *FilePreviewDiagnostic    `json:"diagnostic,omitempty"`
	Selection     FilePreviewSelection      `json:"selection"`
	State         string                    `json:"state"`
	Verdict       runtime.EvaluationVerdict `json:"verdict"`
	FullPass      bool                      `json:"fullPass"`
	ReasonCode    string                    `json:"reasonCode"`
	Reason        string                    `json:"reason"`
	Examples      []FilePreviewExample      `json:"examples"`
	AdHoc         *FilePreviewCompletion    `json:"adHoc,omitempty"`
	Retention     FilePreviewRetention      `json:"retention"`
}
type FilePreviewDiagnostic struct {
	Field  string `json:"field"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Repair string `json:"repair"`
}
type FilePreviewIdentity struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
}
type FilePreviewRoute struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}
type FilePreviewSelection struct {
	Scope      string   `json:"scope"`
	CaseIDs    []string `json:"caseIds"`
	TotalCases int      `json:"totalCases"`
}
type FilePreviewRetention struct {
	Status string `json:"status"`
}
type FilePreviewCompletion struct {
	State         string `json:"state"`
	ReasonCode    string `json:"reasonCode"`
	Reason        string `json:"reason"`
	AnswerExcerpt string `json:"answerExcerpt,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
	ToolCalls     int    `json:"toolCalls,omitempty"`
}
type FilePreviewExample struct {
	ID string `json:"id"`
	FilePreviewCompletion
	Verdict    runtime.EvaluationVerdict `json:"verdict"`
	Assertions []FilePreviewAssertion    `json:"assertions"`
}
type FilePreviewAssertion struct {
	ID         string                    `json:"id"`
	Type       string                    `json:"type"`
	Verdict    runtime.EvaluationVerdict `json:"verdict"`
	ReasonCode string                    `json:"reasonCode"`
	Reason     string                    `json:"reason"`
	Judge      *FilePreviewJudge         `json:"judge,omitempty"`
}

// FilePreviewError contains fixed safe text or validated catalog choices, never raw provider content.
type FilePreviewError struct {
	code    string
	exit    int
	message string
}

func (e *FilePreviewError) Error() string {
	if e.message != "" {
		return e.message
	}
	return previewReason(e.code)
}
func (e *FilePreviewError) ExitCode() int { return e.exit }

var previewCopilotFailures = []struct {
	err  error
	code string
}{
	{copilot.ErrAuthRequired, "copilot_auth_required"},
	{copilot.ErrAuthRevoked, "copilot_auth_revoked"},
	{copilot.ErrSubscription, "copilot_no_subscription"},
	{copilot.ErrPolicy, "copilot_policy_denied"},
	{copilot.ErrQuota, "copilot_quota"},
	{copilot.ErrModelUnavailable, "copilot_model_unavailable"},
	{copilot.ErrMalformed, "copilot_malformed"},
	{copilot.ErrKeychain, "copilot_keychain_unavailable"},
	{copilot.ErrUnavailable, "copilot_unavailable"},
}

func previewCopilotFailure(err error) string {
	for _, failure := range previewCopilotFailures {
		if errors.Is(err, failure.err) {
			return failure.code
		}
	}
	return ""
}

func previewReason(code string) string {
	for _, failure := range previewCopilotFailures {
		if code == failure.code {
			return failure.err.Error()
		}
	}
	reasons := map[string]string{
		"checks_passed": "All selected example assertions passed.", "assertion_satisfied": "Assertion satisfied.",
		"completion_ready": "Attributable completion available.",
		"assertion_failed": "Assertion not satisfied.", "judge_unknown": "Assertion outcome is inconclusive.",
		"not_run": "Execution is not complete.", "cancelled": "Local preview cancelled.", "deadline_exceeded": "Local preview deadline exceeded.",
		"invalid_file":      "Invalid agent file; use the canonical charter format with required fields and an Examples section.",
		"no_tests":          "No tests yet — add or suggest one",
		"invalid_selection": "Unknown example ID; select an authored example ID.", "invalid_options": "Invalid file preview options.",
		"model_unavailable":   "Selected model route is unavailable or does not match the file model; select an exact trusted route.",
		"tools_unavailable":   "Host-bound local tools are unavailable; Copilot tool execution is unsupported.",
		"unsafe_tool_content": "Unsafe tool preview content was withheld; nothing was journaled.",
		"credential_content":  "Credential-bearing preview content was withheld.",
		"runner_unavailable":  "Embedded preview runner is not available; no inference was performed.",
		"execution_failed":    "Local preview execution failed.", "invalid_completion": "Runner returned incomplete or mismatched execution metadata.",
		"diagnostics_failed": "Cannot write local preview diagnostics.",
		"journal_failed":     "Preview journal finalization failed; the result is unknown.",
	}
	return reasons[code]
}

// PreviewAgentFile is the CLI path wrapper: load once, then execute that snapshot.
func (a *App) PreviewAgentFile(ctx context.Context, opt FilePreviewOptions) (FilePreviewResult, error) {
	parsed, err := agentfile.Load(opt.File)
	return a.previewAgentSnapshot(ctx, parsed, opt, err)
}

// PreviewAgentSnapshot executes a caller-owned parsed snapshot without reopening
// source files. File is a display label only; callers retain target authority.
// The caller must obtain parsed from agentfile.Parse/Load and not mutate it while
// the operation runs. Preview results grant no native evidence/deploy authority.
func (a *App) PreviewAgentSnapshot(ctx context.Context, parsed *agentfile.Agent, opt FilePreviewOptions) (FilePreviewResult, error) {
	return a.previewAgentSnapshot(ctx, parsed, opt, nil)
}

func (a *App) previewAgentSnapshot(ctx context.Context, parsed *agentfile.Agent, opt FilePreviewOptions, parseErr error) (FilePreviewResult, error) {
	filePath := suiteDiagnosticIdentity(filepath.Base(opt.File), nil)
	if opt.FullPath {
		var safe bool
		filePath, safe = suiteDiagnosticText(opt.File, len(opt.File), nil)
		if !safe {
			filePath = "withheld"
		}
	}
	r := FilePreviewResult{SchemaVersion: 1, Kind: "kmx.agent-file-preview", Operation: opt.Operation,
		File: FilePreviewIdentity{Path: filePath}, Selection: FilePreviewSelection{Scope: "full", CaseIDs: []string{}},
		State: "refused", Verdict: runtime.EvaluationUnknown, Examples: []FilePreviewExample{}, Retention: FilePreviewRetention{Status: "not_requested"}}
	fail := func(code string, exit int) (FilePreviewResult, *FilePreviewError) {
		r.FullPass = false
		r.Verdict = runtime.EvaluationUnknown
		r.ReasonCode = code
		r.Reason = previewReason(code)
		return r, &FilePreviewError{code: code, exit: exit}
	}
	if opt.Operation != "run" && opt.Operation != "test" || opt.Operation == "test" && opt.Input != nil || opt.Input != nil && (len(*opt.Input) > agentfile.MaxInputBytes || !utf8.ValidString(*opt.Input)) {
		return fail("invalid_options", 1)
	}
	if parseErr != nil || parsed == nil {
		var detail *agentfile.Error
		if errors.As(parseErr, &detail) {
			r.Diagnostic = &FilePreviewDiagnostic{detail.Field, detail.Location.Line, detail.Location.Column, detail.Problem}
		}
		return fail("invalid_file", 1)
	}
	// Metadata is printable but can still be shaped like a credential. Do not echo it.
	if opt.Model != "" && (!agentfile.ValidModel(opt.Model) || secretshapes.Match(opt.Model) != nil) {
		return fail("invalid_options", 1)
	}
	metadata := []string{r.File.Path, parsed.Model}
	for _, ex := range parsed.Examples {
		metadata = append(metadata, ex.ID)
		for _, as := range ex.Assertions {
			metadata = append(metadata, as.ID)
		}
	}
	for _, s := range metadata {
		if secretshapes.Match(s) != nil {
			r.File.Path = "[agent file]"
			return fail("invalid_file", 1)
		}
	}
	missingModel := parsed.Model == ""
	execution := *parsed
	if opt.Model != "" {
		execution.Model = opt.Model
	} else if missingModel {
		execution.Model = modelaccess.CopilotDefaultModel
	}
	parsed = &execution
	r.File.Revision = parsed.Revision
	r.Selection.TotalCases = len(parsed.Examples)
	// Zero authored examples permit only an explicit ad-hoc Run, never a Test.
	if len(parsed.Examples) == 0 && opt.Input == nil {
		return fail("no_tests", 1)
	}
	route := modelaccess.Route{Provider: "fake", Model: "fake", Auth: "none"}
	if opt.Route != nil {
		route = *opt.Route
		if route.Model == "" || opt.Model != "" || missingModel {
			route.Model = parsed.Model
		}
	} else if parsed.Model != "fake" {
		return fail("model_unavailable", 1)
	}
	if route.Model != parsed.Model || route.Validate() != nil || route.Provider == "fake" && route.Model != "fake" || route.Provider != "fake" && route.Model == "fake" {
		return fail("model_unavailable", 1)
	}
	r.ModelRoute = FilePreviewRoute{route.Provider, route.Model}
	judge := route
	if opt.JudgeRoute != nil {
		judge = *opt.JudgeRoute
	}
	if judge.Validate() != nil || secretshapes.Match(judge.Model) != nil || judge.Provider == "fake" && judge.Model != "fake" || judge.Provider != "fake" && judge.Model == "fake" {
		return fail("model_unavailable", 1)
	}
	parsed.Examples = agentfile.BindJudgeModels(parsed.Examples, judge.Model)
	selected := parsed.Examples
	if opt.CaseID != "" {
		r.Selection.Scope = "subset"
		selected = nil
		for _, ex := range parsed.Examples {
			if ex.ID == opt.CaseID {
				selected = append(selected, ex)
			}
		}
		if len(selected) != 1 {
			return fail("invalid_selection", 1)
		}
	}
	if opt.JudgeRoute != nil {
		r.JudgeRoute = &FilePreviewRoute{judge.Provider, judge.Model}
	}
	if len(parsed.Tools) > 0 && (route.Provider == "copilot" || judge.Provider == "copilot") {
		return fail("tools_unavailable", 1)
	}
	if len(parsed.Tools) == 0 && opt.ToolBindings != "" {
		return fail("invalid_options", 1)
	}
	for _, ex := range selected {
		r.Selection.CaseIDs = append(r.Selection.CaseIDs, ex.ID)
		row := FilePreviewExample{ID: ex.ID, FilePreviewCompletion: previewCompletion("not_run", "not_run"), Verdict: runtime.EvaluationUnknown, Assertions: []FilePreviewAssertion{}}
		for _, as := range ex.Assertions {
			if as.Type == "llmJudge" && as.JudgeModel != "" && as.JudgeModel != judge.Model {
				return fail("model_unavailable", 1)
			}
			unknown := previewAssertion(as.ID, as.Type, runtime.EvaluationUnknown)
			unknown.ReasonCode, unknown.Reason = "not_run", previewReason("not_run")
			row.Assertions = append(row.Assertions, unknown)
		}
		r.Examples = append(r.Examples, row)
	}
	if opt.Input != nil {
		c := previewCompletion("not_run", "not_run")
		r.AdHoc = &c
	}
	if ctx.Err() != nil {
		r.State = "error"
		if errors.Is(ctx.Err(), context.Canceled) {
			r.State = "cancelled"
			return fail("cancelled", 130)
		}
		return fail("deadline_exceeded", 1)
	}
	if a.FileRunner == nil {
		return fail("runner_unavailable", 1)
	}
	request := agentpreview.Request{Agent: parsed, Examples: selected, Route: route, JudgeRoute: opt.JudgeRoute, Input: opt.Input, Progress: opt.Progress, ToolBindings: opt.ToolBindings, FileLabel: r.File.Path}
	judgeCtx, judgeNotes := newFileJudgeContext(ctx)
	outcome, cause := a.FileRunner.Run(judgeCtx, request)
	if errors.Is(cause, agentpreview.ErrUnsafeToolContent) || errors.Is(cause, agentpreview.ErrCredentialContent) || errors.Is(cause, copilot.ErrCredentialContent) {
		r.File = FilePreviewIdentity{Path: "[agent file]"}
		r.ModelRoute = FilePreviewRoute{}
		r.JudgeRoute = nil
		r.Selection = FilePreviewSelection{CaseIDs: []string{}}
		r.Examples = []FilePreviewExample{}
		r.AdHoc = nil
		if errors.Is(cause, agentpreview.ErrUnsafeToolContent) {
			return fail("unsafe_tool_content", 1)
		}
		return fail("credential_content", 1)
	}
	if errors.Is(cause, agentpreview.ErrToolsUnavailable) {
		return fail("tools_unavailable", 1)
	}
	if outcome.NoJournal {
		r.Runner = "preview runner (no journal)"
	}
	// Whitelist result fields; never marshal runtime assertion rows (which include
	// judge evidence and raw reasons), answers or journal handles directly.
	seen := map[string]bool{}
	validCompletion := func(c agentpreview.Completion) bool {
		if c.ToolCalls < 0 || c.ToolCalls > 1 || len(parsed.Tools) == 0 && c.ToolCalls != 0 || c.State != agentpreview.Complete || c.ID == "" || seen[c.ID] || c.FileRevision != parsed.Revision || c.Provider != route.Provider || c.Model != route.Model || len(c.Answer) > 256*1024 || !utf8.ValidString(c.Answer) {
			return false
		}
		seen[c.ID] = true
		return true
	}
	malformed := len(outcome.Examples) > len(selected) || (outcome.AdHoc == nil) != (opt.Input == nil)
	if outcome.AdHoc != nil {
		if validCompletion(*outcome.AdHoc) {
			c := previewCompleted(*outcome.AdHoc, opt.ShowContent)
			r.AdHoc = &c
		} else if outcome.AdHoc.State == agentpreview.Complete {
			malformed = true
		}
	}
	all := []runtime.EvaluationAssertionResult{}
	// Optional explanations share one encoded-byte budget so they cannot crowd
	// verdicts and numeric scores out of the MCP latest-preview snapshot.
	judgeReasonBytes := 64 << 10
	for i, row := range outcome.Examples {
		if i >= len(selected) {
			break
		}
		ex := selected[i]
		if row.ID != ex.ID {
			malformed = true
			break
		}
		if row.Completion.State != agentpreview.Complete {
			continue
		}
		if !validCompletion(row.Completion) || len(row.Assertions) != len(ex.Assertions) {
			malformed = true
			break
		}
		for j, as := range row.Assertions {
			want := ex.Assertions[j]
			if as.ID != want.ID || as.Type != want.Type || as.DefinitionDigest != runtime.EvaluationAssertionDigest(want) || as.Verdict != runtime.EvaluationPass && as.Verdict != runtime.EvaluationFail && as.Verdict != runtime.EvaluationUnknown {
				malformed = true
				break
			}
			r.Examples[i].Assertions[j] = previewAssertion(as.ID, as.Type, as.Verdict)
		}
		if malformed {
			break
		}
		verdict := runtime.EvaluationAssertionsVerdict(row.Assertions)
		show := opt.ShowContent || opt.ShowFailedContent && verdict == runtime.EvaluationFail
		if show {
			for j, as := range row.Assertions {
				note := judgeNotes[fileJudgeKey{ex.ID, as.ID}]
				r.Examples[i].Assertions[j].Judge = previewJudge(as, ex.Assertions[j], note, &judgeReasonBytes)
			}
		}
		r.Examples[i].FilePreviewCompletion = previewCompleted(row.Completion, show)
		r.Examples[i].Verdict = verdict
		all = append(all, row.Assertions...)
	}
	r.State = "error"
	if errors.Is(cause, agentpreview.ErrDiagnosticsFailed) {
		return fail("diagnostics_failed", 1)
	}
	if errors.Is(cause, agentpreview.ErrJournalFailed) {
		return fail("journal_failed", 1)
	}
	if errors.Is(cause, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		r.State = "cancelled"
		return fail("cancelled", 130)
	}
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fail("deadline_exceeded", 1)
	}
	if errors.Is(cause, agentpreview.ErrModelUnavailable) {
		return fail("model_unavailable", 1)
	}
	if cause != nil {
		if code := previewCopilotFailure(cause); code != "" {
			if choices, known := copilot.ModelChoices(cause); known {
				result, previewErr := fail(code, 1)
				names := strings.Join(choices, ", ")
				if names == "" {
					names = "none"
				}
				result.Reason = "Available Copilot models: " + names
				previewErr.message = result.Reason
				return result, previewErr
			}
			return fail(code, 1)
		}
		return fail("execution_failed", 1)
	}
	journalOK := outcome.NoJournal && outcome.Journal == "" || !outcome.NoJournal && outcome.Journal != ""
	if malformed || outcome.State != agentpreview.Complete || !journalOK || len(outcome.Examples) != len(selected) {
		return fail("invalid_completion", 1)
	}
	for _, row := range r.Examples {
		if row.State != "complete" || len(row.Assertions) == 0 {
			return fail("invalid_completion", 1)
		}
	}
	if r.AdHoc != nil && r.AdHoc.State != "complete" {
		return fail("invalid_completion", 1)
	}
	r.State = "complete"
	if len(selected) == 0 {
		// An ad-hoc answer is not evidence: no verdict and never a vacuous full pass.
		r.Verdict, r.FullPass, r.ReasonCode = runtime.EvaluationUnknown, false, "completion_ready"
		r.Reason = previewReason(r.ReasonCode)
		return r, nil
	}
	r.Verdict = runtime.EvaluationAssertionsVerdict(all)
	r.ReasonCode = "checks_passed"
	if r.Verdict == runtime.EvaluationFail {
		r.ReasonCode = "assertion_failed"
	} else if r.Verdict != runtime.EvaluationPass {
		r.ReasonCode = "judge_unknown"
	}
	r.Reason = previewReason(r.ReasonCode)
	r.FullPass = r.Selection.Scope == "full" && r.Verdict == runtime.EvaluationPass
	if r.Verdict != runtime.EvaluationPass {
		return r, &FilePreviewError{code: r.ReasonCode, exit: 2}
	}
	return r, nil
}

func previewCompletion(state, code string) FilePreviewCompletion {
	return FilePreviewCompletion{State: state, ReasonCode: code, Reason: previewReason(code)}
}
func previewCompleted(c agentpreview.Completion, show bool) FilePreviewCompletion {
	row := previewCompletion("complete", "completion_ready")
	row.ToolCalls = c.ToolCalls
	if show {
		row.AnswerExcerpt, _ = suiteDiagnosticText(c.Answer, 4096, nil)
		full, _ := suiteDiagnosticText(c.Answer, len(c.Answer), nil)
		row.Truncated = len(row.AnswerExcerpt) < len(full)
	}
	return row
}
func previewAssertion(id, kind string, v runtime.EvaluationVerdict) FilePreviewAssertion {
	code := "judge_unknown"
	if v == runtime.EvaluationPass {
		code = "assertion_satisfied"
	} else if v == runtime.EvaluationFail {
		code = "assertion_failed"
	}
	return FilePreviewAssertion{ID: id, Type: kind, Verdict: v, ReasonCode: code, Reason: previewReason(code)}
}
