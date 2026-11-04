package copilotd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

var (
	ErrUnsupported           = errors.New("selected case semantics cannot run natively")
	ErrAgentNotDiscovered    = errors.New("native agent not discovered")
	ErrAgentAmbiguous        = errors.New("native agent name is ambiguous")
	ErrAgentMetadataMismatch = errors.New("native agent metadata does not match authored source")
)

// JudgeAccess scores selected llmJudge assertions on a transport separate from
// the host. CheckContent covers both the host's and the judge's credentials, so
// the runner can use it for every retained or transferred value. Validate covers
// only the judge's own lifecycle. Evaluate runs under the case's context and
// returns a nonnil error when the judge itself cannot run; an unusable score is
// an unknown row, not an error.
type JudgeAccess interface {
	CheckContent(...string) error
	Validate(context.Context) error
	Evaluate(context.Context, runtime.EvaluationCase, runtime.EvaluationEvidence) ([]runtime.EvaluationAssertionResult, error)
}

var errJudgeRows = errors.New("separate judge returned rows that do not match the selected assertions")

type Options struct {
	Host           string
	ExampleTimeout time.Duration // caller supplies the existing example limit; no CLI input
	Access         func(context.Context, func(ahptypes.ActionEnvelope) error) (copilot.HostAccess, error)
	Snapshot       func(context.Context, func(...string) error) (agentpreview.SourceSnapshot, error)
	// Judge prepares an invocation-local separate judge once, after host access and
	// before any send. It receives the host's content checker and is called only
	// when a selected case has an llmJudge assertion. A nonnil capability returned
	// with an error is content-check-only.
	Judge func(context.Context, agentpreview.Request, func(...string) error) (JudgeAccess, error)
}
type runner struct{ options Options }

func NewRunner(o Options) agentpreview.Runner { return &runner{o} }
func nonce() string                           { return uuid.NewString() }
func (r *runner) Run(ctx context.Context, req agentpreview.Request) (out agentpreview.Outcome, err error) {
	out.NoJournal = true
	out.State = agentpreview.Failed
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	hasJudge := false
	for _, ex := range req.Examples {
		if ex.Mocks != nil {
			return out, ErrUnsupported
		}
		for _, as := range ex.Assertions {
			if as.Type == "llmJudge" {
				if r.options.Judge == nil {
					return out, ErrUnsupported
				}
				hasJudge = hasJudge || !req.SkipAssertions
			}
		}
	}
	for _, s := range req.ToolSettings {
		if s.State != localtools.Enabled {
			return out, ErrUnsupported
		}
	}
	if req.Agent == nil || r.options.Access == nil || r.options.Snapshot == nil {
		return out, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var active *trace
	var mu sync.Mutex
	access, setupErr := r.options.Access(ctx, func(e ahptypes.ActionEnvelope) error {
		mu.Lock()
		t := active
		mu.Unlock()
		if t != nil {
			return t.accept(e)
		}
		return nil
	})
	if access != nil {
		defer access.Close(context.Background())
	}
	fields := append([]string{}, req.SourceText...)
	fields = append(fields, req.Agent.Instructions, req.Agent.Frontmatter, req.Agent.Title, req.Route.Model, req.FileLabel, r.options.Host)
	if req.Input != nil {
		fields = append(fields, *req.Input)
	}
	for _, ex := range req.Examples {
		fields = append(fields, ex.ID, ex.Input)
	}
	if access != nil {
		if err = access.CheckContent(fields...); err != nil {
			return out, err
		}
	}
	if setupErr != nil {
		return out, setupErr
	}
	if access == nil {
		return out, copilot.ErrHostConnection
	}
	// One checker covers host and judge credentials everywhere below.
	check := access.CheckContent
	var judge JudgeAccess
	if hasJudge {
		var judgeErr error
		judge, judgeErr = r.options.Judge(ctx, req, access.CheckContent)
		if judge != nil {
			check = judge.CheckContent
			if err = checkJudgeContent(check, req, fields); err != nil {
				return out, err
			}
		}
		if judgeErr != nil {
			return out, judgeErr
		}
		if judge == nil {
			return out, ErrUnsupported
		}
	}
	if req.TelemetryReady != nil {
		if e := req.TelemetryReady(check); e != nil {
			return out, errors.Join(agentpreview.ErrDiagnosticsFailed, e)
		}
	}
	if req.BeforeExecute != nil {
		if err = req.BeforeExecute(); err != nil {
			return out, fmt.Errorf("%w", agentpreview.ErrDiagnosticsFailed)
		}
	}
	execute := func(ex agentfile.Example, index int) (c agentpreview.Completion, rows []runtime.EvaluationAssertionResult, cause error) {
		start := time.Now()
		limit := r.options.ExampleTimeout
		if limit <= 0 {
			limit = 2 * time.Minute
		}
		caseCtx, stop := context.WithTimeout(ctx, limit)
		defer stop()
		c = agentpreview.Completion{Telemetry: &agentpreview.Telemetry{}, State: agentpreview.Failed, Provider: req.Route.Provider, Model: req.Route.Model, FileRevision: req.Agent.Revision, SnapshotRevision: req.SnapshotRevision}
		defer func() {
			c.DurationMs = time.Since(start).Milliseconds()
			if c.Native != nil {
				if e := checkDocument(check, c.Native); e != nil {
					c.Native = nil
					c.Answer = ""
					c.State = agentpreview.Failed
					rows = nil
					cause = e
				}
			}
		}()
		if req.ValidateSource != nil {
			if cause = req.ValidateSource(caseCtx); cause != nil {
				return
			}
		}
		if cause = caseCtx.Err(); cause != nil {
			return
		}
		if cause = req.ObserveCase(index, agentpreview.Observation{Event: "case.started"}); cause != nil {
			return
		}
		snapshot, e := r.options.Snapshot(caseCtx, check)
		if e != nil {
			cause = e
			return
		}
		session := "ahp-session:/" + nonce()
		c.ID = session
		hostVersion, protocol := access.Info()
		c.Native = &agentpreview.NativeExecution{Host: r.options.Host, HostVersion: hostVersion, ProtocolVersion: protocol, Session: session, GitRevision: snapshot.GitRevision, ContentRevision: snapshot.ContentRevision}
		client := access.Client()
		owned := false
		defer func() {
			cleanCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			warn := func(s string) {
				if check(s) == nil {
					out.Notices = append(out.Notices, s)
				}
			}
			if owned {
				if e := client.Request(cleanCtx, "disposeSession", ahptypes.DisposeSessionParams{Channel: ahptypes.URI(session)}, &struct{}{}); e != nil {
					warn(fmt.Sprintf("Cleanup unconfirmed: possible owned session %q", session))
				}
			}
			if e := snapshot.Close(cleanCtx); e != nil {
				warn(fmt.Sprintf("Cleanup unconfirmed: possible owned snapshot %q", snapshot.Directory))
			}
		}()
		if cause = access.Validate(caseCtx); cause != nil {
			return
		}
		if client == nil {
			cause = copilot.ErrHostConnection
			return
		}
		provider := "copilot"
		directory := (&url.URL{Scheme: "file", Path: snapshot.Directory}).String()
		cause = client.Request(caseCtx, "createSession", ahptypes.CreateSessionParams{Channel: ahptypes.URI(session), Provider: &provider, WorkingDirectories: []ahptypes.URI{ahptypes.URI(directory)}, Config: map[string]json.RawMessage{"target": json.RawMessage(`"workspace"`)}}, &struct{}{})
		if cause != nil {
			out.Notices = append(out.Notices, fmt.Sprintf("Creation unconfirmed: possible owned session %q", session))
			return
		}
		owned = true
		if cause = check(agentpreview.SessionCreatedLine(ex.ID, session)); cause != nil {
			return
		}
		if req.SessionCreated != nil {
			if e := req.SessionCreated(ex.ID, session); e != nil {
				cause = agentpreview.ErrDiagnosticsFailed
				return
			}
		}
		if cause = req.ObserveCase(index, agentpreview.Observation{Event: "session.observed", Session: session}); cause != nil {
			return
		}
		source := req.SourceAgent
		if source == nil {
			source = req.Agent
		}
		var state *ahptypes.SessionState
		var agent string
		for {
			result, sub, e := client.Subscribe(caseCtx, session)
			if sub != nil {
				sub.Close()
			}
			if e != nil {
				cause = e
				return
			}
			state = result.Snapshot.State.Session
			if state != nil {
				agent, e = findAgent(state.Customizations, source)
				if state.CreationError != nil {
					cause = errTrace
					return
				}
				if state.Lifecycle == ahptypes.SessionLifecycleReady && state.Customizations != nil {
					if e != nil {
						cause = e
						return
					}
					if len(state.Chats) > 0 {
						break
					}
				}
			}
			select {
			case <-caseCtx.Done():
				cause = caseCtx.Err()
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		// Name selection checks advertised metadata, not exact source attestation.
		c.Native.Selection = "name"
		chat := state.Chats[0].Resource
		if state.DefaultChat != nil {
			chat = *state.DefaultChat
		}
		c.Native.Chat = string(chat)
		if state.Config != nil {
			json.Unmarshal(state.Config.Values["effectiveApprovalMode"], &c.Native.EffectiveApprovalMode)
		}
		initial, sub, e := client.Subscribe(caseCtx, string(chat))
		if sub != nil {
			defer sub.Close()
		}
		if e != nil || initial.Snapshot.State.Chat == nil {
			cause = errTrace
			return
		}
		turn := nonce()
		c.Native.Turn = turn
		t := newTrace(string(chat), turn, req.Route.Model, *initial.Snapshot.State.Chat)
		if req.Observed != nil && index >= 0 {
			t.observe = func(o agentpreview.Observation) error {
				o.Session = session
				o.Chat = string(chat)
				return req.ObserveCase(index, o)
			}
		}
		mu.Lock()
		active = t
		mu.Unlock()
		defer func() {
			mu.Lock()
			active = nil
			mu.Unlock()
			metadata := t.telemetry()
			metadata.Sent = c.Telemetry.Sent
			c.Telemetry = &metadata
			c.Native.ActualModels = t.observed()
			c.Native.PendingTool, c.Native.PendingInput = t.pending()
			if len(c.Native.PendingInput) > 4096 {
				c.Native.PendingInput = c.Native.PendingInput[:4096]
			}
		}()
		if cause = access.Validate(caseCtx); cause != nil {
			return
		}
		c.Telemetry.Sent = true // A failed/lost acknowledgment cannot prove no send.
		_, cause = client.Dispatch(caseCtx, string(chat), ahptypes.StateAction{Value: &ahptypes.ChatTurnStartedAction{Type: ahptypes.ActionType("chat/turnStarted"), TurnId: turn, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Message: ahptypes.Message{Text: ex.Input, Origin: ahptypes.MessageOrigin{Kind: ahptypes.MessageKind("user")}, Agent: &ahptypes.AgentSelection{Uri: ahptypes.URI(agent)}, Model: &ahptypes.ModelSelection{Id: req.Route.Model}}}})
		if cause != nil {
			return
		}
		select {
		case <-t.done:
		case <-client.Done():
			if req.Observed != nil {
				cause = t.failure()
				if cause == nil {
					cause = copilot.ErrHostConnection
				}
			}
		case <-caseCtx.Done():
			cause = caseCtx.Err()
		}
		// A receive-side semantic refusal closes the transport; keep its cause.
		if cause == nil {
			cause = t.failure()
		}
		if cause != nil {
			cancelCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = client.Dispatch(cancelCtx, string(chat), ahptypes.StateAction{Value: &ahptypes.ChatTurnCancelledAction{Type: ahptypes.ActionType("chat/turnCancelled"), TurnId: turn, Duration: time.Since(start).Milliseconds()}})
			done()
			return
		}
		final, finalSub, e := client.Subscribe(caseCtx, string(chat))
		if finalSub != nil {
			defer finalSub.Close()
		}
		if e != nil {
			cause = e
			return
		}
		answer, models, counts, e := t.finish(final.Snapshot.State.Chat)
		if e != nil {
			cause = e
			return
		}
		if req.ValidateSource != nil {
			if cause = req.ValidateSource(caseCtx); cause != nil {
				return
			}
		}
		if cause = check(answer); cause != nil {
			return
		}
		names := map[string]bool{}
		for _, name := range req.Agent.Tools {
			names[name] = true
		}
		for name := range counts {
			names[name] = true
		}
		ordered := []string{}
		for name := range names {
			ordered = append(ordered, name)
		}
		sort.Strings(ordered)
		var tools []agentpreview.ToolSummary
		total := 0
		for _, name := range ordered {
			if cause = check(name); cause != nil {
				return
			}
			count := counts[name]
			total += count
			tools = append(tools, agentpreview.ToolSummary{Name: name, Mode: "live", Source: "live", CallCount: count, RequestCount: count})
		}
		if !req.SkipAssertions && len(ex.Assertions) > 0 {
			evaluation := runtime.EvaluationCase{ID: ex.ID, Input: ex.Input, Assertions: ex.Assertions}
			evidence := runtime.EvaluationEvidence{Answer: answer, AnswerAvailable: true, ToolSupported: true, ToolEvidenceComplete: true, ToolCalls: counts}
			if judge != nil && hasLLMJudge(ex) {
				// The separate judge shares this case's budget; its errors and the
				// context are latched before the source or provider is trusted again.
				var judged []runtime.EvaluationAssertionResult
				judged, cause = judge.Evaluate(caseCtx, evaluation, evidence)
				if interrupted := caseCtx.Err(); interrupted != nil {
					cause = interrupted
				}
				if cause != nil {
					return
				}
				if cause = matchJudgeRows(judged, ex.Assertions); cause != nil {
					return
				}
				if cause = judge.Validate(caseCtx); cause != nil {
					return
				}
				if cause = access.Validate(caseCtx); cause != nil {
					return
				}
				if req.ValidateSource != nil {
					if cause = req.ValidateSource(caseCtx); cause != nil {
						if !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
							cause = errors.Join(agentpreview.ErrSourceChanged, cause)
						}
						return
					}
				}
				rows = judged
			} else {
				rows = runtime.EvaluateAssertions(evaluation, evidence)
			}
		}
		c.Answer = answer
		c.Native.ActualModels = models
		c.State = agentpreview.Complete
		c.ToolCalls, c.Tools = total, tools
		return
	}
	if req.Input != nil {
		c, _, e := execute(agentfile.Example{ID: "input", Input: *req.Input}, -1)
		out.AdHoc = &c
		if e != nil {
			return out, e
		}
	}
	for index, ex := range req.Examples {
		c, rows, e := execute(ex, index)
		row := agentpreview.ExampleOutcome{ID: ex.ID, Completion: c}
		if e == nil {
			row.Assertions = rows
		}
		out.Examples = append(out.Examples, row)
		if e != nil {
			return out, e
		}
		if req.ExampleFinished != nil {
			req.ExampleFinished(row)
		}
	}
	out.State = agentpreview.Complete
	return out, nil
}
func findAgent(customizations []ahptypes.Customization, source *agentfile.Agent) (string, error) {
	if source == nil || source.Title == "" {
		return "", ErrAgentNotDiscovered
	}
	var candidate ahptypes.AgentCustomization
	count, selectable := 0, false
	var walk func(json.RawMessage, bool)
	walk = func(raw json.RawMessage, enabled bool) {
		var nodes []struct {
			ahptypes.AgentCustomization
			Children json.RawMessage
		}
		if json.Unmarshal(raw, &nodes) != nil {
			return
		}
		for _, n := range nodes {
			nodeEnabled := enabled && (n.Enabled == nil || *n.Enabled)
			if n.Type == "agent" && n.Name == source.Title {
				count++
				candidate = n.AgentCustomization
				selectable = nodeEnabled
			}
			if len(n.Children) > 0 {
				walk(n.Children, nodeEnabled)
			}
		}
	}
	raw, _ := json.Marshal(customizations)
	walk(raw, true)
	// Count the whole tree before filtering metadata or enablement. Even a
	// disabled same-name entry makes name-based selection ambiguous.
	if count > 1 {
		return "", ErrAgentAmbiguous
	}
	if count == 0 || !selectable || candidate.Uri == "" {
		return "", ErrAgentNotDiscovered
	}
	value := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	tools := func(names []string) map[string]bool {
		set := make(map[string]bool, len(names))
		for _, name := range names {
			set[name] = true
		}
		return set
	}
	want, got := tools(source.Tools), tools(candidate.Tools)
	matches := len(want) == len(got)
	for name := range want {
		matches = matches && got[name]
	}
	// The protocol omits both missing and empty tools. Matching empty sets
	// does not attest that tools are disabled.
	if value(candidate.Description) != source.Identity.Purpose || value(candidate.Model) != source.Model || !matches {
		return "", ErrAgentMetadataMismatch
	}
	return string(candidate.Uri), nil
}

func hasLLMJudge(ex agentfile.Example) bool {
	for _, a := range ex.Assertions {
		if a.Type == "llmJudge" {
			return true
		}
	}
	return false
}

// matchJudgeRows keeps a judge from dropping, reordering or redefining assertions.
func matchJudgeRows(rows []runtime.EvaluationAssertionResult, want []runtime.EvaluationAssertion) error {
	if len(rows) != len(want) {
		return errJudgeRows
	}
	for i, row := range rows {
		if row.ID != want[i].ID || row.Type != want[i].Type || row.DefinitionDigest != runtime.EvaluationAssertionDigest(want[i]) {
			return errJudgeRows
		}
	}
	return nil
}

// checkJudgeContent adds what only a judge receives or could echo: every
// assertion (selected or not, in the original source too) and the original
// source's strings, using decoded values as well as the encoded document.
func checkJudgeContent(check func(...string) error, req agentpreview.Request, fields []string) error {
	if err := check(fields...); err != nil {
		return err
	}
	documents := []any{req.JudgeRoute, req.Examples, req.Agent}
	if req.SourceAgent != nil {
		documents = append(documents, req.SourceAgent)
	}
	for _, document := range documents {
		if err := checkDocument(check, document); err != nil {
			return err
		}
	}
	return nil
}

func checkDocument(check func(...string) error, document any) error {
	raw, err := json.Marshal(document)
	if err != nil {
		return err
	}
	if err = check(string(raw)); err != nil {
		return err
	}
	var decoded any
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	return checkStrings(check, decoded)
}

func checkStrings(check func(...string) error, value any) error {
	switch v := value.(type) {
	case string:
		return check(v)
	case []any:
		for _, item := range v {
			if err := checkStrings(check, item); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range v {
			if err := check(key); err != nil {
				return err
			}
			if err := checkStrings(check, item); err != nil {
				return err
			}
		}
	}
	return nil
}
