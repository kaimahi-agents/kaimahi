package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// TaskResult inspects a previously submitted AI Task. Non-follow inspection
// never opens a result session for a Task without a readable terminal answer.
func (a *App) TaskResult(opt TaskResultOptions) error {
	if a.Cfg == nil || a.Run == nil || a.Out == nil || a.Err == nil {
		return fmt.Errorf("task result requires configured kubectl and streams")
	}
	if err := scaffold.ValidateObjectName(opt.Task); err != nil {
		return err
	}
	namespace := strings.TrimSpace(opt.Namespace)
	if namespace == "" {
		namespace = OrkaNamespace
	}
	if err := scaffold.ValidateNamespace(namespace); err != nil {
		return err
	}
	follow := opt.Wait != 0
	timeout := 30 * time.Second
	if follow {
		var err error
		timeout, err = orkaResultDeadline(opt.Wait)
		if err != nil {
			return err
		}
	}
	port, err := orkaResultPort(opt.ResultPort)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(a.operationContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The wait cap is below the session token's lifetime even when polling the
	// Task consumes most of it. Without --wait, bound the single inspection.
	ctx, cancel := a.waitContext(ctx, "task-result", timeout)
	defer cancel()
	worker := a.withRunContext(ctx)
	if err := worker.preflightOrkaRemote(); err != nil {
		return err
	}
	inspectCtx, stopInspect := worker.waitContext(ctx, "task-result-inspect", 0)
	raw, err := worker.orkaCapture(inspectCtx, nil, "-n", namespace, "get", orkaPlural("Task"), opt.Task, "--ignore-not-found=true", "-o", "json")
	stopInspect()
	if err != nil {
		inspected := fmt.Errorf("cannot inspect Task %s/%s: %w", namespace, opt.Task, err)
		if follow {
			return a.runTaskWaitError(inspectCtx, inspected, opt.Task, a.Cfg.KubeContext, namespace)
		}
		return inspected
	}
	if len(raw) == 0 {
		return fmt.Errorf("Task %s/%s not found", namespace, opt.Task)
	}
	id, taskType, err := decodeTaskResultIdentity(raw, opt.Task, namespace)
	if err != nil {
		return err
	}
	if taskType != "ai" {
		return fmt.Errorf("Task %s/%s is not an AI Task (spec.type != ai)", namespace, opt.Task)
	}
	// Re-read with the pinned identity rather than trusting an old phase
	// observation. A replaced Task cannot lend its result to this invocation.
	statusStarted := false
	var lastPhase string
	reportedPhase := false
	for {
		task, err := worker.readOrkaObject(ctx, namespace, id)
		if err != nil {
			if follow {
				return a.runTaskWaitError(ctx, err, opt.Task, a.Cfg.KubeContext, namespace)
			}
			return err
		}
		if !reportedPhase || task.Status.Phase != lastPhase {
			worker.notef("Task %s phase: %s", opt.Task, task.Status.Phase)
			lastPhase, reportedPhase = task.Status.Phase, true
		}
		switch task.Status.Phase {
		case "Failed", "Cancelled":
			return &orkaTaskEndedError{Phase: task.Status.Phase}
		case "Succeeded":
			if task.Status.ResultRef.Available {
				return worker.taskResultRead(ctx, namespace, id, port, follow)
			}
		}
		if !follow {
			return a.pendingTask(opt.Task, a.Cfg.KubeContext, namespace)
		}
		if !statusStarted {
			var stopStatus context.CancelFunc
			ctx, stopStatus = worker.waitContext(ctx, "task-result-status", 0)
			defer stopStatus()
			statusStarted = true
		}
		if err := worker.pause(ctx, time.Second); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return a.pendingTask(opt.Task, a.Cfg.KubeContext, namespace)
			}
			return fmt.Errorf("waiting for Task %s/%s: %w", namespace, opt.Task, err)
		}
	}
}

func decodeTaskResultIdentity(raw []byte, name, namespace string) (orkaIdentity, string, error) {
	var object struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name, Namespace, UID string
			Generation           int64
			DeletionTimestamp    *time.Time `json:"deletionTimestamp"`
		} `json:"metadata"`
		Spec struct {
			Type string `json:"type"`
		} `json:"spec"`
	}
	if !agentruntime.JSONKeysUnique(raw) || json.Unmarshal(raw, &object) != nil || object.APIVersion != "core.orka.ai/v1alpha1" || object.Kind != "Task" || object.Metadata.Name != name || object.Metadata.Namespace != namespace || object.Metadata.UID == "" || object.Metadata.Generation < 1 || object.Metadata.DeletionTimestamp != nil {
		return orkaIdentity{}, "", fmt.Errorf("Task %s/%s has invalid or terminating identity", namespace, name)
	}
	return orkaIdentity{Kind: "Task", Name: name, UID: object.Metadata.UID, Generation: object.Metadata.Generation, strictAPIVersion: object.APIVersion}, object.Spec.Type, nil
}

func (a *App) taskResultRead(ctx context.Context, namespace string, id orkaIdentity, port string, follow bool) error {
	session, err := a.openOrkaResultSession(ctx, CreateOptions{Namespace: namespace, ResultServiceAccount: orkaResultAccount, ResultPort: port})
	if err != nil {
		if follow {
			return a.runTaskWaitError(ctx, err, id.Name, a.Cfg.KubeContext, namespace)
		}
		return err
	}
	defer session.close()
	answer, err := a.readOrkaTaskResult(session.ctx, namespace, id, session, nil, follow)
	if err != nil {
		if errors.Is(err, ErrTaskPending) {
			return a.pendingTask(id.Name, a.Cfg.KubeContext, namespace)
		}
		if follow {
			return a.runTaskWaitError(ctx, err, id.Name, a.Cfg.KubeContext, namespace)
		}
		return err
	}
	if strings.TrimSpace(answer) == "" {
		return fmt.Errorf("Task has no printable answer")
	}
	if err := session.close(); err != nil {
		return fmt.Errorf("result session cleanup failed; answer withheld: %w", err)
	}
	_, err = fmt.Fprintln(a.Out, answer)
	return err
}
