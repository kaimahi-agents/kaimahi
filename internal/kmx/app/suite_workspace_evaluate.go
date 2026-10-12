package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	rt "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// EvaluateSuiteWorkspaceOptions is the concrete CLI/App workspace domain. It
// carries no bundle selection, runtime/model override, session or gate bypass.
type EvaluateSuiteWorkspaceOptions struct {
	Workspace, ToContext, ToNamespace, Inference, Out, Case string
	JudgeBaseURL, JudgeKeyEnv, ResultPort                   string
	CaseTimeout                                             time.Duration
	ShowContent                                             bool
}

var suiteEvaluationCaseID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

func suiteEvaluationError(code string) error { return fmt.Errorf("native suite evaluation: %s", code) }

// ValidateEvaluateSuiteWorkspaceOptions is side-effect free and runs before
// operational configuration in the CLI as well as at the App boundary.
func ValidateEvaluateSuiteWorkspaceOptions(opt EvaluateSuiteWorkspaceOptions) error {
	for _, field := range []struct{ name, value string }{{"workspace", opt.Workspace}, {"to-context", opt.ToContext}, {"to-namespace", opt.ToNamespace}, {"inference", opt.Inference}, {"out", opt.Out}} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("--%s requires an explicit non-empty value", field.name)
		}
		if !utf8.ValidString(field.value) || len(field.value) > 4096 || strings.IndexFunc(field.value, unicode.IsControl) >= 0 || scaffold.RefuseKeyShapes(field.value) != nil {
			return fmt.Errorf("invalid --%s selector", field.name)
		}
	}
	if !suiteMetadataStrings(opt.ToContext) || strings.IndexFunc(opt.ToContext, unicode.IsSpace) >= 0 {
		return suiteEvaluationError("invalid --to-context selector")
	}
	if scaffold.ValidateNamespace(opt.ToNamespace) != nil {
		return suiteEvaluationError("invalid --to-namespace selector")
	}
	provider, ok := strings.CutPrefix(opt.Inference, "provider:")
	if !ok || scaffold.ValidateObjectName(provider) != nil {
		return suiteEvaluationError("--inference requires provider:NAME")
	}
	if opt.Case != "" && (!suiteEvaluationCaseID.MatchString(opt.Case) || scaffold.RefuseKeyShapes(opt.Case) != nil) {
		return suiteEvaluationError("invalid --case selector")
	}
	if opt.CaseTimeout != 0 && (opt.CaseTimeout < 10*time.Second || opt.CaseTimeout > maxEvaluationCaseTimeout) {
		return suiteEvaluationError("--case-timeout must be between 10s and 9m")
	}
	if opt.ResultPort != "" {
		port, e := strconv.ParseUint(opt.ResultPort, 10, 16)
		if e != nil || port == 0 {
			return suiteEvaluationError("--result-port must be a TCP port number")
		}
	}
	if opt.JudgeBaseURL != "" {
		if _, e := canonicalEvaluationJudgeURL(opt.JudgeBaseURL); e != nil {
			return suiteEvaluationError("invalid --judge-base-url")
		}
	}
	if opt.JudgeKeyEnv != "" && (!evaluationJudgeKeyEnvRE.MatchString(opt.JudgeKeyEnv) || scaffold.RefuseKeyShapes(opt.JudgeKeyEnv) != nil || opt.JudgeBaseURL == "") {
		return suiteEvaluationError("invalid --judge-key-env configuration")
	}
	root, e := suiteAbsolutePath(opt.Workspace)
	if e != nil {
		return suiteEvaluationError("invalid workspace path")
	}
	out, e := suiteAbsolutePath(opt.Out)
	if e != nil || suitePathWithin(root, out) {
		return suiteEvaluationError("--out must be outside authored workspace")
	}
	return nil
}

// EvaluateSuiteWorkspace selects one exact prepared candidate, pins it before
// native work, then consumes only the loaded packaged bytes. Candidate writer
// exclusion is released before target preparation/model deadlines: another
// explicit evaluation may select a newer candidate without splicing this run.
func (a *App) EvaluateSuiteWorkspace(opt EvaluateSuiteWorkspaceOptions) error {
	if e := ValidateEvaluateSuiteWorkspaceOptions(opt); e != nil {
		return e
	}
	if a.Cfg == nil || a.Run == nil || a.Out == nil || a.Err == nil {
		return suiteEvaluationError("configured target and streams required")
	}
	ctx, stop := signal.NotifyContext(a.operationContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	state, e := config.StateDir()
	if e != nil {
		return suiteEvaluationError("private state unavailable")
	}
	output, e := preflightSuiteReceiptOutput(opt.Workspace, state, opt.Out)
	if e != nil {
		return e
	}
	defer output.Close()
	writer, e := lockSuiteCandidate(opt.Workspace, state)
	if e != nil {
		return e
	}
	// Every return in selection releases the lock; no later path owns it.
	suite, selected, decoded, rows, e := selectSuiteEvaluationCandidate(ctx, writer, opt)
	closeErr := writer.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	runID, e := randomHex(16)
	if e != nil {
		return suiteEvaluationError("run identity unavailable")
	}
	worker := *a
	cfg := *a.Cfg
	runner := *a.Run
	cfg.KubeContext, cfg.ContextSource = opt.ToContext, config.SourceFlag
	worker.Cfg, worker.Run = &cfg, &runner
	worker.guarded = false
	runner.Context = ctx
	runner.Stdout, runner.Stderr, runner.Echo = io.Discard, io.Discard, false
	// Do not replay a generated --context prefix alongside --to-context. Only
	// validated selectors are named, never authored case content or raw argv.
	command := []string{"kmx", "agent", "evaluate", "--workspace", shellArg(opt.Workspace), "--to-context", shellArg(opt.ToContext), "--to-namespace", shellArg(opt.ToNamespace), "--inference", shellArg(opt.Inference), "--out", shellArg(opt.Out)}
	if opt.Case != "" {
		command = append(command, "--case", shellArg(opt.Case))
	}
	for _, flag := range []struct{ name, value string }{{"result-port", opt.ResultPort}, {"judge-base-url", opt.JudgeBaseURL}, {"judge-key-env", opt.JudgeKeyEnv}} {
		if flag.value != "" {
			command = append(command, "--"+flag.name, shellArg(flag.value))
		}
	}
	if opt.CaseTimeout != 0 {
		command = append(command, "--case-timeout", opt.CaseTimeout.String())
	}
	if opt.ShowContent {
		command = append(command, "--show-content")
	}
	worker.InvocationCommand = strings.Join(command, " ")
	if worker.preflight(depKubectl) != nil {
		return suiteEvaluationError("kubectl preflight unavailable")
	}
	preparation, cancel := worker.waitContext(ctx, "suite-native-prepare", defaultEvaluationCaseTimeout)
	plan, e := worker.prepareSuiteNative(preparation, suite, opt.ToNamespace, strings.TrimPrefix(opt.Inference, "provider:"))
	cancel()
	if e != nil {
		return suiteEvaluationError("native preparation unavailable; no placement attempted")
	}
	// Admission/readiness is not evidence serializability. Reject a known
	// incompatible live subject before confirmation, placement or any Task.
	if validateSuiteEvidenceSubject(suite, plan.Subject) != nil {
		return suiteEvaluationError("native evidence subject unavailable; no placement attempted")
	}
	action := fmt.Sprintf("place suite-owned Provider and Agent %s in %s and execute %d evaluation Task(s) (never retried)", suite.MemberID, opt.ToNamespace, len(selected))
	if e = worker.guardOrkaMutation(ctx, CreateOptions{Name: suite.MemberID, Namespace: opt.ToNamespace}, action); e != nil {
		return suiteEvaluationError("target confirmation unavailable; no placement attempted")
	}
	// The shared result machinery's full preflight (existing account/Service,
	// granted token lifetime, loopback forward and absent-Task authorization)
	// is exercised before pair writes. It provisions no account or RBAC.
	if _, e = fmt.Fprintln(a.Err, "Task execution uses the selected ServiceAccount's full effective authority. v0.2.0 Task-read enforcement is unverified; v0.1.3 only authenticates result reads. Retain the namespaced Task-get grant. Discarding the token is not revocation."); e != nil {
		return suiteEvaluationError("authority notice unavailable")
	}
	quiet := worker
	quiet.Out, quiet.Err = io.Discard, io.Discard
	timeout := opt.CaseTimeout
	if timeout == 0 {
		timeout = defaultEvaluationCaseTimeout
	}
	authCtx, cancel := worker.waitContext(ctx, "suite-result-preflight", timeout)
	session, e := quiet.openOrkaResultSession(authCtx, CreateOptions{Namespace: opt.ToNamespace, ResultServiceAccount: orkaResultAccount, ResultPort: suiteEvaluationResultPort(opt.ResultPort)})
	if e == nil {
		probe, e2 := randomHex(8)
		if e2 != nil {
			e = e2
		} else {
			e = session.probe(authCtx, opt.ToNamespace, "kmx-eval-preflight-"+probe)
		}
		session.close()
	}
	cancel()
	if e != nil {
		return suiteEvaluationError("result authorization preflight unavailable; no placement attempted")
	}
	placeCtx, cancel := worker.waitContext(ctx, "suite-native-place", defaultEvaluationCaseTimeout)
	placement, placeErr := quiet.placeSuiteNative(placeCtx, plan)
	cancel()
	detail := ""
	runErr := placeErr
	if placeErr != nil {
		detail = "placement unavailable"
	} else {
		for i, p := range selected {
			if ctx.Err() != nil {
				detail = "run incomplete"
				runErr = ctx.Err()
				break
			}
			judge := evaluationJudgeConfig{}
			for _, assertion := range decoded[i].Assertions {
				if assertion.Type == "llmJudge" {
					judge = evaluationJudgeConfig{BaseURL: opt.JudgeBaseURL, KeyEnv: opt.JudgeKeyEnv}
					break
				}
			}
			caseCtx, cancel := worker.waitContext(ctx, "orka-evaluation-case", timeout)
			result, caseErr := worker.evaluateSuiteNative(caseCtx, placement, p, suiteNativeEvaluationOptions{ResultPort: opt.ResultPort, Judge: judge, ShowContent: opt.ShowContent})
			cancel()
			mapped, mapErr := suiteEvidenceCaseFromNative(p, result)
			if mapErr == nil {
				rows[i] = mapped
			} else {
				detail = "run incomplete"
			}
			if caseErr != nil || mapErr != nil {
				detail = "run incomplete"
				runErr = suiteEvaluationError("case or output unavailable")
			}
			// An output failure is not permission to continue exporting content or
			// retry execution. Retain all initialized unknown rows for unrun cases.
			if caseErr != nil || mapErr != nil {
				break
			}
		}
		finalCtx, cancel := worker.waitContext(ctx, "suite-native-final", 30*time.Second)
		finalErr := quiet.verifySuiteNative(finalCtx, placement)
		ended := finalCtx.Err() != nil
		cancel()
		if finalErr != nil || ended {
			detail = "subject unavailable"
			runErr = suiteEvaluationError("final subject unavailable")
		}
	}
	receipt, e := newSuiteEvaluationReceipt(suite, placement, runID, opt.Case != "", rows, detail)
	if e != nil {
		return e
	}
	saved, e := publishSuiteEvaluationReceipt(suite, plan.Subject, receipt, output)
	if e != nil {
		return e
	}
	e = output.Close()
	output.history, output.parent = nil, nil
	if e != nil {
		return e
	}
	coverage := "full"
	if opt.Case != "" {
		coverage = "diagnostic"
	}
	if _, e = fmt.Fprintf(a.Out, "suite evaluation %s: %s\nS: %s\nrunID: %s\nrequiredImages: []\nhistory: %s\nreceipt: %s\n", coverage, receipt.Result, suite.Digest, runID, saved.HistoryPath, saved.ExportPath); e != nil {
		return suiteEvaluationError("finished run summary unavailable; private evidence may remain")
	}
	if runErr != nil || !receipt.PromotionEligible {
		return suiteEvaluationError("did not complete a full passing run; inspect private evidence (diagnostic runs never qualify)")
	}
	return nil
}
func suiteEvaluationResultPort(port string) string {
	if port == "" {
		return "19180"
	}
	n, _ := strconv.ParseUint(port, 10, 16)
	return strconv.FormatUint(n, 10)
}

func selectSuiteEvaluationCandidate(ctx context.Context, w *suiteCandidateWriter, opt EvaluateSuiteWorkspaceOptions) (*workspace.NativeSuite, []workspace.PackagedCase, []rt.EvaluationCase, []suiteEvidenceCase, error) {
	refused := func(code string) (*workspace.NativeSuite, []workspace.PackagedCase, []rt.EvaluationCase, []suiteEvidenceCase, error) {
		return nil, nil, nil, nil, suiteEvaluationError(code)
	}
	prepared, e := workspace.Prepare(ctx, w.root, filepath.Join(w.state, "suites"))
	if e != nil {
		return refused("candidate preparation unavailable")
	}
	suite, e := workspace.Load(ctx, prepared)
	if e != nil {
		return refused("packaged candidate unavailable")
	}
	if len(suite.Cases) == 0 || len(suite.Cases) > 128 {
		return refused("bounded packaged case inventory required")
	}
	var selected []workspace.PackagedCase
	var parsed []rt.EvaluationCase
	var judgeCases []bundleEvaluationCase
	// Decode the complete inventory before selection/native work. Evidence and
	// execution helpers independently revalidate their strict trust boundaries.
	for _, p := range suite.Cases {
		c, e := rt.ParseEvaluationCase(p.Bytes)
		if e != nil {
			return refused("packaged case inventory unavailable")
		}
		if opt.Case == "" || opt.Case == c.ID {
			selected = append(selected, p)
			parsed = append(parsed, c)
			judgeCases = append(judgeCases, bundleEvaluationCase{Case: c})
		}
	}
	if len(selected) == 0 {
		return refused("selected case is absent from packaged inventory")
	}
	// Reuse the aggregate case/Task/assertion/judge reserve, not per-row size
	// checks. Add 256 KiB for the native envelope: the 34 target/binding/runtime
	// strings are capped at 2048 printable ASCII bytes (at most 2x in JCS),
	// leaving over 120 KiB for fixed subject fields, owned before/residual pair
	// metadata, markers and separators. Measure the known model once: the
	// helper's first metadata slot is empty because native cases omit a model.
	// This budgets admissible evidence, not arbitrary post-write diagnostics;
	// the existing exact writer/identity checks remain authoritative.
	const nativeMetadataReserve = 256 << 10
	if preflightEvaluationReceiptSize(judgeCases, "", suite.Model, strings.Repeat("x", nativeMetadataReserve)) != nil {
		return refused("selected cases exceed the conservative 1 MiB receipt budget; reduce the selection or use --case")
	}
	if _, e = newEvaluationJudge(evaluationJudgeConfig{BaseURL: opt.JudgeBaseURL, KeyEnv: opt.JudgeKeyEnv}, judgeCases, io.Discard); e != nil {
		return refused("selected judge configuration unavailable")
	}
	rows := make([]suiteEvidenceCase, 0, len(selected))
	for _, p := range selected {
		row, e := unknownSuiteEvidenceCase(p, opt.JudgeBaseURL)
		if e != nil {
			return nil, nil, nil, nil, e
		}
		rows = append(rows, row)
	}
	if e = w.pin(ctx, prepared); e != nil {
		return nil, nil, nil, nil, e
	}
	return suite, selected, parsed, rows, nil
}
