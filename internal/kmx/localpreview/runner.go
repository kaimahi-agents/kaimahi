package localpreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agent-primitives/sessions/api"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// RunnerOptions supplies invocation-local facilities. JournalDir is resolved
// lazily, after validation; constructing a runner performs no IO or authentication.
// Judge receives validated routes and provider-owned transports, without
// canonicalizing selected endpoints. It preserves routes and assertion rows.
type RunnerOptions struct {
	JournalDir func() (string, error)
	Judge      JudgeFactory
	// JudgeReply validates the shared native scorer's reply before Sessions sees it.
	JudgeReply func([]byte, string, string) error
	// Access establishes provider-owned credentials before journal creation.
	Access         func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error)
	ExampleTimeout time.Duration
	// Tools resolves a trusted host from a frozen binding. Nil refuses tools;
	// it never selects an implicit credential or a production fixture provider.
	Tools func(context.Context, localtools.Binding) (*localtools.Host, error)
	// ResolveTools freezes a trusted binding for exactly the declared names.
	// It performs no discovery; the authored file supplies no execution authority.
	ResolveTools func(context.Context, []string) (*localtools.Host, error)
	// ResolveAvailableTools has explicit host-owned missing-name accounting; nil means all absent.
	ResolveAvailableTools func(context.Context, []string) (*localtools.Host, error)
}

type runner struct {
	options RunnerOptions
	open    func(string, string) (Executor, string, error)
}

// NewRunner adapts selected examples and optional input to one journal/executor.
// It neither reloads a file nor infers a model, endpoint or credential facility.
func NewRunner(options RunnerOptions) agentpreview.Runner {
	return &runner{options: options, open: func(dir, model string) (Executor, string, error) { return openJournalExecutor(dir, model) }}
}

var errRunnerExecution = errors.New("local preview did not produce an attributable completion")

func (r *runner) Run(ctx context.Context, request agentpreview.Request) (out agentpreview.Outcome, err error) {
	// One outer budget bounds the whole operation; each turn also has its own
	// deadline, shared with its assertion evaluation and native judge samples.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out.State = agentpreview.Failed
	examples := cloneRunnerExamples(request.Examples)
	for _, ex := range examples {
		c := agentruntime.EvaluationCase{ID: ex.ID, Input: ex.Input, Assertions: ex.Assertions}
		out.Examples = append(out.Examples, agentpreview.ExampleOutcome{ID: ex.ID, Completion: agentpreview.Completion{State: agentpreview.NotRun}, Assertions: agentruntime.EvaluateAssertions(c, agentruntime.EvaluationEvidence{})})
	}
	if request.Input != nil {
		out.AdHoc = &agentpreview.Completion{State: agentpreview.NotRun}
	}
	abort := func(cause error) (agentpreview.Outcome, error) {
		out.State = agentpreview.Failed
		if errors.Is(cause, context.Canceled) {
			out.State = agentpreview.Cancelled
		}
		return out, cause
	}
	if ctx.Err() != nil {
		return abort(ctx.Err())
	}
	if request.Agent == nil || len(examples) == 0 && request.Input == nil || request.Route.Model != request.Agent.Model {
		return abort(agentpreview.ErrModelUnavailable)
	}
	agent := *request.Agent
	if request.Route.Provider == "fake" && agent.NativeTaskInstructions != "" {
		agent.Instructions = agent.NativeTaskInstructions
	}
	agent.Examples = examples
	judgeSelection := request.Route
	if request.JudgeRoute != nil {
		judgeSelection = *request.JudgeRoute
	}
	var access modelaccess.PreviewAccess
	var toolHost *localtools.Host
	checkContent := func(fields ...string) error {
		if len(request.Agent.Tools) > 0 {
			for _, field := range fields {
				if secretshapes.Match(field) != nil {
					return agentpreview.ErrUnsafeToolContent
				}
			}
		}
		if access != nil && access.CheckContent(fields...) != nil {
			return agentpreview.ErrCredentialContent
		}
		if toolHost != nil && toolHost.Check(fields...) != nil {
			return agentpreview.ErrUnsafeToolContent
		}
		return nil
	}
	validate := func(ctx context.Context) error {
		if access != nil {
			return access.Validate(ctx)
		}
		return ctx.Err()
	}
	request.ToolDefinitions = nil
	declared := slices.Clone(agent.Tools)
	for _, name := range declared {
		if !localtools.ValidToolName(name) {
			return abort(agentpreview.ErrToolsUnavailable)
		}
	}
	if len(declared) > 0 || request.ToolBindings != "" {
		var bindErr error
		toolHost, bindErr = r.resolveTools(ctx, request, neededLiveTools(declared, examples, request.Input != nil))
		if bindErr != nil {
			return abort(bindErr)
		}
		if checkToolContent(checkContent, request) != nil {
			return agentpreview.Outcome{State: agentpreview.Failed}, agentpreview.ErrUnsafeToolContent
		}
		agent.Tools = nil
	}

	var providerRoutes []modelaccess.Route
	for _, selection := range []modelaccess.Route{request.Route, judgeSelection} {
		if selection.Provider == "copilot" && !slices.Contains(providerRoutes, selection) {
			providerRoutes = append(providerRoutes, selection)
		}
	}
	if len(providerRoutes) > 0 {
		if r.options.Access == nil {
			return abort(agentpreview.ErrModelUnavailable)
		}
		var setupErr error
		access, setupErr = r.options.Access(ctx, providerRoutes)
		if access != nil {
			// Marshal failure leaves nil content, which the pre-journal check refuses.
			content, _ := json.Marshal(struct {
				Agent        agentfile.Agent
				Label        string
				Route, Judge modelaccess.Route
				Input        *string
				Definitions  any
			}{agent, request.FileLabel, request.Route, judgeSelection, request.Input, request.ToolDefinitions})
			if checkJSONContent(content, checkContent) != nil {
				return agentpreview.Outcome{State: agentpreview.Failed}, agentpreview.ErrCredentialContent
			}
		}
		if setupErr != nil {
			return abort(setupErr)
		}
		if access == nil {
			return abort(agentpreview.ErrModelUnavailable)
		}
	}
	routeFor := func(selection modelaccess.Route) (route, error) {
		if selection.Provider != "copilot" {
			return runnerRoute(selection)
		}
		if selection.Validate() != nil || access == nil {
			return route{}, agentpreview.ErrModelUnavailable
		}
		transport, err := access.Client(selection)
		if err != nil {
			return route{}, err
		}
		if transport == nil {
			return route{}, agentpreview.ErrModelUnavailable
		}
		return route{model: selection.Model, baseURL: selection.Endpoint, transport: transport, safeErrors: true}, nil
	}
	agentRoute, routeErr := routeFor(request.Route)
	if routeErr != nil {
		return abort(routeErr)
	}
	// Even an unused explicit route must be a supported, exact selection.
	judgeRoute, routeErr := routeFor(judgeSelection)
	if routeErr != nil {
		return abort(routeErr)
	}
	seenExamples := map[string]bool{}
	hasJudge := false
	for _, ex := range examples {
		if ex.ID == "" || seenExamples[ex.ID] {
			return abort(errRunnerExecution)
		}
		seenExamples[ex.ID] = true
		for _, a := range ex.Assertions {
			if a.Type == "llmJudge" {
				hasJudge = true
				if a.JudgeModel != judgeSelection.Model {
					return abort(agentpreview.ErrModelUnavailable)
				}
			}
		}
	}
	input := ""
	if request.Input != nil {
		input = *request.Input
		if len(input) > agentfile.MaxInputBytes || agentruntime.ValidateEvaluationCase(agentruntime.EvaluationCase{ID: "input", Input: input, ExpectContains: []string{"input"}}) != nil {
			return abort(errRunnerExecution)
		}
	}
	// A placeholder satisfies prepare's no-empty-directory check, without
	// resolving operator state until all content and route preflights succeed.
	options := Options{JournalDir: "pending", ExampleTimeout: r.options.ExampleTimeout, allowToolAssertions: len(declared) > 0}
	options.resolveRoute = func(_ string, _ *Endpoint, _ http.RoundTripper, role string) (route, error) {
		if role == "the judge" {
			return judgeRoute, nil
		}
		return agentRoute, nil
	}
	var executor Executor
	var judgeProgressErr error
	if hasJudge {
		if request.JudgeRoute != nil {
			options.JudgeEndpoint = &Endpoint{BaseURL: judgeRoute.baseURL}
		}
		if r.options.Judge != nil {
			options.Judge = func(base, key string, transport http.RoundTripper, cases []agentruntime.EvaluationCase) (agentruntime.EvaluationJudge, error) {
				guarded := &runnerJudgeTransport{next: transport, safeErrors: judgeRoute.safeErrors, request: request, failure: &judgeProgressErr, executor: &executor, check: checkContent, validate: validate, reply: r.options.JudgeReply}
				return r.options.Judge(base, key, guarded, cases)
			}
		}
	}
	// A zero-example ad-hoc Run prepares no evaluation cases or judge.
	p, prepareErr := prepare(&agent, options, len(examples) > 0)
	if prepareErr != nil {
		return abort(prepareErr)
	}
	journalContent := agentfile.CloneExamples(examples)
	if request.Input != nil {
		journalContent = append(journalContent, agentfile.Example{Input: input})
	}
	if preflightErr := p.refuseJournalCredentials(journalContent); preflightErr != nil {
		// Never return prepopulated identities if they contain a resolved credential.
		return agentpreview.Outcome{State: agentpreview.Failed}, preflightErr
	}
	if err := validate(ctx); err != nil {
		return abort(err)
	}
	if r.options.JournalDir == nil {
		return abort(agentpreview.ErrJournalFailed)
	}
	plans := make([]toolPlan, len(examples))
	for i, ex := range examples {
		plans[i], err = makeToolPlan(declared, ex, toolHost, checkContent, validate)
		if err != nil {
			return abort(err)
		}
	}
	adHocPlan, planErr := makeToolPlan(declared, agentfile.Example{}, toolHost, checkContent, validate)
	if request.Input != nil && planErr != nil {
		return abort(planErr)
	}
	dir, dirErr := r.options.JournalDir()
	if dirErr != nil {
		return abort(fmt.Errorf("%w: %w", agentpreview.ErrJournalFailed, dirErr))
	}
	var journal string
	var openErr error
	executor, journal, openErr = r.open(dir, p.route.model)
	if openErr != nil {
		return abort(fmt.Errorf("%w: %w", agentpreview.ErrJournalFailed, openErr))
	}
	defer func() {
		if closeErr := executor.Close(); closeErr != nil {
			out.State = agentpreview.Failed
			out.Journal = ""
			err = errors.Join(err, fmt.Errorf("%w: %w", agentpreview.ErrJournalFailed, closeErr))
		}
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
			out.State = agentpreview.Failed
			if errors.Is(ctx.Err(), context.Canceled) {
				out.State = agentpreview.Cancelled
			}
		}
		if err == nil && out.State == agentpreview.Complete && journal != "" {
			out.Journal = journal
		}
		if err == nil && out.Journal == "" {
			out.State = agentpreview.Failed
			err = agentpreview.ErrJournalFailed
		}
	}()
	client := newModelClient(p.route)
	if access != nil || len(declared) > 0 {
		client.check = checkContent
	}
	call := client.Model
	if len(declared) > 0 {
		call = func(ctx context.Context, req api.ModelRequest) (api.ModelResponse, error) {
			if err := validate(ctx); err != nil {
				return api.ModelResponse{}, err
			}
			return client.ToolModel(ctx, req)
		}
	}
	used := map[string]bool{}
	execute := func(ex agentfile.Example, index int) (completion agentpreview.Completion, assertions []agentruntime.EvaluationAssertionResult, cause error) {
		turnCtx, turnCancel := context.WithTimeout(ctx, p.timeout)
		defer turnCancel()
		if err := validate(turnCtx); err != nil {
			return completion, nil, err
		}
		label := ex.ID
		if index < 0 {
			label = "ad-hoc"
		}
		if reportErr := request.Report("Completion %s started: provider=%s endpoint=%s", label, request.Route.Provider, runnerDiagnosticEndpoint(request.Route)); reportErr != nil {
			return completion, nil, reportErr
		}
		started := time.Now()
		plan := adHocPlan
		if index >= 0 {
			plan = plans[index]
		}
		execution, execErr := executor.Execute(turnCtx, Turn{AgentID: agent.Identity.ID, Revision: agent.Revision, ExampleID: ex.ID, Instructions: agent.Instructions, Input: ex.Input, Model: p.route.model, Call: call, Tool: plan.execute, ToolDefinitions: plan.definitions, ToolSources: plan.sources})
		reason := "complete"
		if execErr != nil || turnCtx.Err() != nil {
			reason = "execution_failed"
		}
		if reportErr := request.Report("Completion %s finished: %s elapsed=%s", label, reason, time.Since(started).Round(time.Millisecond)); reportErr != nil {
			return completion, nil, errors.Join(execErr, turnCtx.Err(), reportErr)
		}
		if turnCtx.Err() != nil {
			return completion, nil, turnCtx.Err()
		}
		if execErr != nil {
			if interrupted(execErr) {
				return completion, nil, runnerContextError(execErr)
			}
			return completion, nil, execErr
		}
		// Match App's completion bound before judging or sending later turns;
		// the standalone library permits larger answers than the file API.
		if !execution.Journaled || execution.SessionUID == "" || used[execution.SessionUID] || len(execution.Answer) > 256*1024 || len(declared) > 0 && !execution.ToolEvidenceComplete {
			return completion, nil, errRunnerExecution
		}
		used[execution.SessionUID] = true
		if index >= 0 {
			evidence := agentruntime.EvaluationEvidence{Answer: execution.Answer, AnswerAvailable: true, ToolSupported: len(declared) > 0, ToolEvidenceComplete: execution.ToolEvidenceComplete, ToolCalls: execution.ToolCallCounts}
			if p.judge != nil {
				assertions = p.judge(turnCtx, p.cases[index], evidence)
			} else {
				assertions = agentruntime.EvaluateAssertions(p.cases[index], evidence)
			}
			if judgeProgressErr != nil {
				return completion, nil, judgeProgressErr
			}
			if turnCtx.Err() != nil {
				return completion, nil, turnCtx.Err()
			}
			// Refuse incomplete or misattributed judge output rather than losing the
			// prepopulated identities/digests on an abort.
			if len(assertions) != len(ex.Assertions) {
				return completion, nil, errRunnerExecution
			}
			for i, row := range assertions {
				a := ex.Assertions[i]
				if row.ID != a.ID || row.Type != a.Type || row.DefinitionDigest != agentruntime.EvaluationAssertionDigest(a) {
					return completion, nil, errRunnerExecution
				}
			}
		}
		completion = agentpreview.Completion{State: agentpreview.Complete, ID: execution.SessionUID, FileRevision: agent.Revision, Provider: request.Route.Provider, Model: request.Route.Model, Answer: execution.Answer, ToolCalls: execution.ToolCalls, Tools: plan.summaries(execution.ToolCallCounts)}
		return completion, assertions, nil
	}
	if request.Input != nil {
		c, _, cause := execute(agentfile.Example{Input: input}, -1)
		if cause != nil {
			return abort(cause)
		}
		out.AdHoc = &c
	}
	for i, ex := range examples {
		c, assertions, cause := execute(ex, i)
		if cause != nil {
			return abort(cause)
		}
		out.Examples[i].Completion = c
		out.Examples[i].Assertions = assertions
		if request.ExampleFinished != nil && ctx.Err() == nil {
			request.ExampleFinished(out.Examples[i])
		}
	}
	if ctx.Err() != nil {
		return abort(ctx.Err())
	}
	out.State = agentpreview.Complete
	return out, nil
}

func cloneRunnerExamples(examples []agentfile.Example) []agentfile.Example {
	return agentfile.CloneExamples(examples)

}

// runnerRoute is deliberately narrower than standalone library endpoints. These
// modelaccess routes are keyless and local; preserve the endpoint selected by the
// caller (including localhost and custom paths), not the suite canonicalizer's /v1.
func runnerRoute(selection modelaccess.Route) (route, error) {
	if selection.Validate() != nil || (selection.Provider == "fake") != (selection.Model == FakeModel) {
		return route{}, agentpreview.ErrModelUnavailable
	}
	if selection.Provider == "fake" {
		return resolveRoute(FakeModel, nil, nil, "Model.Preferred")
	}
	if selection.Provider != "compatible" && selection.Provider != "ollama" && selection.Provider != "foundry-local" || selection.Auth != "none" {
		return route{}, agentpreview.ErrModelUnavailable
	}
	u, err := url.Parse(selection.Endpoint)
	if err != nil || u.RawPath != "" || u.Opaque != "" || strings.HasSuffix(u.Host, ":") {
		return route{}, agentpreview.ErrModelUnavailable
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return route{}, agentpreview.ErrModelUnavailable
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return route{}, agentpreview.ErrModelUnavailable
		}
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 30 * time.Second, MaxResponseHeaderBytes: 16 * 1024, TLSHandshakeTimeout: 10 * time.Second}
	return route{model: selection.Model, baseURL: strings.TrimRight(selection.Endpoint, "/"), transport: transport}, nil
}

func runnerDiagnosticEndpoint(selection modelaccess.Route) string {
	if selection.Provider == "fake" {
		return "in-process"
	}
	u, _ := url.Parse(selection.Endpoint)
	endpoint := u.Scheme + "://" + u.Host
	switch u.Path {
	case "", "/", "/v1", "/v1/":
		return endpoint + u.Path
	default:
		return endpoint + "/[path withheld]"
	}
}

func runnerContextError(err error) error {
	// Sessions may translate context failures to gRPC statuses; restore errors.Is.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if status.Code(err) == codes.DeadlineExceeded {
		return errors.Join(context.DeadlineExceeded, err)
	}
	return errors.Join(context.Canceled, err)
}
