package localpreview

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// DefaultExampleTimeout bounds one example: its model send plus any judging.
const DefaultExampleTimeout = 2 * time.Minute

// JudgeFactory builds the shared llmJudge evaluator for one route. The kmx app
// supplies its native judge (app.LocalPreviewJudge); baseURL and keyEnv are the
// non-secret route selection and transport, when non-nil, replaces the network.
type JudgeFactory func(baseURL, keyEnv string, transport http.RoundTripper, cases []agentruntime.EvaluationCase) (agentruntime.EvaluationJudge, error)

// Turn is one tool-free agent turn handed to an Executor.
type Turn struct {
	// Judge samples always use the tool-free chat harness, even for a tool agent.
	Judge             bool
	AgentID, Revision string
	// ExampleID is empty for ad-hoc input.
	ExampleID           string
	Instructions, Input string
	// Model is the exact selected model; Call is its only route. Call sends at
	// most once per invocation and returns fixed-text errors.
	Model           string
	Call            controller.ModelFunc
	Tool            controller.ToolFunc
	ToolDefinitions []api.ToolDefinition
	ToolSources     *localtools.SourceConfig
	ToolSettings    []localtools.ToolSetting
}

// Execution is an attributable terminal result of one Turn.
type Execution struct {
	Answer               string
	ToolCalls            int
	ToolCallCounts       map[string]int
	ToolEvidenceComplete bool
	// SessionUID identifies the journaled Session; empty for an Executor that
	// does not journal.
	SessionUID string
	Journaled  bool
}

// Executor is the seam between the preview loop and execution. The default is
// the embedded Sessions executor with a SQLite journal. An Executor must not
// retry a send, execute tools, fall back to another model, or report success
// for an incomplete or canceled turn.
type Executor interface {
	Execute(ctx context.Context, turn Turn) (Execution, error)
	Close() error
}

// Options configures one preview. JournalDir must be an absolute owner-only
// directory (normally config.LocalPreviewJournalDir()).
type Options struct {
	// resolveRoute is private to the runner adapter's validated modelaccess routes.
	// Standalone Run/Test retain the shared endpoint validation and canonicalization.
	resolveRoute        func(string, *Endpoint, http.RoundTripper, string) (route, error)
	allowToolAssertions bool
	JournalDir          string
	// Endpoint is the agent model's OpenAI-compatible route. It must be nil
	// when Model.Preferred is fake and is required otherwise.
	Endpoint *Endpoint
	// JudgeEndpoint deliberately overrides the judge route. By default llmJudge
	// uses the agent's route, and the fake model uses the fake judge.
	JudgeEndpoint *Endpoint
	// Judge is required when any example uses llmJudge.
	Judge JudgeFactory
	// ExampleTimeout defaults to DefaultExampleTimeout.
	ExampleTimeout time.Duration
	// Transport replaces the network transport for compatible routes (tests,
	// proxies). The fake route always uses its in-memory transport.
	Transport http.RoundTripper
	// Executor replaces the default journaled embedded Sessions executor.
	Executor Executor
}

// ExampleResult is one example's outcome. Answer is retained content, not a
// safe log projection; Reason and Refusal text never contain content.
type ExampleResult struct {
	ID         string
	Verdict    agentruntime.EvaluationVerdict
	Execution  string // completed, refused, failed, canceled or not-run
	Reason     string
	Refusal    *Refusal
	SessionUID string
	Answer     string
	Assertions []agentruntime.EvaluationAssertionResult
}

// Result is a local preview result: never deployment qualification.
type Result struct {
	Label      string
	RunID      string
	Journaled  bool
	AgentID    string
	Revision   string
	Model      string
	ModelRoute string
	JudgeRoute string
	Verdict    agentruntime.EvaluationVerdict
	Examples   []ExampleResult
	ModelSends int64
}

// prepared holds every validated input before any effect.
type prepared struct {
	agent   *agentfile.Agent
	route   route
	judge   agentruntime.EvaluationJudge
	judgeRt string
	cases   []agentruntime.EvaluationCase
	timeout time.Duration
	options Options
	// credentials are the nonempty agent and judge bearer values resolved
	// once by prepare; journal-bound content is checked against them.
	credentials []string
}

func prepare(agent *agentfile.Agent, options Options, examples bool) (*prepared, error) {
	if agent == nil {
		return nil, refuse(RefusedRequest, "no agent was supplied", "load an agent file")
	}
	if options.Executor == nil && options.JournalDir == "" {
		return nil, refuse(RefusedJournal, "no journal directory was selected", "use the kmx local preview journal directory")
	}
	if len(agent.Tools) > 0 {
		return nil, refuse(RefusedTools, "tool declarations require a host-bound file runner", "use file Run/Test with explicit tool bindings")
	}
	p := &prepared{agent: agent, options: options, timeout: options.ExampleTimeout}
	if p.timeout <= 0 {
		p.timeout = DefaultExampleTimeout
	}
	hasJudge := false
	if examples {
		if len(agent.Examples) == 0 {
			return nil, refuse(RefusedRequest, "the agent has no examples to test", "add examples to the Examples section")
		}
		for _, example := range agent.Examples {
			for _, assertion := range example.Assertions {
				switch assertion.Type {
				case "contains", "notContains", "regex":
				case "llmJudge":
					hasJudge = true
				case "toolCalled", "toolNotCalled":
					if options.allowToolAssertions {
						break
					}
					return nil, refuse(RefusedTools, "an example asserts tool use", "local preview is tool-free; remove tool assertions")
				default:
					return nil, refuse(RefusedRequest, "an example uses an unsupported assertion type", "use contains, notContains, regex or llmJudge")
				}
			}
		}
	}
	resolver := options.resolveRoute
	if resolver == nil {
		resolver = resolveRoute
	}
	var err error
	if p.route, err = resolver(agent.Model, options.Endpoint, options.Transport, "Model.Preferred"); err != nil {
		return nil, err
	}
	p.addCredential(p.route.key)
	if !hasJudge && options.JudgeEndpoint != nil {
		return nil, refuse(RefusedJudge, "a judge endpoint was configured but no example uses llmJudge", "remove the judge endpoint")
	}
	if hasJudge && options.Judge == nil {
		return nil, refuse(RefusedJudge, "llmJudge examples need the shared judge", "run the preview through kmx")
	}
	judgeRoute := p.route
	if hasJudge && options.JudgeEndpoint != nil {
		// An explicit override is a compatible endpoint; the judge model is
		// each assertion's authored judgeModel, not Model.Preferred.
		if judgeRoute, err = resolver("", options.JudgeEndpoint, options.Transport, "the judge"); err != nil {
			return nil, err
		}
	}
	// A standalone endpoint override selects transport, not another model.
	// Adapter routes can supply an explicit judge model; otherwise use the run model.
	model := judgeRoute.model
	if model == "" {
		model = p.route.model
	}
	execution := *agent
	execution.Examples = agentfile.BindJudgeModels(agent.Examples, model)
	p.agent = &execution
	if examples {
		for _, example := range execution.Examples {
			c := agentruntime.EvaluationCase{ID: example.ID, Input: example.Input, Assertions: example.Assertions}
			if agentruntime.ValidateEvaluationAssertions(c.Assertions) != nil {
				return nil, refuse(RefusedRequest, "an example has invalid assertions", "fix the Examples section")
			}
			p.cases = append(p.cases, c)
		}
	}
	if !hasJudge {
		return p, nil
	}
	p.judgeRt = judgeRoute.kind()
	p.addCredential(judgeRoute.key)
	transport := judgeRoute.transport
	if p.judge, err = options.Judge(judgeRoute.baseURL, judgeRoute.keyEnv, transport, p.cases); err != nil || p.judge == nil {
		return nil, refuse(RefusedJudge, "the llmJudge configuration was refused", "check the judge endpoint and key environment variable")
	}
	return p, nil
}

func (p *prepared) addCredential(key string) {
	if key != "" && !slices.Contains(p.credentials, key) {
		p.credentials = append(p.credentials, key)
	}
}

// refuseJournalCredentials refuses when journal content or returned assertion
// metadata contains a resolved agent or judge credential (exact value, any length).
// Sessions records the identities, instructions and input when a turn starts,
// before any reply check runs, so this must precede the journal's creation.
func (p *prepared) refuseJournalCredentials(examples []agentfile.Example) error {
	fields := []string{p.agent.Identity.ID, p.agent.Revision, p.route.model, p.agent.Instructions}
	for _, example := range examples {
		if localtools.CheckJSON(example.Mocks, func(values ...string) error { fields = append(fields, values...); return nil }) != nil {
			return agentpreview.ErrUnsafeToolContent
		}
		fields = append(fields, example.ID, example.Input)
		for _, assertion := range example.Assertions {
			fields = append(fields, assertion.ID, assertion.Type, assertion.JudgeModel, assertion.JudgeVersion)
		}
	}
	for _, key := range p.credentials {
		for _, field := range fields {
			if strings.Contains(field, key) {
				return refuse(RefusedCredentialContent, "the instructions, an example, assertion metadata, the input or the agent identity contains the endpoint or judge credential, so nothing was sent or journaled",
					"remove the credential value; agent files and inputs must never contain secrets")
			}
		}
	}
	return nil
}

// Run sends one ad-hoc input to the agent. It evaluates no assertions.
func Run(ctx context.Context, agent *agentfile.Agent, input string, options Options) (Result, error) {
	p, err := prepare(agent, options, false)
	if err != nil {
		return Result{}, err
	}
	// Apply the authored-example bound and the same input validator (UTF-8 and
	// credential shapes); the placeholder expectation only satisfies its shape.
	if len(input) > agentfile.MaxInputBytes || agentruntime.ValidateEvaluationCase(agentruntime.EvaluationCase{ID: "input", Input: input, ExpectContains: []string{"input"}}) != nil {
		return Result{}, refuse(RefusedRequest, "the input is empty, too large or credential-shaped", "use a nonblank input of at most 16 KiB")
	}
	return p.run(ctx, []agentfile.Example{{Input: input}}, false)
}

// Test runs every example in order, one fresh Session each, and evaluates its
// assertions. Any fail, unknown, refusal, cancellation or incomplete turn
// makes the result non-pass.
func Test(ctx context.Context, agent *agentfile.Agent, options Options) (Result, error) {
	p, err := prepare(agent, options, true)
	if err != nil {
		return Result{}, err
	}
	return p.run(ctx, p.agent.Examples, true)
}

func (p *prepared) run(ctx context.Context, examples []agentfile.Example, evaluate bool) (result Result, err error) {
	if err := p.refuseJournalCredentials(examples); err != nil {
		return Result{}, err
	}
	result = Result{Label: Label, AgentID: p.agent.Identity.ID, Revision: p.agent.Revision, Model: p.route.model,
		ModelRoute: p.route.kind(), JudgeRoute: p.judgeRt, Verdict: agentruntime.EvaluationUnknown}
	client := newModelClient(p.route)
	defer func() { result.ModelSends = client.sends.Load() }()
	executor := p.options.Executor
	if executor == nil {
		var journaled *journalExecutor
		journaled, result.RunID, err = openJournalExecutor(p.options.JournalDir, p.route.model)
		if err != nil {
			return result, err
		}
		executor = journaled
	}
	defer func() {
		if closeErr := executor.Close(); closeErr != nil {
			result.Verdict = agentruntime.EvaluationUnknown
			result.Journaled = false
			result.RunID = ""
			err = errors.Join(err, closeErr)
		}
	}()

	verdicts := make([]agentruntime.EvaluationVerdict, 0, len(examples))
	for i, example := range examples {
		row := ExampleResult{ID: example.ID, Verdict: agentruntime.EvaluationUnknown, Execution: "not-run", Reason: "not run"}
		if ctx.Err() != nil {
			result.Examples = append(result.Examples, row)
			verdicts = append(verdicts, row.Verdict)
			continue
		}
		exampleCtx, cancel := context.WithTimeout(ctx, p.timeout)
		execution, execErr := executor.Execute(exampleCtx, Turn{
			AgentID: p.agent.Identity.ID, Revision: p.agent.Revision, ExampleID: example.ID,
			Instructions: p.agent.Instructions, Input: example.Input, Model: p.route.model, Call: client.Model,
		})
		result.Journaled = execution.Journaled
		row.SessionUID = execution.SessionUID
		switch {
		case execErr == nil:
			row.Execution, row.Reason, row.Answer = "completed", "completed", execution.Answer
			if evaluate {
				evidence := agentruntime.EvaluationEvidence{AnswerAvailable: true, Answer: execution.Answer}
				if p.judge != nil {
					row.Assertions = p.judge(exampleCtx, p.cases[i], evidence)
				} else {
					row.Assertions = agentruntime.EvaluateAssertions(p.cases[i], evidence)
				}
				row.Verdict = agentruntime.EvaluationAssertionsVerdict(row.Assertions)
				row.Reason = string(row.Verdict)
			} else {
				row.Reason = "answered; no assertions evaluated"
			}
		case interrupted(execErr) || exampleCtx.Err() != nil:
			row.Execution, row.Reason = "canceled", "canceled or timed out before a complete answer was journaled"
		default:
			var refusal *Refusal
			if errors.As(execErr, &refusal) {
				row.Execution, row.Reason, row.Refusal = "refused", refusal.Error(), refusal
			} else {
				row.Execution, row.Reason = "failed", "execution did not complete; the journal keeps the incomplete record"
			}
		}
		cancel()
		result.Examples = append(result.Examples, row)
		verdicts = append(verdicts, row.Verdict)
	}
	if evaluate {
		result.Verdict = combine(verdicts)
	}
	if err := ctx.Err(); err != nil {
		result.Verdict = agentruntime.EvaluationUnknown
		return result, err
	}
	return result, nil
}

// interrupted reports cancellation or a deadline, whether observed locally or
// returned by the embedded Sessions server as a gRPC status.
func interrupted(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	code := status.Code(err)
	return code == codes.Canceled || code == codes.DeadlineExceeded
}

func combine(verdicts []agentruntime.EvaluationVerdict) agentruntime.EvaluationVerdict {
	if len(verdicts) == 0 {
		return agentruntime.EvaluationUnknown
	}
	out := agentruntime.EvaluationPass
	for _, v := range verdicts {
		switch {
		case v == agentruntime.EvaluationFail:
			out = agentruntime.EvaluationFail
		case v != agentruntime.EvaluationPass && out == agentruntime.EvaluationPass:
			out = agentruntime.EvaluationUnknown
		}
	}
	return out
}

// journalExecutor runs each Turn as one fresh Session on an embedded Sessions
// host whose journal is this run's SQLite file.
type journalExecutor struct {
	mu       sync.Mutex // serializes turns
	host     *host
	sessions map[string]bool
	// Sessions' Placer binds one model function per host, so each Turn's route
	// is dispatched through the current slot. The controller calls it on a
	// server goroutine; a canceled turn's late error stays in its own slot.
	slotMu  sync.Mutex
	current *turnSlot
	// lock marks the run as executing until Close.
	lock         *runLock
	tools        bool
	definitions  []api.ToolDefinition
	toolSettings []localtools.ToolSetting
	path         string
	modelName    string
	// The upstream chat harness pins its model. Separate in-process hosts share
	// this run's journal while preserving each turn's exact harness/model pair.
	chatHosts map[string]*host
}

type turnSlot struct {
	call controller.ModelFunc
	tool controller.ToolFunc
	mu   sync.Mutex
	err  error
}

func openJournalExecutor(dir, model string) (*journalExecutor, string, error) {
	runID, path, lock, err := newJournal(dir, time.Now())
	if err != nil {
		return nil, "", err
	}
	e := &journalExecutor{sessions: map[string]bool{}, lock: lock, path: path, modelName: model, chatHosts: map[string]*host{}}
	e.host, err = openHost(path, model, e.model)
	if err != nil {
		lock.discardRun()
		return nil, runID, err
	}
	return e, runID, nil
}

func openToolJournalExecutor(dir, model string, tool controller.ToolFunc, settings []localtools.ToolSetting, definitions ...api.ToolDefinition) (*journalExecutor, string, error) {
	runID, path, lock, err := newJournal(dir, time.Now())
	if err != nil {
		return nil, "", err
	}
	e := &journalExecutor{sessions: map[string]bool{}, lock: lock, tools: true, definitions: definitions, toolSettings: append([]localtools.ToolSetting(nil), settings...), path: path, modelName: model, chatHosts: map[string]*host{}}
	guarded := func(ctx context.Context, scope controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
		e.slotMu.Lock()
		slot := e.current
		e.slotMu.Unlock()
		result, err := tool(ctx, scope, call)
		if err != nil {
			e.rememberError(slot, err)
		}
		return result, err
	}
	e.host, err = openPreviewHost(path, localtools.HarnessName, localtools.Harness{Model: model, Definitions: definitions, ToolSettings: e.toolSettings}, e.model, guarded)
	if err != nil {
		lock.discardRun()
		return nil, runID, err
	}
	return e, runID, nil
}

func (e *journalExecutor) model(ctx context.Context, request api.ModelRequest) (api.ModelResponse, error) {
	e.slotMu.Lock()
	slot := e.current
	e.slotMu.Unlock()
	if slot == nil {
		return api.ModelResponse{}, refuse(RefusedRequest, "no model route is active", "start a preview turn")
	}
	response, err := slot.call(ctx, request)
	if err != nil {
		e.rememberError(slot, err)
	}
	return response, err
}

func (e *journalExecutor) rememberError(slot *turnSlot, err error) {
	if slot != nil {
		slot.mu.Lock()
		slot.err = err
		slot.mu.Unlock()
	}
}

func (e *journalExecutor) setSlot(slot *turnSlot) {
	e.slotMu.Lock()
	e.current = slot
	e.slotMu.Unlock()
}

func (e *journalExecutor) Execute(ctx context.Context, turn Turn) (execution Execution, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	slot := &turnSlot{call: turn.Call, tool: turn.Tool}
	e.setSlot(slot)
	defer e.setSlot(nil)
	request := agentsessions.PreviewCaseRequest{
		Revision: turn.Revision, Agent: turn.AgentID, Example: turn.ExampleID,
		Instructions: turn.Instructions, Model: turn.Model, Input: turn.Input, UsedSessionUIDs: e.sessions,
	}
	h := e.host
	toolTurn := e.tools && !turn.Judge
	if !toolTurn && (e.tools || turn.Model != e.modelName) {
		h = e.chatHosts[turn.Model]
		if h == nil {
			var err error
			h, err = openHost(e.path, turn.Model, e.model)
			if err != nil {
				return Execution{}, err
			}
			e.chatHosts[turn.Model] = h
		}
	}
	run := h.client.RunPreviewCase
	if turn.Tool != nil && !turn.Judge {
		guarded := func(ctx context.Context, scope controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
			result, err := slot.tool(ctx, scope, call)
			if err != nil {
				e.rememberError(slot, err)
			}
			return result, err
		}
		h, err = openPreviewHost(e.path, localtools.HarnessName, localtools.Harness{Model: turn.Model, Definitions: turn.ToolDefinitions, Sources: turn.ToolSources, ToolSettings: turn.ToolSettings}, e.model, guarded)
		if err != nil {
			return Execution{}, err
		}
		defer func() { err = errors.Join(err, h.close()) }()
		request.ToolDefinitions, request.ToolSources, request.ToolSettings = turn.ToolDefinitions, turn.ToolSources, turn.ToolSettings
		run = h.client.RunToolPreviewCase
	} else if toolTurn {
		request.ToolDefinitions = e.definitions
		request.ToolSettings = e.toolSettings
		run = h.client.RunToolPreviewCase
	}
	result, err := run(ctx, request)
	execution = Execution{SessionUID: result.SessionUID, Journaled: true}
	if result.SessionUID != "" {
		e.sessions[result.SessionUID] = true
	}
	if err != nil {
		// The model route's own fixed-text error explains a refused reply
		// better than the strict client's shape diagnostic.
		slot.mu.Lock()
		callErr := slot.err
		slot.mu.Unlock()
		if callErr != nil {
			return execution, callErr
		}
		if ctx.Err() != nil {
			return execution, ctx.Err()
		}
		return execution, err
	}
	execution.Answer = result.Output
	execution.ToolCallCounts, execution.ToolEvidenceComplete = result.ToolCalls, result.ToolEvidenceComplete
	for _, count := range result.ToolCalls {
		execution.ToolCalls += count
	}
	return execution, nil
}

// Close closes the journal, then releases the run lock so pruning and Clear
// may remove the finished run.
func (e *journalExecutor) Close() error {
	err := e.host.close()
	for _, h := range e.chatHosts {
		err = errors.Join(err, h.close())
	}
	return errors.Join(err, e.lock.release())
}
