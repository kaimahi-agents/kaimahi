package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"os"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

var errSuiteKagentBindings = errors.New("native kagent requires bounded confined author-owned bindings; inputs must remain unchanged")

type suiteKagentBindingSnapshot struct {
	path         string
	parent, file os.FileInfo
	raw          []byte
	binding      agentruntime.KagentBindings
}

func readSuiteKagentBindings(path string) ([]byte, os.FileInfo, os.FileInfo, error) {
	root, name, parent, err := openSessionReferenceDirectory(path)
	if err != nil {
		return nil, nil, nil, errSuiteKagentBindings
	}
	defer root.Close()
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		return nil, nil, nil, errSuiteKagentBindings
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, nil, nil, errSuiteKagentBindings
	}
	defer directory.Close()
	file, err := openKagentFile(directory, name)
	if err != nil {
		return nil, nil, nil, errSuiteKagentBindings
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !os.SameFile(before, info) {
		return nil, nil, nil, errSuiteKagentBindings
	}
	raw, err := readKagentOpenedFile(file, info, false)
	current, e := root.Lstat(name)
	if err != nil || e != nil || !os.SameFile(info, current) {
		return nil, nil, nil, errSuiteKagentBindings
	}
	return raw, parent, info, nil
}
func snapshotSuiteKagentBindings(path string) (*suiteKagentBindingSnapshot, error) {
	raw, parent, file, err := readSuiteKagentBindings(path)
	if err != nil {
		return nil, err
	}
	b, err := agentruntime.ParseKagentBindings(raw)
	if errors.Is(err, scaffold.ErrUnresolvedPlaceholder) {
		return nil, err
	}
	if err != nil || b.ModelConfig.Provider != "openai" {
		return nil, errSuiteKagentBindings
	}
	return &suiteKagentBindingSnapshot{path, parent, file, raw, b}, nil
}
func suiteKagentRequestID(id, prefix string) bool {
	value := strings.TrimPrefix(id, prefix)
	if value == id || len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func suiteKagentResponseHistory(raw []byte) bool {
	var r struct {
		Result struct{ History []json.RawMessage }
	}
	return json.Unmarshal(raw, &r) == nil && len(r.Result.History) > 0
}
func (s *suiteKagentBindingSnapshot) recheck() error {
	raw, parent, file, err := readSuiteKagentBindings(s.path)
	if err != nil || !os.SameFile(parent, s.parent) || !os.SameFile(file, s.file) || !bytes.Equal(raw, s.raw) {
		return errSuiteKagentBindings
	}
	return nil
}

func (a *App) evaluateSuiteKagentWorkspace(ctx context.Context, p *PreparedSuiteWorkspaceEvaluation, changed, guardRefusal *bool) error {
	recheck := func(checkCtx context.Context) error {
		if err := checkCtx.Err(); err != nil {
			return err
		}
		if err := workspace.CheckCurrent(checkCtx, p.root, p.prepared); err != nil {
			return err
		}
		if p.bindings == nil {
			return errSuiteKagentBindings
		}
		if err := p.bindings.recheck(); err != nil {
			return err
		}
		return checkCtx.Err()
	}
	if err := recheck(ctx); err != nil {
		return err
	}
	native, err := a.prepareSuiteKagent(ctx, p.suite, p.bindings.binding)
	if err != nil {
		return err
	}
	// Placement qualification permits trusted-proxy; evaluation never does.
	if native.subject.Runtime.AuthMode != "unsecure" {
		return errors.New("native kagent evaluation supports only unsecure auth")
	}
	action := fmt.Sprintf("create new suite-owned Kagent ModelConfig and Agent and execute %d fresh blocking Tasks in %s; candidate and full prompts/answers/history retained upstream (default unlimited retention); Kubernetes audit policy may capture Service-proxy request and response bodies; input and packaged instructions separately transferred to the model", len(p.inventory), p.options.ToNamespace)
	command := a.InvocationCommand
	if command == "" {
		args := []string{"eval", "--workspace", p.root, "--to-context", p.options.ToContext, "--bindings", p.options.Bindings, "--out", p.options.Out, "--case-timeout", p.options.CaseTimeout.String()}
		parts := []string{"kmx"}
		for _, arg := range args {
			parts = append(parts, shellArg(arg))
		}
		command = strings.Join(parts, " ")
	}
	if err := a.guardKagentOperation(ctx, p.options.ToNamespace, action, command); err != nil {
		*guardRefusal = true
		return err
	}
	native.worker.guarded = true
	if err := recheck(ctx); err != nil {
		return err
	}
	if err := native.worker.verifySuiteKagentInputs(ctx, native); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.Out, "Workspace: %q; AgentSuite: %s\n", p.root, p.prepared.Descriptor.Digest); err != nil {
		return err
	}
	*changed = true
	placement, err := a.placeSuiteKagent(ctx, native, nil)
	if err != nil {
		return err
	}
	op := &kagentEvaluationOperation{Requests: map[string]kagentEvaluationExecution{}, Tasks: map[string]bool{}, Contexts: map[string]bool{}, RequireHistory: true}
	observations, err := evaluateSuiteRuns(ctx, p.inventory, p.options.CaseTimeout, a.Out, func(caseCtx context.Context, frame suitePassingInventoryCase) (suitePassingObservation, error) {
		rpc, e := randomHex(16)
		if e != nil {
			return suitePassingObservation{}, e
		}
		message, e := randomHex(16)
		if e != nil {
			return suitePassingObservation{}, e
		}
		execution := kagentEvaluationExecution{RPCID: "rpc-" + rpc, RequestMessageID: "message-" + message}
		// Request correlation is run-local; native Task/context uniqueness remains
		// shared across the entire operation.
		runOperation := *op
		runOperation.Requests = map[string]kagentEvaluationExecution{frame.Case.ID: execution}
		worker := native.worker.withRunContext(caseCtx)
		observe := func() (kagentEvaluationObservation, error) {
			var observation kagentEvaluationObservation
			if err := recheck(caseCtx); err != nil {
				return observation, err
			}
			if _, err := worker.observeSuiteKagent(caseCtx, native, placement.Observation); err != nil {
				return observation, err
			}
			if err := caseCtx.Err(); err != nil {
				return observation, err
			}
			if err := recheck(caseCtx); err != nil {
				return observation, err
			}
			r := native.subject.Runtime
			observation.Service = kagentControllerService{Name: r.ServiceName, Namespace: r.Namespace, UID: r.ServiceUID}
			return observation, nil
		}
		ref := agentruntime.AgentRef{Runtime: agentruntime.Kagent, Context: p.options.ToContext, Namespace: p.options.ToNamespace, Name: p.suite.MemberID, UID: placement.Observation.Resources[1].UID}
		result, _, e := worker.executeKagentEvaluationCase(caseCtx, ref, frame.Case, execution, &runOperation, observe, p.judge)
		if e == nil {
			e = caseCtx.Err()
		}
		if e == nil && p.judge != nil {
			// Judging is external I/O: re-observe attribution before crediting a case.
			_, e = observe()
		}
		if e == nil && result.Detail != "" && result.Verdict == agentruntime.EvaluationFail {
			e = errSuiteNativeCaseFailed
		}
		return suitePassingObservation{frame.Metadata.CaseDigest, frame.Metadata.InputDigest, result}, e
	})
	if err != nil {
		return err
	}
	if err := recheck(ctx); err != nil {
		return err
	}
	if _, err := native.worker.observeSuiteKagent(ctx, native, placement.Observation); err != nil {
		return err
	}
	if err := recheck(ctx); err != nil {
		return err
	}
	receipt, err := newSuiteKagentPassingReceipt(ctx, p, placement.Observation, observations)
	if err != nil {
		return err
	}
	if err := recheck(ctx); err != nil {
		return err
	}
	if err := p.publishPassingReceipt(ctx, receipt); err != nil {
		return err
	}
	return p.reportPassingReceipt(a.Out)
}
