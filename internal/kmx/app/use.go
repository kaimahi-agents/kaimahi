package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The kagent namespace. Named here rather than reached for through config,
// because it is not a setting: it is where the chart puts everything. Still
// read by status and by certificate publication.
const config_kagentNamespace = "kagent"

// requireNamespace refuses before minting a one-time credential that cannot
// be stored in its destination namespace.
func (a *App) requireNamespace(namespace, flag string) error {
	if _, err := a.kubectlCapture("get", "namespace", namespace, "-o", "name"); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("namespace %q does not exist, and it is where the credential's Secret would go.\n"+
				"  Nothing has been issued — the token is shown once, so this is refused before it is minted.\n"+
				"  Name the namespace your runtime reads its Secret from:\n"+
				"    %s <your namespace>", namespace, flag)
		}
		return fmt.Errorf("cannot tell whether namespace %q exists (refusing to guess): %w", namespace, err)
	}
	return nil
}

// UsePreset switches a legacy Agent onto a ModelConfig and waits until that
// is TRUE of the running pods rather than of the object.
//
// TRANSITIONAL, like Govern above it: `kmx use` is retired, and the only
// remaining callers are the lift `agents` phase and the governance it runs.
// It goes with the legacy lift payload.
//
// This is the Makefile's `use` recipe and its `wait_switched` macro, carried
// across wait for wait. Each of those waits was added because something
// passed without it:
//
//   - The Agent's observedGeneration must catch up, or the rollout being
//     waited on below is the PREVIOUS one.
//   - A ModelConfig whose CONTENT changed while the agent was already on it
//     moves no Agent generation at all, and yet must roll the pods. kagent
//     cuts a new Deployment revision within a second; wait for the revision
//     to advance, bounded and loud, before believing the rollout wait.
//   - Exactly ONE pod must be on the new template. `rollout status` returns
//     while the old pod is still draining, and a chat that lands on it gets
//     a perfectly plausible answer from the OLD preset.
//   - Only then is the Agent's Ready condition meaningful.
//
// `apply` names the embedded manifests to apply as part of the switch. They
// are applied INSIDE the before/after window deliberately: the
// content-only-change case is detected by comparing the ModelConfig's
// generation across the apply, so applying it beforehand would make that
// comparison always read "unchanged" and skip the wait it exists to trigger.
func (a *App) UsePreset(agent, preset string, apply []string) error {
	// Three values from BEFORE the apply and the patch, because the waits
	// afterwards are all comparisons against them.
	presetGen, err := a.generation("modelconfig/" + preset)
	if err != nil {
		return err
	}
	agentGen, err := a.generation("agents.kagent.dev/" + agent)
	if err != nil {
		return err
	}
	if agentGen == "" {
		return fmt.Errorf("cannot read agent/%s's generation", agent)
	}
	revBefore, err := a.deploymentRevision(agent)
	if err != nil {
		return err
	}

	for _, name := range apply {
		if err := a.apply(name); err != nil {
			return err
		}
	}
	if err := a.patchModelConfig(agent, preset); err != nil {
		return err
	}

	presetGenAfter, err := a.generation("modelconfig/" + preset)
	if err != nil {
		return err
	}
	agentGenAfter, err := a.generation("agents.kagent.dev/" + agent)
	if err != nil {
		return err
	}
	if agentGenAfter == agentGen && presetGenAfter != presetGen {
		a.notef("NOTE: preset %q changed while %s was already on it — waiting for kagent to cut a new revision (was %s)",
			preset, agent, revBefore)
		// A failed READ aborts, as the shell's `|| exit 1` did. Polling
		// through it would spend 120s and then report a timeout, hiding the
		// actual reason — and "the API server went away" is not "kagent is
		// slow".
		var readErr error
		rolled := run.Poll(60, 2*time.Second, func() bool {
			rev, err := a.deploymentRevision(agent)
			if err != nil {
				readErr = err
				return true
			}
			return newer(rev, revBefore)
		})
		if readErr != nil {
			return readErr
		}
		if !rolled {
			return fmt.Errorf("deploy/%s: revision still %s after 120s — kagent did not roll for the changed preset; refusing to call it switched",
				agent, revBefore)
		}
	}

	if err := a.waitSwitched(agent); err != nil {
		return err
	}
	if err := a.waitAgentReady(agent); err != nil {
		return err
	}
	current, err := a.liveModelConfig(agent)
	if err != nil {
		return err
	}
	if current != preset {
		return fmt.Errorf("agent %s settled on modelConfig %q, not requested %q", agent, current, preset)
	}
	return nil
}

// waitSwitched is `wait_switched`: reconcile, rollout, and then exactly one
// pod on the new template.
func (a *App) waitSwitched(agent string) error {
	gen, err := a.generation("agents.kagent.dev/" + agent)
	if err != nil || gen == "" {
		return fmt.Errorf("cannot read agent/%s's generation", agent)
	}
	if err := a.kubectlRun("-n", config_kagentNamespace, "wait",
		"--for=jsonpath={.status.observedGeneration}="+gen, "agents.kagent.dev/"+agent, "--timeout=120s"); err != nil {
		return err
	}
	if err := a.kubectlRun("-n", config_kagentNamespace, "rollout", "status",
		"deploy/"+agent, "--timeout=180s"); err != nil {
		return err
	}

	rev, err := a.deploymentRevision(agent)
	if err != nil || rev == "" {
		return fmt.Errorf("cannot read deploy/%s's revision", agent)
	}
	hash, err := a.templateHash(agent, rev)
	if err != nil {
		return err
	}

	// `rollout status` is not enough: it returns while the old pod is still
	// draining, and an invoke that lands on it answers from the OLD preset.
	// Wait until the only pod carrying the agent's label is on the new
	// template.
	var last string
	var readErr error
	single := run.Poll(60, 2*time.Second, func() bool {
		pods, err := a.kubectlCapture("-n", config_kagentNamespace, "get", "pods", "-l", "kagent="+agent,
			"-o", `jsonpath={range .items[*]}{.metadata.labels.pod-template-hash}{"\n"}{end}`)
		if err != nil {
			// The shell aborted here too (`pods=$(...) || exit 1`).
			readErr = fmt.Errorf("cannot list deploy/%s's pods while waiting for the switch: %w", agent, err)
			return true
		}
		last = strings.TrimSpace(pods)
		return last == hash
	})
	if readErr != nil {
		return readErr
	}
	if !single {
		a.notef("deploy/%s: still not exactly one pod on template %s after 120s (saw %q):", agent, hash, last)
		_ = a.kubectlRun("-n", config_kagentNamespace, "get", "pods", "-l", "kagent="+agent, "-o", "wide")
		return fmt.Errorf("deploy/%s did not settle on one pod for template %s", agent, hash)
	}
	return nil
}

// generation reads an object's metadata.generation. A genuine NotFound is
// "no generation yet" (a preset that is about to be created); anything else
// aborts, because a read failure read as absence is what silently skips a
// wait.
func (a *App) generation(ref string) (string, error) {
	out, err := a.kubectlCapture("-n", config_kagentNamespace, "get", ref,
		"-o", "jsonpath={.metadata.generation}")
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("cannot read %s: %w", ref, err)
	}
	return strings.TrimSpace(out), nil
}

func (a *App) deploymentRevision(agent string) (string, error) {
	out, err := a.kubectlCapture("-n", config_kagentNamespace, "get", "deploy/"+agent,
		"-o", `jsonpath={.metadata.annotations.deployment\.kubernetes\.io/revision}`)
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("cannot read deploy/%s's revision: %w", agent, err)
	}
	return strings.TrimSpace(out), nil
}

// templateHash finds the pod-template-hash of the ReplicaSet at a revision —
// the identity of "the new pods".
func (a *App) templateHash(agent, revision string) (string, error) {
	out, err := a.kubectlCapture("-n", config_kagentNamespace, "get", "rs", "-l", "kagent="+agent,
		"-o", `jsonpath={range .items[*]}{.metadata.annotations.deployment\.kubernetes\.io/revision} {.metadata.labels.pod-template-hash}{"\n"}{end}`)
	if err != nil {
		return "", fmt.Errorf("cannot list ReplicaSets for deploy/%s: %w", agent, err)
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == revision {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("no ReplicaSet at revision %s for deploy/%s", revision, agent)
}

// newer compares Deployment revisions numerically, as the shell's `-gt` did.
// An unreadable revision is not newer — the wait keeps waiting rather than
// declaring success.
func newer(rev, before string) bool {
	if rev == "" {
		return false
	}
	got, err := strconv.Atoi(rev)
	if err != nil {
		return false
	}
	was := 0
	if before != "" {
		if n, err := strconv.Atoi(before); err == nil {
			was = n
		}
	}
	return got > was
}
