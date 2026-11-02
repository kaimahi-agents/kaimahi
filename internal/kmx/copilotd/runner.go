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

type Options struct {
	Host           string
	ExampleTimeout time.Duration // caller supplies the existing example limit; no CLI input
	Access         func(context.Context, func(ahptypes.ActionEnvelope) error) (copilot.HostAccess, error)
	Snapshot       func(context.Context, func(...string) error) (agentpreview.SourceSnapshot, error)
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
	for _, ex := range req.Examples {
		if ex.Mocks != nil {
			return out, ErrUnsupported
		}
		for _, as := range ex.Assertions {
			if as.Type == "llmJudge" {
				return out, ErrUnsupported
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
	if req.BeforeExecute != nil {
		if err = req.BeforeExecute(); err != nil {
			return out, fmt.Errorf("%w", agentpreview.ErrDiagnosticsFailed)
		}
	}
	execute := func(ex agentfile.Example) (c agentpreview.Completion, cause error) {
		start := time.Now()
		limit := r.options.ExampleTimeout
		if limit <= 0 {
			limit = 2 * time.Minute
		}
		caseCtx, stop := context.WithTimeout(ctx, limit)
		defer stop()
		c = agentpreview.Completion{State: agentpreview.Failed, Provider: req.Route.Provider, Model: req.Route.Model, FileRevision: req.Agent.Revision, SnapshotRevision: req.SnapshotRevision}
		defer func() {
			c.DurationMs = time.Since(start).Milliseconds()
			if c.Native != nil {
				metadata, _ := json.Marshal(c.Native)
				if e := access.CheckContent(string(metadata)); e != nil {
					c.Native = nil
					c.Answer = ""
					c.State = agentpreview.Failed
					cause = e
				}
			}
		}()
		if req.ValidateSource != nil {
			if cause = req.ValidateSource(caseCtx); cause != nil {
				return
			}
		}
		snapshot, e := r.options.Snapshot(caseCtx, access.CheckContent)
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
				if access.CheckContent(s) == nil {
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
		if cause = access.CheckContent(agentpreview.SessionCreatedLine(ex.ID, session)); cause != nil {
			return
		}
		if req.SessionCreated != nil {
			if e := req.SessionCreated(ex.ID, session); e != nil {
				cause = agentpreview.ErrDiagnosticsFailed
				return
			}
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
		mu.Lock()
		active = t
		mu.Unlock()
		defer func() {
			mu.Lock()
			active = nil
			mu.Unlock()
			c.Native.ActualModels = t.observed()
			c.Native.PendingTool, c.Native.PendingInput = t.pending()
			if len(c.Native.PendingInput) > 4096 {
				c.Native.PendingInput = c.Native.PendingInput[:4096]
			}
		}()
		if cause = access.Validate(caseCtx); cause != nil {
			return
		}
		_, cause = client.Dispatch(caseCtx, string(chat), ahptypes.StateAction{Value: &ahptypes.ChatTurnStartedAction{Type: ahptypes.ActionType("chat/turnStarted"), TurnId: turn, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Message: ahptypes.Message{Text: ex.Input, Origin: ahptypes.MessageOrigin{Kind: ahptypes.MessageKind("user")}, Agent: &ahptypes.AgentSelection{Uri: ahptypes.URI(agent)}, Model: &ahptypes.ModelSelection{Id: req.Route.Model}}}})
		if cause != nil {
			return
		}
		select {
		case <-t.done:
		case <-client.Done():
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
		if cause = access.CheckContent(answer); cause != nil {
			return
		}
		c.Answer = answer
		c.Native.ActualModels = models
		c.State = agentpreview.Complete
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
		for _, name := range ordered {
			count := counts[name]
			c.ToolCalls += count
			c.Tools = append(c.Tools, agentpreview.ToolSummary{Name: name, Mode: "live", Source: "live", CallCount: count, RequestCount: count})
		}
		return
	}
	if req.Input != nil {
		c, e := execute(agentfile.Example{ID: "input", Input: *req.Input})
		out.AdHoc = &c
		if e != nil {
			return out, e
		}
	}
	for _, ex := range req.Examples {
		c, e := execute(ex)
		row := agentpreview.ExampleOutcome{ID: ex.ID, Completion: c}
		if e == nil && !req.SkipAssertions {
			counts := map[string]int{}
			for _, tool := range c.Tools {
				counts[tool.Name] = tool.CallCount
			}
			row.Assertions = runtime.EvaluateAssertions(runtime.EvaluationCase{ID: ex.ID, Input: ex.Input, Assertions: ex.Assertions}, runtime.EvaluationEvidence{Answer: c.Answer, AnswerAvailable: true, ToolSupported: true, ToolEvidenceComplete: true, ToolCalls: counts})
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
