package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

var ErrMarkdownTestsFailed = errors.New("local preview tests did not pass")

type MarkdownCaseResult struct {
	ID      string                                   `json:"id"`
	Verdict string                                   `json:"verdict"`
	Checks  []agentruntime.EvaluationAssertionResult `json:"checks"`
	Reason  string                                   `json:"reason,omitempty"`
	Model   string                                   `json:"model,omitempty"`
	Answer  string                                   `json:"-"`
}
type MarkdownTestResult struct {
	BindingDigest string               `json:"bindingDigest,omitempty"`
	SchemaVersion int                  `json:"schemaVersion"`
	ID            string               `json:"id"`
	AgentDigest   string               `json:"agentDigest"`
	TestsDigest   string               `json:"testsDigest"`
	PairDigest    string               `json:"pairDigest"`
	RunKey        string               `json:"runKey"`
	Model         string               `json:"model"`
	Runtime       string               `json:"runtime"`
	Adapter       string               `json:"adapter"`
	Mode          string               `json:"mode"`
	Selected      string               `json:"selected,omitempty"`
	Started       time.Time            `json:"started"`
	Completed     time.Time            `json:"completed"`
	Status        string               `json:"status"`
	Stale         bool                 `json:"stale"`
	Cases         []MarkdownCaseResult `json:"cases"`
}
type MarkdownSession struct {
	stateMu             sync.RWMutex
	InitialTest         bool
	Out, Err            io.Writer
	Provider, Model     string
	executor            markdownExecutor
	runtime             string
	instance            *agentLease
	frozen              *MarkdownAgent
	ownedManaged        string
	SelectedSession     string
	NewSession          bool
	SessionLabel        string
	conversationDisplay string // UI-owned identity; executor may be busy in a worker
}

func (s *MarkdownSession) executionState() (markdownExecutor, string) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.executor, s.ownedManaged
}
func (s *MarkdownSession) setExecutionState(executor markdownExecutor, owned string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.executor, s.ownedManaged = executor, owned
}

func NewMarkdownSession(out, err io.Writer) *MarkdownSession {
	return &MarkdownSession{Out: out, Err: err, executor: &markdownCopilot{}}
}
func (s *MarkdownSession) Close() { s.executor.Close(); s.finishInstance(nil) }
func (s *MarkdownSession) Reset() { s.executor.Reset(); s.finishInstance(nil) } // lifecycle cleanup/reload, not conversation reset
func (s *MarkdownSession) ResetConversation(ctx context.Context) error {
	if e, ok := s.executor.(*attachedAgentExecutor); ok {
		return e.selectSession(ctx, "", true, "conversation")
	}
	s.executor.Reset()
	return nil
}
func (s *MarkdownSession) conversationLabel() string {
	if e, ok := s.executor.(*attachedAgentExecutor); ok {
		return e.session + " (" + e.label + ")"
	}
	return ""
}
func (s *MarkdownSession) finishInstance(cause error) {
	if s.ownedManaged != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		var err error
		if executor, ok := s.executor.(*attachedAgentExecutor); ok {
			current, e := FindAgentInstance(s.ownedManaged)
			if e != nil {
				err = e
			} else if current.State == "running" && current.Generation == executor.record.Generation && current.Version == executor.record.Version {
				_, err = agentWorkerCall(ctx, executor.record, "stop", "")
			}
		}
		cancel()
		if err != nil {
			fmt.Fprintln(s.Err, "Could not stop managed agent:", err)
		}
		s.setExecutionState(&markdownCopilot{}, "")
	}
	if s.instance != nil {
		if err := s.instance.finish(cause); err != nil {
			fmt.Fprintln(s.Err, "Could not save stopped agent state:", err)
		}
		s.instance = nil
		s.frozen = nil
	}
}
func (s *MarkdownSession) StartAgent(ctx context.Context, a *MarkdownAgent) error {
	if _, ok := s.executor.(*attachedAgentExecutor); ok {
		_, err := s.executor.Start(ctx)
		return err
	}
	if _, ok := s.executor.(*markdownCopilot); ok {
		previous, e := FindAgentInstance(a.Path)
		alreadyRunning := e == nil && previous.State == "running"
		record, err := s.StartBackgroundAgent(ctx, a)
		if err != nil {
			return err
		}
		s.executor.Close()
		owned := ""
		if !alreadyRunning {
			owned = record.ID
		}
		s.setExecutionState(&attachedAgentExecutor{record: record}, owned)
		if err := s.executor.(*attachedAgentExecutor).selectSession(ctx, s.SelectedSession, s.NewSession, s.SessionLabel); err != nil {
			s.finishInstance(err)
			return err
		}
		s.NewSession = false
		return nil
	}
	if err := attachApprovedTools(a); err != nil {
		return err
	}
	if s.instance != nil {
		if s.instance.record.Version == a.Revision {
			return nil
		}
		return errors.New("agent changed; reload to start its new version")
	}
	if err := s.checkSelection(a); err != nil {
		return err
	}
	lease, frozen, err := beginAgentInstance(a, s.effective(a).Model)
	if err != nil {
		return err
	}
	s.instance, s.frozen = lease, frozen
	v, err := s.executor.Start(ctx)
	if err != nil {
		s.finishInstance(err)
		return err
	}
	s.runtime = v
	fmt.Fprintf(s.Err, "Agent %s · running · version %s · run %d\n", a.Name, mdShort(a.Revision), lease.record.Generation)
	return nil
}
func (s *MarkdownSession) AnswerAgent(ctx context.Context, a *MarkdownAgent, input string) (markdownAnswer, error) {
	if _, ok := s.executor.(*attachedAgentExecutor); ok {
		return s.executor.Answer(ctx, a, input, nil)
	}
	if err := s.StartAgent(ctx, a); err != nil {
		return markdownAnswer{}, err
	}
	if _, ok := s.executor.(*attachedAgentExecutor); ok {
		return s.executor.Answer(ctx, a, input, nil)
	}
	answer, err := s.executor.Answer(ctx, s.frozen, input, nil)
	if err != nil {
		s.executor.Reset()
		s.finishInstance(err)
	}
	return answer, err
}
func (s *MarkdownSession) effective(a *MarkdownAgent) *MarkdownAgent {
	copy := *a
	if s.Model != "" {
		copy.Model = s.Model
	}
	return &copy
}
func (s *MarkdownSession) checkSelection(a *MarkdownAgent) error {
	if len(a.Unmapped) > 0 {
		return fmt.Errorf("unsupported execution settings: %s; imported source is preserved", strings.Join(a.Unmapped, ", "))
	}
	if a.ToolsOmitted {
		fmt.Fprintln(s.Err, "Tools omitted: this KMX host exposes only explicitly approved local bindings; Copilot host defaults are not imported.")
	}
	if err := a.CheckCurrent(); err != nil {
		return err
	}
	if s.Model != "" && !localModelName.MatchString(s.Model) {
		return errors.New("use a valid model identifier")
	}
	if s.Provider != "" && s.Provider != "copilot" {
		return errors.New("only provider copilot is supported")
	}
	if s.Provider == "" {
		p, err := ReadMarkdownPreferences()
		if err != nil {
			return err
		}
		if p.LocalAgents.DefaultProvider != "copilot" {
			return errors.New("choose local inference with kmx settings --provider copilot, or pass --provider copilot for this run")
		}
	}
	return nil
}
func (s *MarkdownSession) prepare(ctx context.Context, a *MarkdownAgent) error {
	if err := s.checkSelection(a); err != nil {
		return err
	}
	v, err := s.executor.Start(ctx)
	if err != nil {
		return err
	}
	s.runtime = v
	return nil
}
func (s *MarkdownSession) Run(ctx context.Context, a *MarkdownAgent, input string) error {
	if strings.TrimSpace(input) == "" || len(input) > 64<<10 {
		return errors.New("provide a nonempty prompt of at most 64 KiB")
	}
	answer, err := s.AnswerAgent(ctx, a, input)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(s.Out, safeTerminal(answer.Text))
	return err
}
func (s *MarkdownSession) Evaluate(ctx context.Context, a *MarkdownAgent, selected string, emit func(MarkdownCaseResult)) (result MarkdownTestResult, err error) {
	cases := a.Examples
	grant, binding, grantErr := readCurrentToolGrant(a.Path)
	if grant != nil && grant.AgentDigest != a.Revision && len(a.Tools) == 0 && len(a.CLITools) == 0 {
		grant, binding, grantErr = nil, "", nil
	}
	if grantErr != nil {
		return result, grantErr
	}
	if grant != nil {
		if grant.ToolsDigest != a.ToolsRevision {
			return result, errors.New("tool companion changed; review approval before testing")
		}
		if grant.AgentDigest != a.Revision {
			return result, errors.New("tool approval is stale; suggest/review the edited definition before testing")
		}
		copy := *a
		copy.Tools = append([]string{}, grant.Tools...)
		copy.Mocks = make(map[string][]MarkdownMock, len(a.Mocks))
		for id, mocks := range a.Mocks {
			for _, mock := range mocks {
				for _, tool := range copy.Tools {
					if mock.Name == tool {
						copy.Mocks[id] = append(copy.Mocks[id], mock)
						break
					}
				}
			}
		}
		a = &copy
		for _, c := range cases {
			if err := validateMarkdownMocks(a.Tools, a.Mocks[c.ID]); err != nil {
				return result, fmt.Errorf("case %s needs mocks for the approved tool binding: %w", c.ID, err)
			}
		}
	}
	if len(cases) == 0 {
		return result, errors.New("no tests: add cases to the paired .agent.test.md file")
	}
	if selected != "" {
		cases = nil
		for _, c := range a.Examples {
			if c.ID == selected {
				cases = append(cases, c)
			}
		}
		if len(cases) == 0 {
			return result, errors.New("unknown test ID")
		}
	}
	for _, c := range cases {
		if e := validateMarkdownMocks(a.Tools, a.Mocks[c.ID]); e != nil {
			return result, e
		}
	}
	if err = s.prepare(ctx, a); err != nil {
		return result, err
	}
	a = s.effective(a)
	mode := "tool-free"
	if len(a.Tools) > 0 {
		mode = "mocked-tools"
	}
	for _, c := range cases {
		if len(a.Mocks[c.ID]) > 0 {
			mode = "mocked-tools"
		}
	}
	result = MarkdownTestResult{SchemaVersion: 1, ID: fmt.Sprintf("%d", time.Now().UnixNano()), AgentDigest: a.Revision, TestsDigest: a.TestsRevision, PairDigest: a.PairRevision, Model: a.Model, Runtime: s.runtime, Adapter: markdownAdapterVersion, Mode: mode, Selected: selected, Started: time.Now().UTC(), Status: "running", Cases: []MarkdownCaseResult{}}
	result.BindingDigest = binding
	result.RunKey = mdJSONHash(struct{ Schema, Pair, Adapter, Runtime, Model, Mode, Binding string }{"kmx.md-test/v2", a.PairRevision, markdownAdapterVersion, s.runtime, a.Model, mode, binding})
	defer func() {
		s.executor.Reset()
		result.Completed = time.Now().UTC()
		result.Stale = a.CheckCurrent() != nil
		if err != nil && result.Status == "running" {
			result.Status = "error"
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				result.Status = "cancelled"
			}
		}
		if saveErr := saveMarkdownResult(a, result); saveErr != nil {
			fmt.Fprintln(s.Err, "Test history unavailable:", saveErr)
		}
	}()
	failed := false
	for _, c := range cases {
		if e := ctx.Err(); e != nil {
			return result, e
		}
		s.executor.Reset()
		mocks := a.Mocks[c.ID]
		if mocks == nil {
			mocks = []MarkdownMock{}
		}
		answer, e := s.executor.Answer(ctx, a, c.Input, mocks)
		row := MarkdownCaseResult{ID: c.ID, Verdict: "error", Model: answer.Model, Answer: answer.Text}
		if e != nil {
			row.Reason = "execution incomplete"
			result.Cases = append(result.Cases, row)
			if emit != nil {
				emit(row)
			}
			return result, e
		}
		row.Checks = agentruntime.EvaluateAssertions(c, agentruntime.EvaluationEvidence{Answer: answer.Text, AnswerAvailable: true, ToolSupported: len(mocks) > 0, ToolEvidenceComplete: true, ToolCalls: answer.Calls})
		row.Verdict = "pass"
		for _, check := range row.Checks {
			if check.Verdict != agentruntime.EvaluationPass {
				row.Verdict = string(check.Verdict)
				failed = true
			}
		}
		if answer.MockError != "" {
			row.Verdict = "fail"
			row.Reason = answer.MockError
			failed = true
		}
		result.Cases = append(result.Cases, row)
		if emit != nil {
			emit(row)
		}
		if answer.MockError != "" {
			result.Status = "failed"
			return result, ErrMarkdownTestsFailed
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	result.Status = "passed"
	if selected != "" {
		result.Status = "partial"
	}
	if failed {
		result.Status = "failed"
		err = ErrMarkdownTestsFailed
	}
	return result, err
}
func (s *MarkdownSession) Test(ctx context.Context, a *MarkdownAgent, selected string, content bool) error {
	out := &markdownWriter{Writer: s.Out}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if _, err := fmt.Fprintf(out, "Local preview · %s\nAgent %s\nTests %s\n", a.Name, a.Revision, a.TestsRevision); err != nil {
		return err
	}
	result, err := s.Evaluate(ctx, a, selected, func(row MarkdownCaseResult) {
		fmt.Fprintf(out, "%s %s · %s\n", strings.ToUpper(row.Verdict), row.ID, row.Reason)
		for _, c := range row.Checks {
			fmt.Fprintf(out, "  %s %s · %s\n", strings.ToUpper(string(c.Verdict)), c.ID, c.Reason)
		}
		if content {
			fmt.Fprintf(out, "Answer: %s\n", safeTerminal(row.Answer))
		}
		if out.err != nil {
			cancel()
		}
	})
	if result.ID != "" {
		_, outErr := fmt.Fprintf(out, "%s · %d cases · %s · stale=%t · model=%s\n", result.Status, len(result.Cases), result.Mode, result.Stale, mdModelLabel(result.Model))
		err = errors.Join(err, outErr)
	}
	return errors.Join(err, out.err)
}

type markdownWriter struct {
	io.Writer
	err error
}

func (w *markdownWriter) Write(b []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, e := w.Writer.Write(b)
	if e == nil && n != len(b) {
		e = io.ErrShortWrite
	}
	w.err = e
	return n, e
}
func mdModelLabel(model string) string {
	if model == "" || model == "auto" {
		return "default/auto (unpinned)"
	}
	return model
}
func markdownHistoryPath(a *MarkdownAgent) (string, error) {
	dir, err := config.StateDir()
	canonical, e := filepath.EvalSymlinks(a.Path)
	if e != nil {
		return "", e
	}
	key := strings.TrimPrefix(mdHash([]byte(canonical)), "sha256:")
	return filepath.Join(dir, "md-tests", key, "history.json"), err
}
func LoadMarkdownHistory(a *MarkdownAgent) ([]MarkdownTestResult, error) {
	path, err := markdownHistoryPath(a)
	if err != nil {
		return nil, err
	}
	var rows []MarkdownTestResult
	err = mdStateRead(path, &rows)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(rows) > 10 {
		return nil, errors.New("test history exceeds retention limit")
	}
	for _, r := range rows {
		if r.SchemaVersion != 1 || r.ID == "" || r.RunKey == "" {
			return nil, errors.New("invalid test history")
		}
	}
	return rows, nil
}
func saveMarkdownResult(a *MarkdownAgent, r MarkdownTestResult) error {
	path, err := markdownHistoryPath(a)
	if err != nil {
		return err
	}
	return mdStateUpdate(path, func() ([]byte, error) {
		rows, err := LoadMarkdownHistory(a)
		if err != nil {
			return nil, err
		}
		rows = append(rows, r)
		if len(rows) > 10 {
			rows = rows[len(rows)-10:]
		}
		return json.Marshal(rows)
	})
}
func MarkdownStatus(a *MarkdownAgent, r *MarkdownTestResult) string {
	if len(a.Examples) == 0 {
		return "NO TESTS"
	}
	if r == nil {
		return "UNTESTED"
	}
	_, binding, err := readCurrentToolGrant(a.Path)
	if err != nil {
		return "BINDING UNAVAILABLE"
	}
	if r.BindingDigest != binding {
		return "BINDING CHANGED · last " + r.Status
	}
	if r.PairDigest != a.PairRevision || r.Model != a.Model || r.Stale {
		return "CHANGED · last " + r.Status
	}
	if r.Adapter != markdownAdapterVersion {
		return "RUNNER CHANGED · last " + r.Status
	}
	return strings.ToUpper(r.Status) + " · " + r.Mode + " · " + r.Completed.Local().Format("Jan 02 15:04")
}
func ListMarkdownAgents(out io.Writer, root string) ([]string, error) {
	orphans, err := MarkdownOrphanTests(root)
	if err != nil {
		return nil, err
	}
	for _, path := range orphans {
		fmt.Fprintln(out, "ORPHAN TEST:", safeTerminal(path))
	}
	paths, err := DiscoverMarkdownAgents(root)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		_, err = fmt.Fprintln(out, "No agents. Add .github/agents/NAME.agent.md and .github/agent-tests/NAME.agent.test.md (or use kmx create NAME --type md).")
		return paths, err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "#\tAGENT\tMODEL\tTESTS\tSTATUS\tFILE")
	for i, path := range paths {
		a, e := ReadMarkdownAgent(path)
		if e != nil {
			fmt.Fprintf(w, "%d\tINVALID\t—\t—\t%s\t%s\n", i+1, safeTerminal(e.Error()), safeTerminal(path))
			continue
		}
		history, e := LoadMarkdownHistory(a)
		status := "UNTESTED"
		var last *MarkdownTestResult
		if len(history) > 0 {
			last = &history[len(history)-1]
		}
		status = MarkdownStatus(a, last)
		if e != nil {
			status = "HISTORY UNAVAILABLE"
		} else if last != nil {
			status = "HISTORICAL " + status
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%d\t%s\t%s\n", i+1, a.Name, mdModelLabel(a.Model), len(a.Examples), status, safeTerminal(path))
	}
	return paths, w.Flush()
}
func ShowMarkdownAgent(out io.Writer, a *MarkdownAgent) error {
	_, err := fmt.Fprintf(out, "%s · %s\nDefinition: %s\nTests: %s\nAgent %s\nTests %s\nPair %s\n\n%s\n", a.Name, mdModelLabel(a.Model), safeTerminal(a.Path), safeTerminal(a.TestsPath), a.Revision, a.TestsRevision, a.PairRevision, safeTerminal(a.Instructions))
	if err != nil {
		return err
	}
	for _, c := range a.Examples {
		fmt.Fprintf(out, "\n%s:%d · %s\n  %s\n", safeTerminal(a.TestsPath), a.Lines[c.ID], c.ID, safeTerminal(c.Input))
		for _, check := range agentruntime.EvaluationCaseAssertions(c) {
			fmt.Fprintf(out, "  %s · %s %q %q\n", check.ID, check.Type, check.Value, check.Pattern)
		}
	}
	return nil
}
func (s *MarkdownSession) Chat(ctx context.Context, a *MarkdownAgent, in io.Reader) error {
	if err := s.StartAgent(ctx, a); err != nil {
		return err
	}
	defer s.Reset()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), markdownAgentLimit)
	fmt.Fprintln(s.Out, "/show /test [id] /reload /reset /quit")
	if label := s.conversationLabel(); label != "" {
		fmt.Fprintln(s.Out, "Session:", label, "· reconnect retains model context; previous transcript is not displayed")
	}
	for {
		fmt.Fprint(s.Out, "\nyou> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		input := strings.TrimSpace(scanner.Text())
		switch {
		case input == "/quit" || input == "/back":
			return nil
		case input == "/show":
			_ = ShowMarkdownAgent(s.Out, a)
		case input == "/reload":
			if _, ok := s.executor.(*attachedAgentExecutor); ok && s.ownedManaged == "" {
				fmt.Fprintln(s.Err, "Attached to a frozen running version. Use kmx stop then kmx run FILE to update it.")
				continue
			}
			next, err := LoadMarkdownAgent(a.Path, a.TestsPath)
			if err != nil {
				fmt.Fprintln(s.Err, err)
				continue
			}
			a = next
			s.Reset()
			fmt.Fprintln(s.Out, "Reloaded", a.PairRevision)
		case input == "/reset":
			if err := s.ResetConversation(ctx); err != nil {
				fmt.Fprintln(s.Err, err)
			} else {
				fmt.Fprintln(s.Out, "New conversation; agent version and generation unchanged.")
			}
		case input == "/test" || strings.HasPrefix(input, "/test "):
			next, err := LoadMarkdownAgent(a.Path, a.TestsPath)
			if err != nil {
				fmt.Fprintln(s.Err, err)
				continue
			}
			tester := NewMarkdownSession(s.Out, s.Err)
			tester.Provider, tester.Model = s.Provider, s.Model
			err = tester.Test(ctx, next, strings.TrimSpace(strings.TrimPrefix(input, "/test")), false)
			tester.Close()
			if err != nil {
				fmt.Fprintln(s.Err, err)
			}
		case input == "":
			continue
		case strings.HasPrefix(input, "/"):
			fmt.Fprintln(s.Err, "Unknown command")
		default:
			if err := s.Run(ctx, a, input); err != nil {
				fmt.Fprintln(s.Err, err)
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
