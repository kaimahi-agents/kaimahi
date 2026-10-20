package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// Both the dashboard and wizard use the same pane and worker lifecycle.
func (a *App) bindSuiteWorkspaceReviewWorkers(ctx context.Context, m *agentTUIModel, workers *sync.WaitGroup) {
	m.startCreate = func(_ agentTUIEnvironment, _, root string, previous *suiteWorkspaceReview) (<-chan agentTUICreateResult, context.CancelFunc) {
		workCtx, stop := context.WithCancel(ctx)
		result := make(chan agentTUICreateResult, 1)
		workers.Add(1)
		go func() {
			defer workers.Done()
			if previous != nil {
				result <- agentTUICreateResult{review: previous, err: previous.checkCurrent(workCtx)}
				return
			}
			review, err := a.reviewSuiteWorkspace(workCtx, root)
			result <- agentTUICreateResult{review: review, err: err}
		}()
		return result, stop
	}
	m.startCreateLift = func(request consoleWorkspaceLiftRequest) (<-chan agentTUICreateResult, context.CancelFunc) {
		workCtx, stop := context.WithCancel(ctx)
		result := make(chan agentTUICreateResult, 1)
		workers.Add(1)
		go func() {
			defer workers.Done()
			msg := a.consoleWorkspaceLift(workCtx, request)
			msg.report = func(observed ReleaseLiftResult) error {
				_, err := fmt.Fprintln(a.Err, suiteWorkspaceLiftOutcomeSummary(observed))
				return err
			}
			result <- msg
		}()
		return result, stop
	}
}

type suiteWorkspaceReviewModel struct {
	agentTUIModel
	pane    *agentTUICreatePane
	initial tea.Cmd
}

func (m suiteWorkspaceReviewModel) Init() tea.Cmd { return m.initial }
func (m suiteWorkspaceReviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.agentTUIModel.Update(msg)
	m.agentTUIModel = next.(agentTUIModel)
	if m.creation == nil {
		return m, tea.Sequence(cmd, tea.Quit)
	}
	return m, cmd
}

// runSuiteWorkspaceReview shares console's source/case, destination and two
// consent stages. It returns verified placement only, never sends an answer.
func (a *App) runSuiteWorkspaceReview(ctx context.Context, root, namespace string) (ReleaseLiftResult, error) {
	env := agentTUIEnvironment{Name: a.Cfg.KubeContext}
	server, err := a.consoleCreateTarget(ctx, env)
	if err != nil {
		return ReleaseLiftResult{}, err
	}
	raw, err := env.app(a).orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return ReleaseLiftResult{}, err
	}
	kube, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return ReleaseLiftResult{}, err
	}
	posture, err := guard.Classify(kube, env.Name)
	if err != nil {
		return ReleaseLiftResult{}, err
	}
	if server == "" || consoleContextServer(kube, env.Name) != server {
		return ReleaseLiftResult{}, fmt.Errorf("selected environment changed before review")
	}
	env.Server, env.Local = server, posture.Local
	model := newAgentTUIModel(AgentTUIOptions{Namespace: valueOr(namespace, OrkaNamespace)})
	model.columns[0].Env = env
	next, _ := model.createAgent()
	model = next.(agentTUIModel)
	model.creation.input.SetValue(root)
	workCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	a.bindSuiteWorkspaceReviewWorkers(workCtx, &model, &workers)
	pane := model.creation
	wrapper := suiteWorkspaceReviewModel{agentTUIModel: model, pane: pane, initial: model.startWorkspaceReview(root, nil)}
	result, err := tea.NewProgram(wrapper, tea.WithInput(a.Stdin), tea.WithOutput(a.Out), tea.WithContext(workCtx),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, ok := msg.(tea.InterruptMsg); ok {
				return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
			}
			return msg
		})).Run()
	cancel()
	workers.Wait()
	if err != nil {
		return pane.result, err
	}
	finished := result.(suiteWorkspaceReviewModel).pane
	if finished.err != nil {
		return finished.result, finished.err
	}
	if !finished.ready {
		return finished.result, context.Canceled
	}
	return finished.result, nil
}

func (a *App) consoleCreateTarget(ctx context.Context, env agentTUIEnvironment) (string, error) {
	raw, err := env.app(a).orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return "", err
	}
	kube, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return "", err
	}
	p, err := guard.Classify(kube, env.Name)
	if err != nil {
		return "", err
	}
	if p.Host == "" {
		return "", fmt.Errorf("selected environment has no API server")
	}
	return consoleContextServer(kube, env.Name), nil
}

// Consent binds the complete selected API URI, not just a hostname that may
// identify several kind clusters on different ports.
func consoleContextServer(kube *guard.Kubeconfig, name string) string {
	for _, context := range kube.Contexts {
		if context.Name != name {
			continue
		}
		for _, cluster := range kube.Clusters {
			if cluster.Name == context.Context.Cluster {
				return cluster.Cluster.Server
			}
		}
	}
	return ""
}

type consoleWorkspaceLiftRequest struct {
	env                                       agentTUIEnvironment
	server, namespace, provider, typedContext string
	review                                    *suiteWorkspaceReview
	prepared                                  *preparedQuickstartSuite
	reviewedDigest                            string
}

type agentTUICreateResult struct {
	review    *suiteWorkspaceReview
	prepared  *preparedQuickstartSuite
	result    ReleaseLiftResult
	verified  bool
	err       error
	pane      *agentTUICreatePane
	operation int
	report    func(ReleaseLiftResult) error
}

// Consent is scoped to the retained source, selected context/server, namespace
// and named native Provider. Console streams stay private during evaluation.
func (a *App) consoleWorkspaceLift(ctx context.Context, request consoleWorkspaceLiftRequest) agentTUICreateResult {
	fail := func(err error) agentTUICreateResult { return agentTUICreateResult{err: err} }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if request.review == nil {
		return fail(fmt.Errorf("source/case review required"))
	}
	if !request.env.Local && (request.env.Name == "" || request.typedContext != request.env.Name) {
		return fail(fmt.Errorf("type the selected context exactly before authorizing evaluation Tasks and resource placement"))
	}
	if err := scaffold.ValidateObjectName(request.provider); err != nil {
		return fail(err)
	}
	if err := scaffold.ValidateNamespace(request.namespace); err != nil {
		return fail(err)
	}
	if err := request.review.checkCurrent(ctx); err != nil {
		return fail(err)
	}
	worker := *request.env.app(a).withRunContext(ctx)
	cfg := *worker.Cfg
	cfg.Confirm = ""
	if !request.env.Local {
		cfg.Confirm = request.typedContext
	}
	worker.Cfg = &cfg
	worker.Out, worker.Err, worker.Stdin = io.Discard, io.Discard, nil
	worker.guarded = false
	runner := *worker.Run
	runner.Stdout, runner.Stderr = io.Discard, io.Discard
	worker.Run = &runner
	server, err := worker.consoleCreateTarget(ctx, request.env)
	if err != nil {
		return fail(err)
	}
	if request.server == "" || server != request.server {
		return fail(fmt.Errorf("selected environment API server changed; review the destination again"))
	}
	if request.prepared == nil {
		prepared, err := worker.prepareWorkspaceSuiteLift(ctx, suiteWorkspaceLiftOptions{Workspace: request.review.root, ExpectedSuite: request.review.suite.Digest, ExpectedFingerprint: request.review.prepared.Fingerprint, Namespace: request.namespace, Inference: "provider:" + request.provider, ResultPort: worker.quickstartResultPort, StrictReuse: false})
		return agentTUICreateResult{prepared: prepared, err: err}
	}
	summary := request.prepared.Plan.Summary()
	if summary.Target.Context != request.env.Name || summary.Target.Namespace != request.namespace || summary.SuiteDigest != request.review.suite.Digest || summary.Inference != "provider:"+request.provider {
		return fail(fmt.Errorf("Deploy plan differs from the reviewed source or destination"))
	}
	result, err := worker.applyWorkspaceSuiteLift(ctx, request.prepared, request.reviewedDigest)
	if err == nil && result.State != "ready" {
		err = fmt.Errorf("Deploy did not return successful placement")
	}
	if err == nil {
		err = worker.VerifyReleaseLiftPlacement(ctx, result)
	}
	return agentTUICreateResult{result: result, verified: err == nil, err: err}
}

type agentTUICreatePane struct {
	input                                                  textinput.Model
	env                                                    agentTUIEnvironment
	namespace, server                                      string
	review                                                 *suiteWorkspaceReview
	prepared                                               *preparedQuickstartSuite
	summary                                                ReleaseLiftSummary
	result                                                 ReleaseLiftResult
	provider, typedContext, stage                          string
	page, scroll, operation                                int
	running, finished, cancelling, executionStarted, ready bool
	frame                                                  int
	err                                                    error
	cancel                                                 context.CancelFunc
	report                                                 func(ReleaseLiftResult) error
}

// Only observed identities and outcomes survive terminal close, never source,
// specifications, execution content or the raw worker error.
func suiteWorkspaceLiftOutcomeSummary(result ReleaseLiftResult) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Deploy state: %s", tuiOneLine(result.State))
	for _, row := range result.Resources {
		fmt.Fprintf(&text, "\n%s/%s: %s; observed Ready=%t; UID=%s", tuiOneLine(row.Kind), tuiOneLine(row.Name), tuiOneLine(row.Outcome), row.Ready, tuiOneLine(row.UID))
	}
	return text.String()
}

// Exec releases Bubble Tea's terminal before writing to the App's diagnostic
// stream. Keep that bound writer rather than accepting the process stderr.
type suiteWorkspaceLiftReport struct {
	pane   *agentTUICreatePane
	report func(ReleaseLiftResult) error
}

func (r suiteWorkspaceLiftReport) Run() error {
	if err := r.report(r.pane.result); err != nil {
		r.pane.err = errors.Join(r.pane.err, fmt.Errorf("report deploy outcomes: %w", err))
		r.pane.ready = false
		return err
	}
	return nil
}
func (suiteWorkspaceLiftReport) SetStdin(io.Reader)  {}
func (suiteWorkspaceLiftReport) SetStdout(io.Writer) {}
func (suiteWorkspaceLiftReport) SetStderr(io.Writer) {}

func (p *agentTUICreatePane) reviewLines(width int) []string {
	if p.stage == "plan" {
		// Summary is identity-only, including every resource decision. Pagination
		// requires reaching the bottom before consuming its retained digest.
		raw, _ := json.MarshalIndent(p.summary, "", "  ")
		return agentTUIIndentedText("DEPLOY PLAN / IDENTITY REVIEW\n"+string(raw)+"\nConfirm Apply once, then Verify placement. Ready is not an answer.", width, 0)
	}
	if p.stage == "consent" {
		text := fmt.Sprintf("EXECUTION CONSENT\nContext: %s · Server: %s\nNamespace: %s · Inference: provider:%s (Ready openai only)\nSuite: %s\nSource fingerprint: %s\nAuthorize full inventory: %d evaluation Tasks and owned Provider/Agent resource placement, then exact local publication and Deploy planning. No automatic chat or answer Task. Cancellation/errors may leave resources.", p.env.Name, p.server, p.namespace, p.provider, p.review.suite.Digest, p.review.prepared.Fingerprint, len(p.review.inventory))
		if !p.env.Local {
			text += "\nType selected context exactly to authorize: " + p.env.Name + "\n" + p.input.View()
		} else {
			text += "\nEnter confirms this execution review."
		}
		return agentTUIIndentedText(text, width, 0)
	}
	if p.page == 0 {
		return p.review.sourceLines(width)
	}
	if p.page <= len(p.review.inventory) {
		return p.review.caseLines(p.page-1, width)
	}
	return agentTUIIndentedText("All source and cases reviewed. Enter to select the destination Provider; no execution authorized yet.", width, 0)
}

func (m agentTUIModel) updateCreatePane(msg tea.Msg) (tea.Model, tea.Cmd) {
	p := m.creation
	switch msg := msg.(type) {
	case agentTUICreateResult:
		if msg.pane != p || msg.operation != p.operation || !p.running {
			return m, nil
		}
		p.running = false
		if p.cancel != nil {
			p.cancel()
			p.cancel = nil
		}
		if p.stage == "applying" {
			p.result, p.report = msg.result, msg.report
			p.scroll = 0
		}
		if p.cancelling {
			p.finished = true
			p.err = context.Canceled
			return m, nil
		}
		p.err = msg.err
		if msg.err != nil {
			p.finished = true
			return m, nil
		}
		switch p.stage {
		case "preparing":
			if msg.prepared == nil || msg.prepared.Plan == nil {
				p.finished = true
				p.err = fmt.Errorf("Deploy preparation returned no plan")
				return m, nil
			}
			p.prepared = msg.prepared
			p.summary = msg.prepared.Plan.Summary()
			p.stage, p.scroll = "plan", 0
		case "applying":
			p.finished = true
			if !msg.verified || msg.result.State != "ready" {
				p.err = fmt.Errorf("Deploy placement was not verified")
				return m, nil
			}
			p.ready = true
			p.result = msg.result
			m.status = "Ready · verified suite placement (no answer requested)"
		default:
			if msg.review == nil || p.review != nil {
				p.finished = true
				p.err = fmt.Errorf("workspace review returned no fresh selection")
				return m, nil
			}
			p.review = msg.review
			p.page, p.scroll = 0, 0
		}
		return m, nil
	case quickstartTickMsg:
		if !p.running {
			return m, nil
		}
		p.frame++
		return m, quickstartTick()
	case tea.WindowSizeMsg:
		p.input.SetWidth(max(1, min(88, m.width-8)-4))
		return m, nil
	case tea.KeyPressMsg:
		cancel := msg.Code == tea.KeyEsc || msg.String() == "ctrl+c"
		if p.running {
			if cancel && !p.cancelling && p.cancel != nil {
				p.cancelling = true
				p.cancel()
			}
			return m, nil
		}
		if cancel || p.finished && msg.Code == tea.KeyEnter {
			if p.cancel != nil {
				p.cancel()
			}
			m.creation = nil
			if p.finished && len(p.result.Resources) > 0 && p.report != nil {
				report := p.report
				p.report = nil
				return m, tea.Exec(suiteWorkspaceLiftReport{pane: p, report: report}, nil)
			}
			return m, nil
		}
		if p.finished {
			return m, nil
		}
		if p.stage == "provider" {
			if msg.Code == tea.KeyEnter {
				provider := strings.TrimSpace(p.input.Value())
				if err := scaffold.ValidateObjectName(provider); err != nil {
					p.err = err
					return m, nil
				}
				p.provider = provider
				p.input.SetValue("")
				p.err = nil
				p.stage = "consent"
				p.scroll = 0
				return m, nil
			}
		} else if p.review != nil {
			width, height := max(1, min(88, m.width-8)-4), max(1, min(26, m.height-2)-7)
			rows := p.reviewLines(width)
			end := max(0, len(rows)-height)
			switch msg.Code {
			case tea.KeyDown:
				p.scroll = min(end, p.scroll+1)
			case tea.KeyUp:
				p.scroll = max(0, p.scroll-1)
			case tea.KeyPgDown:
				p.scroll = min(end, p.scroll+height)
			case tea.KeyPgUp:
				p.scroll = max(0, p.scroll-height)
			case tea.KeyHome:
				p.scroll = 0
			case tea.KeyEnd:
				p.scroll = end
			case tea.KeyLeft:
				if p.stage == "" && p.page > 0 {
					p.page--
					p.scroll = 0
				} else if p.stage == "consent" {
					p.stage = "provider"
					p.input.SetValue(p.provider)
					p.scroll = 0
				}
			case tea.KeyEnter:
				if p.scroll < end {
					p.scroll = min(end, p.scroll+height)
					return m, nil
				}
				switch p.stage {
				case "consent":
					if !p.env.Local && p.input.Value() != p.env.Name {
						p.err = fmt.Errorf("type selected context exactly: %s", p.env.Name)
						return m, nil
					}
					p.typedContext = p.input.Value()
					if m.opt.Demo {
						p.finished = true
						return m, nil
					}
					return m, m.startWorkspaceLift(false)
				case "plan":
					if m.opt.Demo {
						p.finished = true
						return m, nil
					}
					return m, m.startWorkspaceLift(true)
				default:
					if p.page <= len(p.review.inventory) {
						p.page++
						p.scroll = 0
					} else {
						p.stage = "provider"
						p.input.SetValue("")
						p.scroll = 0
					}
				}
				return m, nil
			default:
				if p.stage == "consent" && !p.env.Local {
					p.input, _ = p.input.Update(msg)
				}
			}
			return m, nil
		} else if msg.Code == tea.KeyEnter {
			if strings.TrimSpace(p.input.Value()) == "" {
				p.err = fmt.Errorf("explicit workspace root required; author offline with kmx create first")
				return m, nil
			}
			p.err = nil
			return m, m.startWorkspaceReview(p.input.Value(), nil)
		}
	}
	if p.running || p.finished {
		return m, nil
	}
	updated, msgCmd := p.input.Update(msg)
	p.input = updated
	return m, msgCmd
}

func (m agentTUIModel) startWorkspaceReview(root string, previous *suiteWorkspaceReview) tea.Cmd {
	p := m.creation
	if m.opt.Demo {
		p.finished = true
		return nil
	}
	if m.startCreate == nil {
		p.err = fmt.Errorf("workspace review unavailable")
		return nil
	}
	p.running = true
	result, cancel := m.startCreate(p.env, p.server, root, previous)
	p.cancel = cancel
	operation := p.operation
	return tea.Batch(quickstartTick(), func() tea.Msg { msg := <-result; msg.pane = p; msg.operation = operation; return msg })
}

func (m agentTUIModel) startWorkspaceLift(apply bool) tea.Cmd {
	p := m.creation
	if m.startCreateLift == nil {
		p.err = fmt.Errorf("suite Deploy unavailable")
		return nil
	}
	request := consoleWorkspaceLiftRequest{env: p.env, server: p.server, namespace: p.namespace, provider: p.provider, typedContext: p.typedContext, review: p.review}
	p.stage = "preparing"
	if apply {
		request.prepared = p.prepared
		request.reviewedDigest = p.summary.PlanDigest
		p.stage = "applying"
	}
	p.running, p.executionStarted, p.err = true, true, nil
	p.operation++
	operation := p.operation
	result, cancel := m.startCreateLift(request)
	p.cancel = cancel
	return tea.Batch(quickstartTick(), func() tea.Msg { msg := <-result; msg.pane = p; msg.operation = operation; return msg })
}

func (m agentTUIModel) creationView() string {
	p := m.creation
	width, height := max(12, min(88, m.width-8)), max(8, min(26, m.height-2))
	inner := width - 4
	fit := func(s string) string { return ansi.Truncate(tuiOneLine(s), inner, "…") }
	rows := []string{fit("CREATE AGENT · SOURCE → EVAL → DEPLOY · " + p.env.Name), fit("Namespace: " + p.namespace + " · Server: " + valueOr(p.server, "not available (not reloaded)"))}
	switch {
	case p.finished:
		message := "DEMO / review only · no execution"
		if p.ready {
			message = "Ready · Apply and placement Verify succeeded. No answer requested."
		}
		if p.err != nil {
			message = "Stopped: " + p.err.Error()
			if p.executionStarted || p.review != nil {
				message = "Stopped · resources may remain; no retry/cleanup. " + p.err.Error()
			}
		}
		if len(p.result.Resources) > 0 {
			message = suiteWorkspaceLiftOutcomeSummary(p.result) + "\n" + message
		}
		body := agentTUIIndentedText(message, inner, 0)
		rows = append(rows, body[:min(len(body), height-6)]...)
		rows = append(rows, "", "<enter> / <esc> close")
	case p.running:
		message := "Reading authored source and complete case inventory…"
		if p.stage == "preparing" {
			message = "Evaluating full inventory, publishing exact suite and preparing Deploy…"
		}
		if p.stage == "applying" {
			message = "Applying reviewed plan once, then verifying placement…"
		}
		if p.cancelling {
			message = "Cancelling; resources may remain. Waiting for operation to stop…"
		}
		rows = append(rows, "", fit([]string{"⠋", "⠙", "⠹", "⠸"}[p.frame%4]+" "+message), "", "<esc> cancel")
	case p.stage == "provider":
		rows = append(rows, "", fit("Destination Provider name (Ready openai only):"), fit(p.input.View()), fit("No host Foundry/Copilot modes or model override."), fit("<enter> review execution consent · <esc> cancel"))
		if p.err != nil {
			rows = append(rows, fit(p.err.Error()))
		}
	case p.review != nil:
		lines := p.reviewLines(inner)
		page := max(1, height-7)
		start := min(max(0, p.scroll), max(0, len(lines)-page))
		rows = append(rows, lines[start:min(len(lines), start+page)]...)
		rows = append(rows, fit(fmt.Sprintf("Lines %d–%d/%d", start+1, min(len(lines), start+page), len(lines))), fit("<enter> next/confirm · ↑↓ scroll · ← back · <esc> cancel"))
		if p.err != nil {
			rows = append(rows, fit(p.err.Error()))
		}
	default:
		rows = append(rows, "", fit("Select an already AUTHORED one-member native Orka workspace."), fit("Explicit root (no discovery fallback):"), fit(p.input.View()), fit("kmx create authors offline separately; this flow does not author."), fit("Review all source/cases before authorizing evaluation Tasks."))
		if p.err != nil {
			rows = append(rows, fit(p.err.Error()))
		}
		rows = rows[:min(len(rows), height-5)]
		rows = append(rows, fit("<enter> review source · <esc> cancel"))
	}
	rows = rows[:min(len(rows), height-4)]
	return lipgloss.NewStyle().Width(width).Padding(0, 1).Background(lipgloss.Color("#18232D")).Foreground(lipgloss.Color("#E6EDF3")).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Cyan).BorderBackground(lipgloss.Color("#18232D")).Render(strings.Join(rows, "\n"))
}
