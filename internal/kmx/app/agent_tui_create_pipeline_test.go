package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Run real Bubble Tea commands/worker deliveries instead of mocking Apply.
func consoleCreateKey(t *testing.T, m agentTUIModel, code rune) (agentTUIModel, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(tea.KeyPressMsg{Code: code})
	return next.(agentTUIModel), cmd
}
func consoleCreateDeliver(t *testing.T, m agentTUIModel, cmd tea.Cmd) agentTUIModel {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing worker command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatal("missing operation batch")
	}
	for _, child := range batch {
		msg := child()
		if _, ok := msg.(agentTUICreateResult); ok {
			next, _ := m.Update(msg)
			return next.(agentTUIModel)
		}
	}
	t.Fatal("worker result missing")
	return m
}

func TestConsoleCreationRealNativePipeline(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			a, _, _, f := nativeEvaluationFixture(t)
			opt := nativeEvaluationOptions(t)
			a.quickstartResultPort = f.opt.ResultPort
			for _, kind := range []string{"providers.core.orka.ai", "agents.core.orka.ai"} {
				if err := os.Remove(filepath.Join(os.Getenv("KMX_RECONCILE_DIR"), kind+".json")); err != nil {
					t.Fatal(err)
				}
			}
			baselineWrites := len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR")))
			var stdout, stderr bytes.Buffer
			a.Out, a.Err = &stdout, &stderr
			capture := a.captureCommand
			if remote {
				a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					if slices.Contains(cmd.Args, "config") {
						return json.NewEncoder(cmd.Stdout).Encode(map[string]any{
							"current-context": "kind-test",
							"contexts":        []any{map[string]any{"name": "kind-test", "context": map[string]any{"cluster": "kind-test"}}},
							"clusters":        []any{map[string]any{"name": "kind-test", "cluster": map[string]any{"server": "https://remote.example.invalid"}}},
						})
					}
					return capture(ctx, cmd)
				}
			}
			env := agentTUIEnvironment{Name: "kind-test", Local: !remote}
			server, err := a.consoleCreateTarget(t.Context(), env)
			if err != nil {
				t.Fatal(err)
			}
			env.Server = server
			m := newAgentTUIModel(AgentTUIOptions{Demo: true})
			m.columns[0].Env = env
			m = tuiKey(m, 'n', "n")
			m.opt.Demo = false
			var workers sync.WaitGroup
			a.bindSuiteWorkspaceReviewWorkers(t.Context(), &m, &workers)
			defer workers.Wait()
			m.creation.input.SetValue(opt.Workspace)
			m, cmd := consoleCreateKey(t, m, tea.KeyEnter)
			m = consoleCreateDeliver(t, m, cmd)
			if m.creation.review == nil || f.taskCreates(t) != 0 {
				t.Fatal("source review sent Task")
			}
			for _, c := range m.creation.review.inventory {
				f.answers[c.Case.Input] = nativeCanary + " answer"
			}
			// Every source/case page must be visited, not skipped by destination entry.
			for m.creation.page <= len(m.creation.review.inventory) {
				m.creation.scroll = len(m.creation.reviewLines(76))
				m = tuiKey(m, tea.KeyEnter, "")
			}
			m = tuiKey(m, tea.KeyEnter, "")
			if m.creation.stage != "provider" || f.taskCreates(t) != 0 {
				t.Fatal("case review bypassed provider stage")
			}
			m.creation.input.SetValue("inference")
			m = tuiKey(m, tea.KeyEnter, "")
			m.creation.scroll = len(m.creation.reviewLines(76))
			if remote {
				m = tuiKey(m, tea.KeyEnter, "")
				if m.creation.running || f.taskCreates(t) != 0 {
					t.Fatal("remote consent bypassed")
				}
				m.creation.input.SetValue("kind-test")
			}
			m, cmd = consoleCreateKey(t, m, tea.KeyEnter)
			m = consoleCreateDeliver(t, m, cmd)
			if m.creation.err != nil {
				t.Fatalf("prepare: %v", m.creation.err)
			}
			if m.creation.stage != "plan" || m.creation.finished || m.creation.ready || f.taskCreates(t) != 2 {
				t.Fatal("full evaluation did not stop for identity-only plan review")
			}
			if _, err := readSuitePassingReceipt(t.Context(), m.creation.prepared.Evidence); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(m.creation.prepared.Layout); err != nil {
				t.Fatal(err)
			}
			lines := strings.Join(m.creation.reviewLines(76), "\n")
			for _, want := range []string{"Provider", "Agent", m.creation.summary.PlanDigest, "same-subject"} {
				if !strings.Contains(lines, want) {
					t.Fatalf("plan omitted %q", want)
				}
			}
			if strings.Contains(lines, nativeCanary) {
				t.Fatal("plan echoed execution content")
			}
			// Enter cannot consume digest while unseen identity rows remain.
			m, cmd = consoleCreateKey(t, m, tea.KeyEnter)
			if cmd != nil || m.creation.running || m.creation.stage != "plan" {
				t.Fatal("plan pagination skipped")
			}
			m.creation.scroll = len(m.creation.reviewLines(76))
			m, cmd = consoleCreateKey(t, m, tea.KeyEnter)
			m = consoleCreateDeliver(t, m, cmd)
			if m.creation.err != nil || !m.creation.ready || m.creation.result.State != "ready" {
				t.Fatalf("verified Ready missing: %v", m.creation.err)
			}
			pairWrites := 0
			for _, call := range persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR")) {
				if call.Document["kind"] == "Provider" || call.Document["kind"] == "Agent" {
					pairWrites++
				}
			}
			if f.taskCreates(t) != 2 || pairWrites != baselineWrites+2 {
				t.Fatalf("Apply sent answer Task or rewrote evaluated pair: tasks=%d pairWrites=%d", f.taskCreates(t), pairWrites)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 || strings.Contains(m.status, nativeCanary) {
				t.Fatal("operation leaked private streams")
			}
			if !strings.Contains(m.status, "Ready") || strings.Contains(m.status, "answered") {
				t.Fatal("Ready claimed answer")
			}
		})
	}
}

type suiteWorkspaceReportTestModel struct {
	agentTUIModel
	initial tea.Cmd
}

func (m suiteWorkspaceReportTestModel) Init() tea.Cmd { return m.initial }

func TestConsoleCreationPartialResultVisibleAndReportedAfterClose(t *testing.T) {
	for _, wizard := range []bool{false, true} {
		for _, cancelling := range []bool{false, true} {
			t.Run(fmt.Sprintf("wizard=%t/cancelling=%t", wizard, cancelling), func(t *testing.T) {
				var diagnostics bytes.Buffer
				a := &App{Err: &diagnostics}
				m := newAgentTUIModel(AgentTUIOptions{Demo: true})
				m = tuiKey(m, 'n', "n")
				var workers sync.WaitGroup
				a.bindSuiteWorkspaceReviewWorkers(t.Context(), &m, &workers)
				// Use the real worker binding, supplying the partial observation at
				// the UI boundary; operation tests cover Apply itself.
				delivery, cancel := m.startCreateLift(consoleWorkspaceLiftRequest{})
				defer cancel()
				msg := <-delivery
				workers.Wait()
				p := m.creation
				p.running, p.executionStarted, p.cancelling = true, true, cancelling
				p.stage = "applying"
				msg.pane = p
				msg.result = ReleaseLiftResult{State: "partial", Summary: ReleaseLiftSummary{Model: nativeCanary}, Resources: []ReleaseLiftOutcome{
					{ReleaseLiftResource: ReleaseLiftResource{Kind: "Provider", Name: "owned-provider", UID: "\x1b[31mcreated-uid\x1b[0m", SpecDigest: nativeCanary}, Outcome: "created", Ready: false},
					{ReleaseLiftResource: ReleaseLiftResource{Kind: "Agent", Name: "owned-agent"}, Outcome: "not-attempted", Ready: false},
				}}
				next, _ := m.Update(msg)
				m = next.(agentTUIModel)
				view := ansi.Strip(m.creationView())
				for _, want := range []string{"partial", "Provider/owned-provider: created", "created-uid", "Agent/owned-agent: not-attempted", "Ready=false"} {
					if !strings.Contains(view, want) {
						t.Errorf("finished pane omitted %q: %s", want, view)
					}
				}
				if diagnostics.Len() != 0 || p.ready || strings.Contains(view, nativeCanary) {
					t.Fatal("report leaked while active, exposed content or claimed Ready")
				}
				var closed tea.Model
				var closeCmd tea.Cmd
				closeKey := tea.KeyPressMsg{Code: tea.KeyEnter}
				if cancelling {
					closeKey.Code = tea.KeyEsc
				}
				if wizard {
					closed, closeCmd = (suiteWorkspaceReviewModel{agentTUIModel: m, pane: p}).Update(closeKey)
				} else {
					closed, closeCmd = m.Update(closeKey)
					closeCmd = tea.Sequence(closeCmd, tea.Quit)
				}
				if closeCmd == nil {
					t.Fatal("close lost report command")
				}
				// Exercise actual Bubble Tea terminal-release/command transport.
				wrapper := suiteWorkspaceReportTestModel{initial: closeCmd}
				if wizard {
					wrapper.agentTUIModel = closed.(suiteWorkspaceReviewModel).agentTUIModel
				} else {
					wrapper.agentTUIModel = closed.(agentTUIModel)
				}
				transportCtx, stop := context.WithTimeout(t.Context(), 3*time.Second)
				defer stop()
				if _, err := tea.NewProgram(wrapper, tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithContext(transportCtx)).Run(); err != nil {
					t.Fatal(err)
				}
				report := diagnostics.String()
				for _, want := range []string{"partial", "Provider/owned-provider: created; observed Ready=false; UID=created-uid", "Agent/owned-agent: not-attempted; observed Ready=false; UID="} {
					if !strings.Contains(report, want) {
						t.Errorf("close diagnostics omitted %q: %s", want, report)
					}
				}
				if strings.Contains(report, nativeCanary) || strings.Contains(report, "\x1b") || strings.Count(report, "created-uid") != 1 {
					t.Fatal("report exposed content/control bytes or repeated identities")
				}
			})
		}
	}
}

// Skipping destination consent, or reporting Ready merely after prepare, breaks
// this model-stage test. Real operation tests below keep Apply/Verify unmocked.
func TestConsoleCreationRequiresDestinationAndPlanReview(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	opt := nativeEvaluationOptions(t)
	review, err := (&App{}).reviewSuiteWorkspace(t.Context(), opt.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.focus = 1
	m = tuiKey(m, 'n', "n")
	m.opt.Demo = false
	m.creation.review = review
	m.creation.page = len(review.inventory) + 1
	m = tuiKey(m, tea.KeyEnter, "")
	view := ansi.Strip(m.creationView())
	if !strings.Contains(view, "Provider name") || m.creation.running || m.creation.finished {
		t.Fatalf("missing destination selection: %s", view)
	}
	m.creation.input.SetValue("inference")
	m = tuiKey(m, tea.KeyEnter, "")
	view = ansi.Strip(strings.Join(m.creation.reviewLines(76), "\n"))
	for _, want := range []string{"evaluation Tasks", "Provider/Agent", m.creation.env.Name} {
		if !strings.Contains(view, want) {
			t.Fatalf("consent missing %q: %s", want, view)
		}
	}
	m.creation.scroll = len(m.creation.reviewLines(76))
	m = tuiKey(m, tea.KeyEnter, "")
	if m.creation.running || m.creation.finished {
		t.Fatal("remote execution accepted without typed context")
	}
}

func TestConsoleCreationLateFailureNeverClaimsNoDeployment(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m = tuiKey(m, 'n', "n")
	m.creation.running = true
	m.creation.executionStarted = true
	next, _ := m.Update(agentTUICreateResult{pane: m.creation, err: context.Canceled})
	m = next.(agentTUIModel)
	view := ansi.Strip(m.creationView())
	if strings.Contains(view, "NOT deployed") || !strings.Contains(view, "resources may remain") || strings.Contains(m.status, "Ready") {
		t.Fatal(view)
	}
}

// Inherited global confirmation must never bypass typed pane consent.
func TestConsoleCreationRemoteConsentAndCancelledOperation(t *testing.T) {
	a, _, _, f := nativeEvaluationFixture(t)
	opt := nativeEvaluationOptions(t)
	review, err := a.reviewSuiteWorkspace(t.Context(), opt.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg.Confirm = "remote"
	for _, typed := range []string{"", "wrong", "remote "} {
		out := a.consoleWorkspaceLift(t.Context(), consoleWorkspaceLiftRequest{
			env: agentTUIEnvironment{Name: "remote"}, namespace: OrkaNamespace,
			provider: "inference", typedContext: typed, review: review,
		})
		if out.err == nil || !strings.Contains(out.err.Error(), "type the selected context exactly") || out.prepared != nil || out.verified {
			t.Fatalf("typed consent bypassed: %+v", out)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out := a.consoleWorkspaceLift(ctx, consoleWorkspaceLiftRequest{})
	if out.err != context.Canceled || out.verified || f.out.Len() != 0 {
		t.Fatalf("cancelled operation proceeded: %+v", out)
	}
}

func TestConsoleCreationFingerprintDriftBeforeExecution(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	opt := nativeEvaluationOptions(t)
	review, err := (&App{}).reviewSuiteWorkspace(t.Context(), opt.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	// YAML spelling changes leave case semantics alone but change authored input.
	writeFile(t, opt.Workspace+"/eval/second.yaml", "id: second\ninput: '"+nativeCanary+" second'\nexpectContains: ['"+nativeCanary+"']\n# authored revision\n")
	out := (&App{}).consoleWorkspaceLift(t.Context(), consoleWorkspaceLiftRequest{
		env: agentTUIEnvironment{Name: "kind-test", Local: true}, namespace: OrkaNamespace,
		provider: "inference", review: review,
	})
	if out.err == nil || !strings.Contains(out.err.Error(), "workspace changed since preparation") || out.prepared != nil {
		t.Fatalf("fingerprint drift accepted: %+v", out)
	}
}

func TestConsoleCreationIgnoresPriorOperationOnSamePane(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m = tuiKey(m, 'n', "n")
	m.creation.running = true
	m.creation.stage = "applying"
	m.creation.operation = 2
	next, _ := m.Update(agentTUICreateResult{pane: m.creation, operation: 1, verified: true, result: ReleaseLiftResult{State: "ready"}})
	m = next.(agentTUIModel)
	if !m.creation.running || m.creation.finished || m.creation.ready {
		t.Fatal("stale operation changed current pane")
	}
}

func TestConsoleCreationDoesNotTrustUnverifiedReadyResult(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m = tuiKey(m, 'n', "n")
	m.creation.running = true
	m.creation.stage = "applying"
	next, _ := m.Update(agentTUICreateResult{pane: m.creation, result: ReleaseLiftResult{State: "ready"}})
	m = next.(agentTUIModel)
	if strings.Contains(m.status, "Ready") || m.creation.err == nil {
		t.Fatal("unverified result became Ready")
	}
}
