package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"golang.org/x/sys/unix"
)

type kagentEvaluationObservation struct {
	Service  kagentControllerService
	Platform kagentEvaluationPlatform
}

type kagentEvaluationOperation struct {
	Initial         kagentEvaluationPlatform
	Recheck         func() error
	Requests        map[string]kagentEvaluationExecution
	Tasks, Contexts map[string]bool
	Sent            bool
	OutputFailed    bool
}

func kagentObservedEvaluationPlatform(profile kagentProfile, service kagentControllerService, images []kagentEvaluationImage) (kagentEvaluationPlatform, error) {
	uniform := func(images []kagentEvaluationImage) (kagentEvaluationImage, error) {
		if len(images) == 0 {
			return kagentEvaluationImage{}, fmt.Errorf("qualified platform observation unavailable")
		}
		for _, image := range images {
			if image != images[0] {
				return kagentEvaluationImage{}, fmt.Errorf("mixed qualified platform observations")
			}
		}
		return images[0], nil
	}
	controller, err := uniform(service.ControllerImages)
	if err != nil {
		return kagentEvaluationPlatform{}, err
	}
	adk, err := uniform(images)
	if err != nil {
		return kagentEvaluationPlatform{}, err
	}
	platform := kagentEvaluationPlatform{SourceCommit: profile.Commit, ControllerImage: controller, ADKImage: adk, Controller: []agentruntime.ResourceEvidence{
		{Kind: "Service", Name: service.Name, Namespace: service.Namespace, UID: service.UID, ResourceVersion: service.ResourceVersion},
		{Kind: "Deployment", Name: service.DeploymentName, Namespace: service.Namespace, UID: service.DeploymentUID, ResourceVersion: service.DeploymentResourceVersion, Generation: service.DeploymentGeneration},
		{Kind: "ConfigMap", Name: service.ConfigMapName, Namespace: service.Namespace, UID: service.ConfigMapUID, ResourceVersion: service.ConfigMapResourceVersion},
	}}
	if !validKagentEvaluationPlatform(platform) {
		return platform, fmt.Errorf("qualified platform observation unavailable")
	}
	return platform, nil
}

func (a *App) evaluateKagentBundle(opt EvaluateAgentBundleOptions) error {
	if opt.Sessions != "" || opt.SessionsCA != "" || opt.FromSessions != "" || opt.SourceSessions != "" || opt.SourceSessionsCA != "" || opt.ShowContent || opt.ResultPort != "" {
		return fmt.Errorf("Kagent evaluation refuses Sessions, recorded references, --show-content and --result-port")
	}
	timeout := opt.CaseTimeout
	if timeout == 0 {
		timeout = defaultEvaluationCaseTimeout
	}
	if timeout < 10*time.Second || timeout > maxEvaluationCaseTimeout {
		return fmt.Errorf("--case-timeout must be between 10s and %s", maxEvaluationCaseTimeout)
	}
	worker, bundle, adapter, rendered, recheck, err := a.prepareKagentBundle(opt.BundleDir, opt.Bindings, opt.ToContext, opt.ToNamespace)
	if err != nil {
		return fmt.Errorf("Kagent evaluation requires strict readable source/bindings and explicit --to-context with a matching namespace")
	}
	desired, err := kagentBundleFromRendered(rendered)
	if err != nil {
		return fmt.Errorf("Kagent evaluation desired rendering unavailable")
	}
	engine := desired.Agent["spec"].(map[string]any)["declarative"].(map[string]any)["runtime"].(string)
	if engine != "go" {
		return fmt.Errorf("Kagent v0.10.3 evaluation supports only Go; Python is render-only")
	}
	name, namespace := kagentObjectName(desired.Agent), kagentObjectNamespace(desired.Agent)
	cases, files, caseRecheck, err := loadKagentEvaluationCases(bundle, adapter.lift.bundleInfo)
	if err != nil {
		return err
	}
	if opt.Case != "" {
		found := false
		for i, c := range cases {
			if c.Case.ID == opt.Case {
				cases, files = cases[i:i+1], files[i:i+1]
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("selected Kagent evaluation case unavailable")
		}
	}
	judge, err := newEvaluationJudge(evaluationJudgeConfig{BaseURL: opt.JudgeBaseURL, KeyEnv: opt.JudgeKeyEnv}, cases, a.Out)
	if err != nil {
		return err
	}
	if err = checkLiftReceiptsDir(bundle); err != nil {
		return fmt.Errorf("Kagent evaluation private receipt directory unavailable")
	}
	if info, err := os.Lstat(filepath.Join(bundle, "receipts", "runs")); !os.IsNotExist(err) && (err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("Kagent evaluation history directory unavailable; requires a directory, not a link")
	}
	root, err := openKagentStatusBundle(bundle, adapter.lift.bundleInfo)
	if err != nil {
		return fmt.Errorf("Kagent evaluation bundle directory unavailable")
	}
	defer root.Close()
	if err = checkKagentReceiptDirs(root, false); err != nil {
		return fmt.Errorf("Kagent evaluation private deployment receipts unavailable")
	}
	ctx, stop := signal.NotifyContext(worker.operationContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	worker.notef("Evaluate destination: context %s, namespace %s, Agent %s", opt.ToContext, namespace, name)
	worker.notef("Each of %d cases sends one fresh blocking A2A Task, never retried. Kagent stores full prompts, history and answers in its database; default upstream session retention is unlimited; Kubernetes audit policy may capture Service-proxy request and response bodies.", len(cases))
	if judge != nil {
		worker.notef("Judge assertions separately send input, answer and rubric to the configured judge endpoint.")
	}
	args := []string{"bundle", "evaluate", opt.BundleDir, "--runtime", "kagent", "--runtime-version", "v0.10.3", "--to-context", opt.ToContext, "--to-namespace", namespace}
	if opt.Bindings != "" {
		args = append(args, "--bindings", opt.Bindings)
	}
	if opt.Case != "" {
		args = append(args, "--case", opt.Case)
	}
	action := fmt.Sprintf("execute %d Kagent evaluation Task(s) in %s; full prompts/history/answers retained in the database (default unlimited retention); audit policy may capture Service-proxy request and response bodies", len(cases), namespace)
	if err = worker.guardKagentOperation(ctx, namespace, action, worker.operationCommand(args...)); err != nil {
		return err
	}
	uid, err := worker.kagentClusterUID(ctx)
	if err != nil {
		return fmt.Errorf("Kagent evaluation cluster identity unavailable")
	}
	target := agentruntime.DeployTarget{Runtime: agentruntime.Kagent, Context: opt.ToContext, Namespace: namespace}
	receiptName := kagentLiftReceiptName(opt.ToContext, namespace, uid)
	var saved kagentReceiptSnapshot
	expected, err := readKagentLiftReceiptEvidenceAt(root, receiptName, target, uid, name, &saved)
	if err != nil || expected == nil {
		return fmt.Errorf("Kagent evaluation requires a complete selected-target deployment receipt; lift first")
	}
	preparedInfo := adapter.lift.bundleInfo
	inputRecheck := func() error {
		if recheck() != nil || caseRecheck() != nil {
			return fmt.Errorf("Kagent evaluation local inputs changed")
		}
		currentRoot, err := openKagentStatusBundle(bundle, preparedInfo)
		if err != nil {
			return fmt.Errorf("Kagent evaluation bundle directory changed")
		}
		defer currentRoot.Close()
		var current kagentReceiptSnapshot
		receipt, err := readKagentLiftReceiptEvidenceAt(currentRoot, receiptName, target, uid, name, &current)
		if err != nil || receipt == nil || !os.SameFile(saved.Parent, current.Parent) || !os.SameFile(saved.File, current.File) || !bytes.Equal(saved.Raw, current.Raw) {
			return fmt.Errorf("Kagent evaluation deployment receipt changed")
		}
		return nil
	}
	ref := agentruntime.AgentRef{Runtime: agentruntime.Kagent, Context: opt.ToContext, Namespace: namespace, Name: name, UID: expected.Resources[1].UID}
	var initial kagentEvaluationObservation
	status, err := worker.observeKagentBundleStatus(ctx, ref, agentruntime.StatusOptions{Desired: &rendered, Expected: expected}, &initial)
	if err != nil || kagentStatusField(status, "state") != "ready" {
		return fmt.Errorf("Kagent evaluation initial receipt-bound qualification unavailable: %s", kagentStatusField(status, "detail"))
	}
	if err = inputRecheck(); err != nil {
		return err
	}
	runID, err := randomHex(16)
	if err != nil {
		return fmt.Errorf("Kagent evaluation request identity unavailable")
	}
	model := desired.ModelConfig["spec"].(map[string]any)["model"].(string)
	receipt := kagentEvaluationReceipt{SchemaVersion: 2, RunID: runID, Bundle: name, PortableDigest: rendered.PortableDigest(), RenderedDigest: rendered.RenderedDigest(), DeploymentReceiptDigest: fmt.Sprintf("%x", sha256.Sum256(saved.Raw)), CasesDigest: agentruntime.EvaluationCasesDigest(files), FullCaseSet: opt.Case == "", GitCommit: liftAgentCommit(ctx, bundle, rendered.PortableDigest()), Target: kagentEvaluationTarget{
		Runtime: agentruntime.Kagent, RuntimeVersion: "v0.10.3", Engine: "go", Context: opt.ToContext, ClusterUID: uid, Namespace: namespace, Agent: name, AgentUID: ref.UID, AgentGeneration: expected.Resources[1].Generation, ModelConfigUID: expected.Resources[0].UID, ModelConfigGeneration: expected.Resources[0].Generation, Model: model, Platform: initial.Platform, Snapshot: kagentEvaluationSnapshot{Proofs: expected.Proofs, Workload: expected.Workload, Dependencies: expected.Dependencies},
	}}
	if err = preflightKagentEvaluationReceiptSize(cases, receipt); err != nil {
		return err
	}
	op := &kagentEvaluationOperation{Initial: initial.Platform, Recheck: inputRecheck, Requests: map[string]kagentEvaluationExecution{}, Tasks: map[string]bool{}, Contexts: map[string]bool{}}
	for _, c := range cases {
		rpc, err := randomHex(16)
		if err != nil {
			return fmt.Errorf("Kagent evaluation request identity unavailable")
		}
		message, err := randomHex(16)
		if err != nil {
			return fmt.Errorf("Kagent evaluation request identity unavailable")
		}
		op.Requests[c.Case.ID] = kagentEvaluationExecution{RPCID: "rpc-" + rpc, RequestMessageID: "message-" + message}
	}
	adapter.evaluation = op
	stopped := false
	for i, c := range cases {
		caseCtx, cancel := ctx, func() {}
		if !stopped {
			caseCtx, cancel = worker.waitContext(ctx, "kagent-evaluation-case", timeout)
		}
		var result agentruntime.EvaluationReceipt
		if stopped {
			result = kagentUnknownEvaluation(caseCtx, c.Case, op.Requests[c.Case.ID], judge)
		} else {
			result, err = adapter.Evaluate(caseCtx, ref, agentruntime.EvaluationRequest{CaseID: c.Case.ID, Input: c.Case.Input, ExpectContains: c.Case.ExpectContains, Assertions: c.Case.Assertions, PortableDigest: rendered.PortableDigest(), Desired: &rendered, Expected: expected, Judge: judge})
			if err != nil {
				if !op.Sent {
					cancel()
					return fmt.Errorf("Kagent evaluation pre-send qualification unavailable; no Task sent")
				}
				result = kagentUnknownEvaluation(caseCtx, c.Case, op.Requests[c.Case.ID], judge)
				result.Stop = true
			}
			stopped = result.Stop
		}
		cancel()
		receipt.Cases = append(receipt.Cases, kagentEvaluationResult{ID: c.Case.ID, Verdict: string(result.Verdict), CaseDigest: agentruntime.EvaluationCaseDigest(files[i].Bytes), InputDigest: agentruntime.EvaluationInputDigest(c.Case.Input), Assertions: result.Assertions, Execution: *result.Execution, AnswerSHA256: result.AnswerSHA256, Detail: result.Detail})
		if _, err := fmt.Fprintln(a.Out, bundleEvaluationVerdictLine(result)); err != nil {
			op.OutputFailed, stopped = true, true
		}
	}
	receipt.Result = string(agentruntime.EvaluationPass)
	counts := map[string]int{}
	for _, c := range receipt.Cases {
		counts[c.Verdict]++
		if c.Verdict == "fail" {
			receipt.Result = "fail"
		} else if c.Verdict != "pass" && receipt.Result != "fail" {
			receipt.Result = "unknown"
		}
	}
	if err = writeKagentEvaluationReceipt(bundle, receipt); err != nil {
		return fmt.Errorf("Kagent evaluation finished but its bounded private receipt was not saved")
	}
	_, outputErr := fmt.Fprintf(a.Out, "\nevaluation %s: %d passed, %d failed, %d unknown\nhistory: %s\nreceipt: %s\n", receipt.Result, counts["pass"], counts["fail"], counts["unknown"], evaluationHistoryPath(bundle, runID), kagentEvaluationLatestPath(bundle, opt.ToContext, namespace, uid))
	if op.OutputFailed || outputErr != nil {
		return fmt.Errorf("Kagent evaluation output unavailable; inspect private receipts, do not resend")
	}
	if receipt.Result != "pass" {
		return fmt.Errorf("Kagent evaluation did not pass; inspect verdicts and private receipts")
	}
	return nil
}

func preflightKagentEvaluationReceiptSize(cases []bundleEvaluationCase, receipt kagentEvaluationReceipt) error {
	if len(cases) == 0 || !validKagentEvaluationTarget(receipt.Target, receipt.Bundle) {
		return fmt.Errorf("Kagent evaluation target snapshot cannot be represented in a strict receipt")
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("Kagent evaluation receipt unavailable")
	}
	remaining := maxSessionsReceiptBytes - len(raw) - 8192
	for _, c := range cases {
		remaining -= 4096
		for range c.Case.ExpectContains {
			remaining -= 384
		}
		for _, assertion := range c.Case.Assertions {
			remaining -= evaluationAssertionReceiptBytes(assertion)
		}
		if remaining < 0 {
			return fmt.Errorf("selected Kagent evaluation cases and actual target exceed the conservative 1 MiB receipt budget")
		}
	}
	return nil
}

func kagentUnknownEvaluation(ctx context.Context, c agentruntime.EvaluationCase, execution kagentEvaluationExecution, judge agentruntime.EvaluationJudge) agentruntime.EvaluationReceipt {
	return agentruntime.EvaluationReceipt{CaseID: c.ID, Verdict: agentruntime.EvaluationUnknown, Detail: evaluationExecutionUnavailable, Execution: &execution, Assertions: evaluateCaseWithJudge(ctx, c, agentruntime.EvaluationEvidence{ToolSupported: false}, judge)}
}

func (a kagentRuntimeAdapter) evaluateKagentCase(ctx context.Context, ref agentruntime.AgentRef, request agentruntime.EvaluationRequest) (agentruntime.EvaluationReceipt, error) {
	op := a.evaluation
	if op == nil || a.app == nil || request.Desired == nil || request.Expected == nil || len(request.Expected.Resources) != 2 || request.PortableDigest != request.Desired.PortableDigest() || ref.UID != request.Expected.Resources[1].UID {
		return agentruntime.EvaluationReceipt{}, fmt.Errorf("Kagent evaluation requires bound desired and expected placement")
	}
	execution, ok := op.Requests[request.CaseID]
	_, bounded := ctx.Deadline()
	if !ok || !bounded || ctx.Err() != nil {
		return agentruntime.EvaluationReceipt{}, fmt.Errorf("Kagent evaluation requires fresh request identity and active case deadline")
	}
	c := agentruntime.EvaluationCase{ID: request.CaseID, Input: request.Input, ExpectContains: request.ExpectContains, Assertions: request.Assertions}
	result := kagentUnknownEvaluation(ctx, c, execution, request.Judge)
	observe := func() (kagentEvaluationObservation, error) {
		var observed kagentEvaluationObservation
		if err := op.Recheck(); err != nil {
			return observed, err
		}
		status, err := a.app.observeKagentBundleStatus(ctx, ref, agentruntime.StatusOptions{Desired: request.Desired, Expected: request.Expected}, &observed)
		if err != nil || kagentStatusField(status, "state") != "ready" || !reflect.DeepEqual(observed.Platform, op.Initial) {
			return observed, fmt.Errorf("Kagent evaluation attribution unavailable")
		}
		if err := op.Recheck(); err != nil {
			return observed, err
		}
		return observed, nil
	}
	observed, err := observe()
	if err != nil {
		return agentruntime.EvaluationReceipt{}, err
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": execution.RPCID, "method": "message/send", "params": map[string]any{"message": map[string]any{"kind": "message", "role": "user", "messageId": execution.RequestMessageID, "parts": []any{map[string]any{"kind": "text", "text": request.Input}}}, "configuration": map[string]any{"blocking": true}}})
	if err != nil {
		return agentruntime.EvaluationReceipt{}, fmt.Errorf("Kagent evaluation request unavailable")
	}
	deadline, _ := ctx.Deadline()
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return agentruntime.EvaluationReceipt{}, fmt.Errorf("Kagent evaluation deadline exhausted before send")
	}
	path := kagentServiceProxyPath(observed.Service, "/api/a2a/"+url.PathEscape(ref.Namespace)+"/"+url.PathEscape(ref.Name))
	op.Sent = true
	raw, sendErr := a.app.kagentCaptureWithTimeout(ctx, remaining, remaining.String(), body, "create", "--raw", path, "-f", "-")
	answer := ""
	var parseErr error
	if sendErr == nil {
		execution, answer, parseErr = parseKagentEvaluationResponse(raw, execution, request.Input)
		if execution.TaskID != "" && (op.Tasks[execution.TaskID] || op.Contexts[execution.ContextID]) {
			execution = op.Requests[request.CaseID]
			parseErr = fmt.Errorf("Kagent evaluation returned reused native identity")
		}
		if execution.TaskID != "" {
			op.Tasks[execution.TaskID], op.Contexts[execution.ContextID] = true, true
		}
	}
	result.Execution = &execution
	// Attribution takes precedence over both a native failure and an answer.
	if _, err = observe(); err != nil {
		result.Stop = true
		return result, nil
	}
	if sendErr != nil || parseErr != nil {
		result.Stop = true
		return result, nil
	}
	switch execution.State {
	case "failed", "canceled", "rejected":
		result.Verdict = agentruntime.EvaluationFail
		result.Detail = evaluationExecutionFailed
	case "completed":
		if _, err := fmt.Fprintln(a.app.Out, safeTerminal(answer)); err != nil {
			op.OutputFailed, result.Stop = true, true
			return result, nil
		}
		result.AnswerSHA256 = agentruntime.EvaluationAnswerDigest(answer)
		result.Detail = ""
		result.Assertions = evaluateCaseWithJudge(ctx, c, agentruntime.EvaluationEvidence{Answer: answer, AnswerAvailable: true, ToolSupported: false}, request.Judge)
		result.Verdict = agentruntime.EvaluationAssertionsVerdict(result.Assertions)
	default:
		result.Stop = true
	}
	return result, nil
}

type kagentEvaluationCaseSnapshot struct {
	Name string
	Raw  []byte
	Info os.FileInfo
}

func loadKagentEvaluationCases(bundle string, prepared os.FileInfo) ([]bundleEvaluationCase, []agentruntime.EvaluationCaseFile, func() error, error) {
	read := func() ([]kagentEvaluationCaseSnapshot, os.FileInfo, error) {
		root, err := openKagentStatusBundle(bundle, prepared)
		if err != nil {
			return nil, nil, err
		}
		defer root.Close()
		fd, err := unix.Openat(int(root.Fd()), agentruntime.EvaluationCaseDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, nil, err
		}
		dir := os.NewFile(uintptr(fd), "eval")
		defer dir.Close()
		parent, err := dir.Stat()
		if err != nil {
			return nil, nil, err
		}
		entries, err := dir.ReadDir(1001)
		if err != nil && err != io.EOF {
			return nil, nil, err
		}
		if len(entries) > 1000 {
			return nil, nil, fmt.Errorf("case directory exceeds bounded entry count")
		}
		var snapshots []kagentEvaluationCaseSnapshot
		total := 0
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			if scaffold.RefuseKeyShapes(entry.Name()) != nil {
				return nil, nil, fmt.Errorf("unsafe case filename")
			}
			file, err := openKagentFile(dir, entry.Name())
			if err != nil {
				return nil, nil, err
			}
			info, err := file.Stat()
			if err != nil {
				file.Close()
				return nil, nil, err
			}
			raw, err := readKagentOpenedFile(file, info, false)
			file.Close()
			if err != nil {
				return nil, nil, err
			}
			total += len(raw)
			if total > maxKagentFileBytes || len(snapshots) >= 1000 {
				return nil, nil, fmt.Errorf("case snapshot exceeds bound")
			}
			snapshots = append(snapshots, kagentEvaluationCaseSnapshot{Name: entry.Name(), Raw: raw, Info: info})
		}
		return snapshots, parent, nil
	}
	snapshots, parent, err := read()
	if err != nil || len(snapshots) == 0 {
		return nil, nil, nil, fmt.Errorf("Kagent evaluation requires bounded regular eval/*.yaml cases, not links")
	}
	// ReadDir on a descriptor is unsorted; preserve the shared filename ordering.
	slices.SortFunc(snapshots, func(a, b kagentEvaluationCaseSnapshot) int { return strings.Compare(a.Name, b.Name) })
	var cases []bundleEvaluationCase
	var files []agentruntime.EvaluationCaseFile
	seen := map[string]bool{}
	for _, s := range snapshots {
		c, err := agentruntime.ParseEvaluationCase(s.Raw)
		if err != nil || seen[c.ID] {
			return nil, nil, nil, fmt.Errorf("Kagent evaluation case is invalid or repeats an ID")
		}
		seen[c.ID] = true
		cases = append(cases, bundleEvaluationCase{File: s.Name, Digest: agentruntime.EvaluationCaseDigest(s.Raw), Case: c})
		files = append(files, agentruntime.EvaluationCaseFile{Name: s.Name, Bytes: s.Raw})
	}
	recheck := func() error {
		current, dir, err := read()
		if err != nil || !os.SameFile(parent, dir) || len(current) != len(snapshots) {
			return fmt.Errorf("Kagent evaluation case snapshot changed")
		}
		slices.SortFunc(current, func(a, b kagentEvaluationCaseSnapshot) int { return strings.Compare(a.Name, b.Name) })
		for i, s := range snapshots {
			if s.Name != current[i].Name || !os.SameFile(s.Info, current[i].Info) || !bytes.Equal(s.Raw, current[i].Raw) {
				return fmt.Errorf("Kagent evaluation case snapshot changed")
			}
		}
		return nil
	}
	return cases, files, recheck, nil
}
