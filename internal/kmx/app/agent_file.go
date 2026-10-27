package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

type FilePreviewOptions struct {
	File, Operation, CaseID string
	// SkipAssertions is answer-only Run; Test refuses it. Default false.
	SkipAssertions bool
	// SourceFile is the trusted physical filename when File is only a display label.
	// CLI callers omit it and use their already-loaded File.
	SourceFile string
	// RetentionFile requests checked retention for a target separate from File's
	// display label. Snapshot callers must use WithFilePreviewRetention.
	RetentionFile     string
	ToolBindings      string
	Model             string
	Route, JudgeRoute *modelaccess.Route
	Input             *string
	ShowContent       bool
	ShowFailedContent bool
	// CLI-only presentation choices; snapshot/MCP callers keep bounded opt-in content.
	IncludeAnswer         bool
	IncludeFailureDetails bool
	DefaultCopilot        bool
	AdHocOnly             bool
	RouteSelected         func(FilePreviewRoute) error

	RecordedReplayOnly   bool
	ExpectedToolSettings *[]localtools.ToolSetting

	// FullPath keeps MCP's requested identity inside the shared redaction boundary.
	FullPath       bool
	Progress       func(string) error
	retentionScope *fileFailureScope
	// CaseFinished receives a safe provisional row, never answer or judge content.
	CaseFinished func(FilePreviewCaseProgress)
	// PrepareRecordingSave freezes authoring authority after Load, before execution.
	PrepareRecordingSave func(context.Context, string) (RecordingSaver, error)
}

type FilePreviewResult struct {
	RecordedReplayOnly bool `json:"recordedReplayOnly,omitempty"`

	SchemaVersion int                       `json:"schemaVersion"`
	SavedRevision string                    `json:"savedRevision,omitempty"`
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
	DurationMs    int64                     `json:"durationMs"`
	Examples      []FilePreviewExample      `json:"examples"`
	AdHoc         *FilePreviewCompletion    `json:"adHoc,omitempty"`
	Retention     FilePreviewRetention      `json:"retention"`
	ToolSettings  []localtools.ToolSetting  `json:"toolSettings,omitempty"`
	Notices       []string                  `json:"notices,omitempty"`
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
	Status      string                          `json:"status"`
	Diagnostics []FilePreviewRetainedDiagnostic `json:"diagnostics,omitempty"`
}
type FilePreviewRetainedDiagnostic struct {
	ID     string `json:"id"`
	CaseID string `json:"caseId"`
}

// WithFilePreviewRetention freezes a trusted resolved target before execution
// only when its checked current bytes match the parsed snapshot revision.
// Failure leaves retention requested but unavailable without changing Test.
func WithFilePreviewRetention(opt FilePreviewOptions, filename, revision string) FilePreviewOptions {
	opt.RetentionFile = filename
	scope, current, err := currentFileFailureScopeRevision(filename)
	if err == nil && current == revision {
		opt.RetentionFile = scope.filename
		opt.retentionScope = &scope
	}
	return opt
}

type FilePreviewCompletion struct {
	State         string                     `json:"state"`
	ReasonCode    string                     `json:"reasonCode"`
	Reason        string                     `json:"reason"`
	Answer        *string                    `json:"answer,omitempty"`
	AnswerExcerpt string                     `json:"answerExcerpt,omitempty"`
	Truncated     bool                       `json:"truncated,omitempty"`
	DurationMs    int64                      `json:"durationMs"`
	ToolCalls     int                        `json:"toolCalls,omitempty"`
	Tools         []agentpreview.ToolSummary `json:"tools,omitempty"`
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
	Failure    string                    `json:"failure,omitempty"`
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
		"invalid_file":      "Invalid agent file; repair the reported source field.",
		"no_tests":          "No tests yet — add or suggest one",
		"invalid_selection": "Unknown example ID; select an authored example ID.", "invalid_options": "Invalid file preview options.",
		"invalid_tool_settings":     "Tool settings in the agent folder's kmx.yaml are invalid.",
		"model_unavailable":         "Selected model route is unavailable or does not match the file model; select an exact trusted route.",
		"tools_unavailable":         "Host-bound tools are unavailable for the declared names.",
		"recording_mismatch":        "Recording mismatch: tool arguments differ from the saved call. Update the saved call before retrying.",
		"recording_source_changed":  "Agent file changed; recording was not saved.",
		"recording_unrepresentable": "Recording cannot fit the agent file format and limits; no recording was saved.",
		"recording_save_failed":     "Recording save failed.",
		"recording_missing":         "A Recorded tool has no saved call. Change the test or tool mode before comparing.",
		"tool_settings_changed":     "Tool settings changed. Reload before comparing.",
		"unsafe_tool_content":       "Unsafe tool preview content was withheld; nothing was journaled.",
		"credential_content":        "Credential-bearing preview content was withheld.",
		"runner_unavailable":        "Embedded preview runner is not available; no inference was performed.",
		"execution_failed":          "Local preview execution failed.", "model_reply_unusable": "The model replied in a form kmx cannot use, so the test did not finish.", "invalid_completion": "Runner returned incomplete or mismatched execution metadata.",
		"diagnostics_failed": "Cannot write local preview diagnostics.",
		"journal_failed":     "Preview journal finalization failed; the result is unknown.",
	}
	return reasons[code]
}

// PreviewAgentFile is the CLI path wrapper: load once, then execute that snapshot.
func (a *App) PreviewAgentFile(ctx context.Context, opt FilePreviewOptions) (FilePreviewResult, error) {
	started := a.timeNow()
	parsed, err := agentfile.Load(opt.File)
	if err == nil && !opt.SkipAssertions && opt.RetentionFile != "" && opt.retentionScope == nil {
		opt = WithFilePreviewRetention(opt, opt.RetentionFile, parsed.Revision)
	}
	return a.previewAgentSnapshot(ctx, parsed, opt, err, started)
}

// PreviewAgentSnapshot executes a caller-owned parsed snapshot without reopening
// source files. File is a display label only; callers retain target authority.
// The caller must obtain parsed from agentfile.Parse/Load and not mutate it while
// the operation runs. Preview results grant no native evidence/deploy authority.
func (a *App) PreviewAgentSnapshot(ctx context.Context, parsed *agentfile.Agent, opt FilePreviewOptions) (FilePreviewResult, error) {
	return a.previewAgentSnapshot(ctx, parsed, opt, nil, a.timeNow())
}

func (a *App) previewAgentSnapshot(ctx context.Context, parsed *agentfile.Agent, opt FilePreviewOptions, parseErr error, started time.Time) (result FilePreviewResult, resultErr error) {
	defer func() { result.DurationMs = previewDurationMilliseconds(a.timeNow().Sub(started)) }()
	filePath := suiteDiagnosticIdentity(filepath.Base(opt.File), nil)
	if opt.FullPath {
		var safe bool
		filePath, safe = suiteDiagnosticText(opt.File, len(opt.File), nil)
		if !safe {
			filePath = "withheld"
		}
	}
	scope := "full"
	if opt.CaseID != "" {
		scope = "subset"
	}
	r := FilePreviewResult{SchemaVersion: 1, Kind: "kmx.agent-file-preview", Operation: opt.Operation,
		File: FilePreviewIdentity{Path: filePath}, Selection: FilePreviewSelection{Scope: scope, CaseIDs: []string{}},
		State: "refused", Verdict: runtime.EvaluationUnknown, Examples: []FilePreviewExample{}, Retention: FilePreviewRetention{Status: "not_requested"}}
	fail := func(code string, exit int) (FilePreviewResult, *FilePreviewError) {
		r.FullPass = false
		r.Verdict = runtime.EvaluationUnknown
		r.ReasonCode = code
		r.Reason = previewReason(code)
		return r, &FilePreviewError{code: code, exit: exit}
	}
	recordingFail := func(err error) (FilePreviewResult, *FilePreviewError) {
		if errors.Is(err, context.Canceled) {
			r.State = "cancelled"
			return fail("cancelled", 130)
		}
		return fail(recordingReason(err), 1)
	}
	if opt.Operation != "run" && opt.Operation != "test" || opt.Operation == "test" && (opt.Input != nil || opt.SkipAssertions) || opt.Input != nil && (len(*opt.Input) > agentfile.MaxInputBytes || !utf8.ValidString(*opt.Input)) {
		return fail("invalid_options", 1)
	}
	if opt.SkipAssertions {
		opt.JudgeRoute = nil
		opt.RetentionFile, opt.retentionScope = "", nil
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
	if parsed.CopilotToolsOmitted {
		r.Notices = []string{agentfile.CopilotToolsNotice}
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
		if !opt.DefaultCopilot {
			return fail("model_unavailable", 1)
		}
		route = modelaccess.Route{Provider: "copilot", Model: parsed.Model, Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
	}
	if route.Model != parsed.Model || route.Validate() != nil || route.Provider == "fake" && route.Model != "fake" || route.Provider != "fake" && route.Model == "fake" {
		return fail("model_unavailable", 1)
	}
	r.ModelRoute = FilePreviewRoute{route.Provider, route.Model}
	judge := route
	if opt.JudgeRoute != nil {
		judge = *opt.JudgeRoute
	}
	if !opt.SkipAssertions {
		if judge.Validate() != nil || secretshapes.Match(judge.Model) != nil || judge.Provider == "fake" && judge.Model != "fake" || judge.Provider != "fake" && judge.Model == "fake" {
			return fail("model_unavailable", 1)
		}
		parsed.Examples = agentfile.BindJudgeModels(parsed.Examples, judge.Model)
	}
	selected := parsed.Examples
	if opt.CaseID != "" {
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
	if opt.AdHocOnly && opt.Input != nil {
		selected = nil
		r.Selection.Scope = "subset"
	}
	usesJudge := opt.JudgeRoute != nil
	for _, ex := range selected {
		for _, assertion := range ex.Assertions {
			usesJudge = usesJudge || assertion.Type == "llmJudge"
		}
	}
	if usesJudge && !opt.SkipAssertions {
		r.JudgeRoute = &FilePreviewRoute{judge.Provider, judge.Model}
	}
	if len(parsed.Tools) == 0 && opt.ToolBindings != "" {
		return fail("invalid_options", 1)
	}
	for _, ex := range selected {
		r.Selection.CaseIDs = append(r.Selection.CaseIDs, ex.ID)
		row := FilePreviewExample{ID: ex.ID, FilePreviewCompletion: previewCompletion("not_run", "not_run"), Verdict: runtime.EvaluationUnknown, Assertions: []FilePreviewAssertion{}}
		for _, as := range ex.Assertions {
			if opt.SkipAssertions {
				break
			}
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
	if opt.retentionScope != nil && opt.retentionScope.filename != opt.RetentionFile {
		return fail("invalid_options", 1)
	}
	if len(parsed.Tools) > 0 {
		source := opt.SourceFile
		if source == "" {
			source = opt.File
		}
		var settingsErr error
		r.ToolSettings, settingsErr = localtools.LoadSettings(ctx, source, parsed.Tools)
		if settingsErr != nil {
			if cause := ctx.Err(); cause != nil {
				r.State = "error"
				if errors.Is(cause, context.Canceled) {
					r.State = "cancelled"
					return fail("cancelled", 130)
				}
				return fail("deadline_exceeded", 1)
			}
			return fail("invalid_tool_settings", 1)
		}
	}
	if expected := opt.ExpectedToolSettings; expected != nil {
		if len(*expected) != len(r.ToolSettings) {
			return fail("tool_settings_changed", 1)
		}
		seen := map[string]bool{}
		for _, setting := range *expected {
			found := false
			for _, actual := range r.ToolSettings {
				if setting == actual {
					found = true
				}
			}
			if seen[setting.Name] || !found {
				return fail("tool_settings_changed", 1)
			}
			seen[setting.Name] = true
		}
	}
	if opt.RecordedReplayOnly {
		for _, ex := range selected {
			if ex.Mocks != nil {
				for _, tool := range ex.Mocks.Tools {
					disabled := false
					for _, s := range r.ToolSettings {
						if s.Name == tool.Name && s.State == localtools.Disabled {
							disabled = true
						}
					}
					if tool.Mode == "recorded" && !disabled && len(tool.Calls) == 0 {
						return fail("recording_missing", 1)
					}
				}
			}
		}
		r.RecordedReplayOnly = true
	}
	recordingSave, prepareErr := prepareRecordingSave(ctx, opt, parsed.Revision, selected, r.ToolSettings)
	if prepareErr != nil {
		return recordingFail(prepareErr)
	}
	request := agentpreview.Request{SkipAssertions: opt.SkipAssertions, Agent: parsed, Examples: selected, Route: route, JudgeRoute: opt.JudgeRoute, Input: opt.Input, Progress: opt.Progress, ToolBindings: opt.ToolBindings, FileLabel: r.File.Path, ToolSettings: append([]localtools.ToolSetting(nil), r.ToolSettings...)}
	request.ExampleFinished = previewCaseObserver(ctx, selected, parsed, route, opt.CaseFinished, opt.SkipAssertions)
	if opt.RouteSelected != nil {
		request.BeforeExecute = func() error { return opt.RouteSelected(r.ModelRoute) }
	}
	runCtx := ctx
	var judgeNotes fileJudgeNotes
	if !opt.SkipAssertions {
		runCtx, judgeNotes = newFileJudgeContext(ctx)
	}
	outcome, cause := a.FileRunner.Run(runCtx, request)
	if errors.Is(cause, agentpreview.ErrUnsafeToolContent) || errors.Is(cause, agentpreview.ErrCredentialContent) || errors.Is(cause, copilot.ErrCredentialContent) {
		r.File = FilePreviewIdentity{Path: "[agent file]"}
		r.ModelRoute = FilePreviewRoute{}
		r.JudgeRoute = nil
		r.ToolSettings = nil
		r.Selection = FilePreviewSelection{CaseIDs: []string{}}
		r.Examples = []FilePreviewExample{}
		r.AdHoc = nil
		if errors.Is(cause, agentpreview.ErrUnsafeToolContent) {
			return fail("unsafe_tool_content", 1)
		}
		return fail("credential_content", 1)
	}
	if errors.Is(cause, copilot.ErrToolsUnsupported) {
		result, previewErr := fail("tools_unavailable", 1)
		result.Reason = "Selected model does not support tool calls."
		previewErr.message = result.Reason
		return result, previewErr
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
	validCompletion := func(c agentpreview.Completion, ex agentfile.Example) bool {
		if !validPreviewTools(c, ex, parsed.Tools) {
			return false
		}
		if c.DurationMs < 0 || c.DurationMs > (5*time.Minute).Milliseconds() || c.ToolCalls < 0 || c.ToolCalls > localtools.MaxCalls || len(parsed.Tools) == 0 && c.ToolCalls != 0 || c.State != agentpreview.Complete || c.ID == "" || seen[c.ID] || c.FileRevision != parsed.Revision || c.Provider != route.Provider || c.Model != route.Model || len(c.Answer) > 256*1024 || !utf8.ValidString(c.Answer) {
			return false
		}
		seen[c.ID] = true
		return true
	}
	malformed := len(outcome.Examples) > len(selected) || (outcome.AdHoc == nil) != (opt.Input == nil)
	if outcome.AdHoc != nil {
		if validCompletion(*outcome.AdHoc, agentfile.Example{}) {
			c := previewCompleted(*outcome.AdHoc, opt.ShowContent)
			if opt.IncludeAnswer {
				c.Answer = previewFullAnswer(outcome.AdHoc.Answer)
			}
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
		expectedAssertions := len(ex.Assertions)
		if opt.SkipAssertions {
			expectedAssertions = 0
		}
		if !validCompletion(row.Completion, ex) || len(row.Assertions) != expectedAssertions {
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
		if opt.SkipAssertions {
			completion := previewCompleted(row.Completion, opt.ShowContent)
			if opt.IncludeAnswer {
				completion.Answer = previewFullAnswer(row.Completion.Answer)
			}
			r.Examples[i].FilePreviewCompletion = completion
			continue
		}
		verdict := runtime.EvaluationAssertionsVerdict(row.Assertions)
		show := opt.ShowContent || opt.ShowFailedContent && verdict == runtime.EvaluationFail
		if show {
			for j, as := range row.Assertions {
				note := judgeNotes[fileJudgeKey{ex.ID, as.ID}]
				r.Examples[i].Assertions[j].Judge = previewJudge(as, ex.Assertions[j], note, &judgeReasonBytes)
				if opt.IncludeFailureDetails && as.Verdict == runtime.EvaluationFail {
					r.Examples[i].Assertions[j].Failure = previewCheckFailure(ex.Assertions[j])
				}
			}
		}
		completion := previewCompleted(row.Completion, show)
		if opt.IncludeAnswer {
			completion.Answer = previewFullAnswer(row.Completion.Answer)
		}
		completion.ReasonCode = previewResultReasonCode(verdict)
		completion.Reason = previewReason(completion.ReasonCode)
		r.Examples[i].FilePreviewCompletion = completion
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
	if errors.Is(cause, agentpreview.ErrRecordingMismatch) {
		return fail("recording_mismatch", 1)
	}
	if errors.Is(cause, agentfile.ErrRecordingInvalid) {
		return fail("recording_unrepresentable", 1)
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
	if errors.Is(cause, agentpreview.ErrModelReply) {
		return fail("model_reply_unusable", 1)
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
		if row.State != "complete" || !opt.SkipAssertions && len(row.Assertions) == 0 {
			return fail("invalid_completion", 1)
		}
	}
	if r.AdHoc != nil && r.AdHoc.State != "complete" {
		return fail("invalid_completion", 1)
	}
	if saveErr := saveFileRecordings(ctx, recordingSave, outcome, selected, &r); saveErr != nil {
		return recordingFail(saveErr)
	}
	if opt.Operation == "test" && opt.retentionScope != nil {
		retainFilePreviewFailures(ctx, *opt.retentionScope, parsed, selected, route, judge, r.Selection.Scope, outcome.Examples, &r)
	} else if opt.Operation == "test" && opt.RetentionFile != "" && runtime.EvaluationAssertionsVerdict(all) != runtime.EvaluationPass {
		r.Retention.Status = "unavailable"
	}
	r.State = "complete"
	if opt.SkipAssertions || len(selected) == 0 {
		// An unchecked answer is not evidence: no verdict and never a vacuous full pass.
		r.Verdict, r.FullPass, r.ReasonCode = runtime.EvaluationUnknown, false, "completion_ready"
		r.Reason = previewReason(r.ReasonCode)
		return r, nil
	}
	r.Verdict = runtime.EvaluationAssertionsVerdict(all)
	r.ReasonCode = previewResultReasonCode(r.Verdict)
	r.Reason = previewReason(r.ReasonCode)
	r.FullPass = r.Selection.Scope == "full" && r.Verdict == runtime.EvaluationPass
	if r.Verdict != runtime.EvaluationPass {
		return r, &FilePreviewError{code: r.ReasonCode, exit: 2}
	}
	return r, nil
}

func retainFilePreviewFailures(ctx context.Context, scope fileFailureScope, agent *agentfile.Agent, selected []agentfile.Example, route, judge modelaccess.Route, selection string, outcomes []agentpreview.ExampleOutcome, result *FilePreviewResult) {
	nonpass := make([]int, 0, len(outcomes))
	for i, outcome := range outcomes {
		if runtime.EvaluationAssertionsVerdict(outcome.Assertions) != runtime.EvaluationPass {
			nonpass = append(nonpass, i)
		}
	}
	if len(nonpass) == 0 {
		return
	}
	if len(nonpass) > maxSuiteFailureRecords {
		result.Retention.Status = "unavailable"
		return
	}
	result.Retention.Status = "retained"
	operationID, err := randomHex(16)
	if err != nil {
		result.Retention.Status = "unavailable"
		return
	}
	judgeRoute := FilePreviewRoute{Provider: judge.Provider, Model: judge.Model}
	for _, i := range nonpass {
		outcome := outcomes[i]
		verdict := runtime.EvaluationAssertionsVerdict(outcome.Assertions)
		definition := selected[i]
		frame := suitePassingInventoryCase{
			Case:     runtime.EvaluationCase{ID: definition.ID, Input: definition.Input, Assertions: definition.Assertions},
			Metadata: suitePassingCase{Run: 1, Assertions: make([]suitePassingAssertion, len(definition.Assertions))},
		}
		observation := suitePassingObservation{EvaluationReceipt: runtime.EvaluationReceipt{
			CaseID: definition.ID, AnswerSHA256: runtime.EvaluationAnswerDigest(outcome.Completion.Answer),
			Verdict: verdict, Assertions: outcome.Assertions,
		}}
		detail := captureSuiteFailureWithReasons(frame, observation, outcome.Completion.Answer, nil, false, nil)
		if detail == nil {
			continue
		}
		// File preview aggregation gives a known failed assertion precedence over
		// an inconclusive sibling; keep that validated operation contract rather
		// than applying workspace repeat-policy unknown precedence here.
		detail.Verdict = string(verdict)
		detail.OperationID = "op-" + operationID
		assertions := make([]fileFailureAssertionRecord, len(outcome.Assertions))
		for j, assertion := range outcome.Assertions {
			assertions[j] = fileFailureAssertionRecord{
				ID: assertion.ID, Type: assertion.Type, Digest: assertion.DefinitionDigest,
				Verdict: assertion.Verdict, ReasonCode: previewAssertion(assertion.ID, assertion.Type, assertion.Verdict).ReasonCode,
			}
		}
		codec := retainedFileFailureCodec{
			revision: agent.Revision, agentID: agent.Identity.ID, operation: "test", selection: selection,
			model: FilePreviewRoute{Provider: route.Provider, Model: route.Model}, judge: judgeRoute, assertions: assertions,
		}
		id, saveErr := saveFileFailure(context.WithoutCancel(ctx), scope, codec, *detail)
		if id != "" {
			result.Retention.Diagnostics = append(result.Retention.Diagnostics, FilePreviewRetainedDiagnostic{ID: id, CaseID: detail.CaseID})
		}
		if saveErr != nil {
			result.Retention.Status = "unavailable"
			return
		}
	}
}

func previewCompletion(state, code string) FilePreviewCompletion {
	return FilePreviewCompletion{State: state, ReasonCode: code, Reason: previewReason(code)}
}
func previewCompleted(c agentpreview.Completion, show bool) FilePreviewCompletion {
	row := previewCompletion("complete", "completion_ready")
	row.DurationMs = c.DurationMs
	row.ToolCalls = c.ToolCalls
	row.Tools = append([]agentpreview.ToolSummary(nil), c.Tools...)
	if show {
		row.AnswerExcerpt, _ = suiteDiagnosticText(c.Answer, 4096, nil)
		full, _ := suiteDiagnosticText(c.Answer, len(c.Answer), nil)
		row.Truncated = len(row.AnswerExcerpt) < len(full)
	}
	return row
}

func previewDurationMilliseconds(elapsed time.Duration) int64 {
	if elapsed <= 0 {
		return 0
	}
	return elapsed.Milliseconds()
}

func previewResultReasonCode(verdict runtime.EvaluationVerdict) string {
	if verdict == runtime.EvaluationPass {
		return "checks_passed"
	}
	if verdict == runtime.EvaluationFail {
		return "assertion_failed"
	}
	return "judge_unknown"
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
