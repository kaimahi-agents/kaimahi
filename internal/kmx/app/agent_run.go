package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const (
	defaultEvaluationCaseTimeout = 5 * time.Minute
	maxEvaluationCaseTimeout     = 9 * time.Minute
)

// ErrTaskPending identifies an execution whose Task may still produce an answer.
// The CLI maps this error to exit status 2. Failed and cancelled Tasks are not pending.
var ErrTaskPending = errors.New("Task result pending")

type RunAgentOptions struct {
	Agent, Namespace, Prompt, PromptFile, ResultPort string
	Wait                                             time.Duration
}

type TaskResultOptions struct {
	Task, Namespace, ResultPort string
	Wait                        time.Duration
}

func orkaResultDeadline(wait time.Duration) (time.Duration, error) {
	if wait == 0 {
		wait = defaultEvaluationCaseTimeout
	}
	if wait < 10*time.Second || wait > maxEvaluationCaseTimeout {
		return 0, fmt.Errorf("--wait must be between 10s and %s", maxEvaluationCaseTimeout)
	}
	return wait, nil
}

func orkaResultPort(port string) (string, error) {
	if port == "" {
		return "19180", nil
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsed == 0 {
		return "", fmt.Errorf("--result-port must be a TCP port number")
	}
	return strconv.FormatUint(parsed, 10), nil
}

func taskRecovery(name, contextName, namespace string) string {
	return fmt.Sprintf("kmx task result %s --context %s --namespace %s --wait 5m", shellArg(name), shellArg(contextName), shellArg(namespace))
}

func pendingTask(name, contextName, namespace string) error {
	return fmt.Errorf("%w: Task %s may still be running or its result is not yet available; recover with: %s", ErrTaskPending, name, taskRecovery(name, contextName, namespace))
}

// orkaPhaseDeadlineError identifies expiry of a bounded Orka phase, even when
// its caller's larger execution budget is still live. Unrelated request errors
// must not acquire pending/recovery semantics just by wrapping a deadline.
type orkaPhaseDeadlineError struct{ err error }

func (e *orkaPhaseDeadlineError) Error() string { return e.err.Error() }
func (e *orkaPhaseDeadlineError) Unwrap() error { return e.err }

func orkaPhaseError(parent, phase context.Context, err error) error {
	deadline, bounded := phase.Deadline()
	parentDeadline, parentBounded := parent.Deadline()
	if bounded && (!parentBounded || deadline.Before(parentDeadline)) && phase.Err() == context.DeadlineExceeded && errors.Is(err, context.DeadlineExceeded) {
		return &orkaPhaseDeadlineError{err: err}
	}
	// Default phases inherit the parent's exact deadline and error semantics.
	return err
}

func runTaskWaitError(ctx context.Context, err error, name, contextName, namespace string) error {
	var phaseDeadline *orkaPhaseDeadlineError
	if err != nil && errors.Is(err, context.DeadlineExceeded) && (ctx.Err() == context.DeadlineExceeded || errors.As(err, &phaseDeadline)) {
		return pendingTask(name, contextName, namespace)
	}
	return err
}

// readRunPromptFile opens a regular file without following a substituted link.
// The size check applies to bytes actually read, even if the file grows.
func readRunPromptFile(path string) (string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", fmt.Errorf("read prompt file (must be regular, not a link): %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect prompt file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return "", fmt.Errorf("prompt file must be regular and no larger than 1 MiB")
	}
	body, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return "", fmt.Errorf("read prompt file: %w", err)
	}
	if len(body) > 1<<20 {
		return "", fmt.Errorf("prompt file exceeds 1 MiB")
	}
	return string(body), nil
}

func (a *App) RunAgent(opt RunAgentOptions) error {
	if a.Cfg == nil || a.Run == nil || a.Out == nil || a.Err == nil {
		return fmt.Errorf("run requires configured kubectl and streams")
	}
	if opt.Agent == "" {
		return fmt.Errorf("--agent is required")
	}
	if (opt.Prompt == "") == (opt.PromptFile == "") {
		return fmt.Errorf("supply exactly one of --prompt or --prompt-file")
	}
	if opt.PromptFile == "" && strings.TrimSpace(opt.Prompt) == "" {
		return fmt.Errorf("prompt must not be blank")
	}
	timeout, err := orkaResultDeadline(opt.Wait)
	if err != nil {
		return err
	}
	port, err := orkaResultPort(opt.ResultPort)
	if err != nil {
		return err
	}
	if opt.Agent != "" {
		if err := scaffold.ValidateObjectName(opt.Agent); err != nil {
			return err
		}
	}
	ctx := a.operationContext()
	worker := *a
	worker.guarded = false
	namespace := opt.Namespace
	if namespace == "" {
		namespace = OrkaNamespace
	}
	name := opt.Agent
	if err := scaffold.ValidateNamespace(namespace); err != nil {
		return err
	}
	if err := worker.preflight(depKubectl); err != nil {
		return err
	}
	raw, err := worker.orkaCapture(ctx, nil, "-n", namespace, "get", orkaPlural("Agent"), name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot read Agent %s/%s at destination: %w", namespace, name, err)
	}
	live := describeRunAgent(raw)
	switch {
	case !live.Found:
		return fmt.Errorf("Agent %s/%s is not deployed at this destination", namespace, name)
	case live.UID == "":
		return fmt.Errorf("Agent %s/%s reports no UID", namespace, name)
	case !live.Ready:
		return fmt.Errorf("Agent %s/%s is not Ready for its current generation", namespace, name)
	}
	// Preserve destination and execution flags, never private prompt text.
	worker.InvocationCommand = worker.operationCommand("run", "--agent", name, "--namespace", namespace,
		"--wait", timeout.String(), "--result-port", port, "--prompt-file", "-")
	if err := worker.guardOrkaMutation(ctx, CreateOptions{Name: name, Namespace: namespace}, "execute one AI Task against Agent "+name+" in "+namespace); err != nil {
		return err
	}
	prompt := opt.Prompt
	if opt.PromptFile != "" {
		if opt.PromptFile == "-" {
			if a.Stdin == nil {
				return fmt.Errorf("--prompt-file - requires stdin")
			}
			// Signal handling starts after input collection: a blocking stdin
			// read cannot observe a cancelled context.
			body, err := io.ReadAll(io.LimitReader(a.Stdin, 1<<20+1))
			if err != nil {
				return fmt.Errorf("read prompt from stdin: %w", err)
			}
			if len(body) > 1<<20 {
				return fmt.Errorf("prompt exceeds size limit")
			}
			prompt = string(body)
		} else {
			prompt, err = readRunPromptFile(opt.PromptFile)
			if err != nil {
				return err
			}
		}
	}
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("prompt must not be blank")
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Status, guard and input collection can take time. Confirm the same Agent
	// identity and current-generation readiness immediately before the session.
	current, err := worker.readOrkaObject(ctx, namespace, orkaIdentity{Kind: "Agent", Name: name, UID: live.UID, Generation: live.Generation})
	if err != nil {
		return err
	}
	if !orkaCurrentGenerationReady(current, live.Generation) {
		return fmt.Errorf("Agent %s/%s is no longer Ready for its current generation", namespace, name)
	}
	suffix, err := randomHex(8)
	if err != nil {
		return err
	}
	taskName := strings.TrimRight(name[:min(len(name), 40)], "-.") + "-run-" + suffix
	execCtx, cancel := worker.waitContext(ctx, "orka-execution", timeout)
	defer cancel()
	session, err := worker.openOrkaResultSession(execCtx, CreateOptions{Namespace: namespace, ResultServiceAccount: orkaResultAccount, ResultPort: port})
	if err != nil {
		return err
	}
	defer session.close()
	execCtx = session.ctx
	if err := session.probe(execCtx, namespace, taskName); err != nil {
		return err
	}
	// Session setup can outlive an Agent replacement or revision. Recheck
	// UID, generation and current-generation readiness before Task creation.
	latestRaw, err := worker.orkaCapture(execCtx, nil, "-n", namespace, "get", orkaPlural("Agent"), name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot recheck Agent %s/%s: %w", namespace, name, err)
	}
	latest := describeRunAgent(latestRaw)
	if !latest.Found || latest.UID != live.UID || latest.Generation != live.Generation {
		return fmt.Errorf("Agent %s/%s was replaced or changed before Task creation", namespace, name)
	}
	if !latest.Ready {
		return fmt.Errorf("Agent %s/%s is no longer Ready for its current generation", namespace, name)
	}
	// A successful name write precedes the only mutation. Nothing describing
	// this Task goes to stdout: the answer owns that stream exclusively.
	identityLine := fmt.Sprintf("Task name: %s\n", taskName)
	n, err := io.WriteString(worker.Err, identityLine)
	if err == nil && n != len(identityLine) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("Task name could not be written; no Task created: %w", err)
	}
	recoveryLine := fmt.Sprintf("Recovery: %s\n", taskRecovery(taskName, worker.Cfg.KubeContext, namespace))
	n, err = io.WriteString(worker.Err, recoveryLine)
	if err == nil && n != len(recoveryLine) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("Recovery command could not be written; no Task created: %w", err)
	}
	task := map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "Task", "metadata": map[string]any{"name": taskName, "namespace": namespace}, "spec": map[string]any{"type": "ai", "prompt": prompt, "agentRef": map[string]any{"name": name, "namespace": namespace}, "resources": map[string]any{}}}
	id, err := worker.createOrkaObject(execCtx, namespace, task)
	if err != nil {
		return fmt.Errorf("Task %s creation failed or was ambiguous; it may exist or be running, do not retry; recover with %s: %w", taskName, taskRecovery(taskName, worker.Cfg.KubeContext, namespace), err)
	}
	answer, err := worker.waitOrkaTaskResult(execCtx, namespace, id, session)
	if err != nil {
		outcome := runTaskWaitError(execCtx, err, taskName, worker.Cfg.KubeContext, namespace)
		if errors.Is(outcome, ErrTaskPending) {
			return outcome
		}
		return fmt.Errorf("%w; recover with: %s", outcome, taskRecovery(taskName, worker.Cfg.KubeContext, namespace))
	}
	_, err = fmt.Fprintln(worker.Out, answer)
	return err
}
