package app

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

const orkaHumanFieldLimit = 256

// preflightOrkaRemote keeps the local kubectl probe bounded, cancellable and
// private. Native Windows requires an operator-provided kubectl; existing Unix
// tool provisioning remains unchanged.
func (a *App) preflightOrkaRemote() error {
	ctx := a.operationContext()
	if err := ctx.Err(); err != nil {
		return orkaCancelledRequest(err)
	}
	if a.Run == nil {
		return fmt.Errorf("cannot inspect Orka: kubectl is unavailable or unusable; check the local prerequisite")
	}
	if err := a.provisionOrkaInspection(); err != nil {
		if cause := ctx.Err(); cause != nil {
			return orkaCancelledRequest(cause)
		}
		return fmt.Errorf("cannot inspect Orka: kubectl is unavailable or unusable; check the local prerequisite")
	}
	path, err := exec.LookPath(depKubectl.name)
	if err != nil {
		return fmt.Errorf("cannot inspect Orka: kubectl is unavailable or unusable; check the local prerequisite")
	}
	identity, err := inspectOrkaInspectionKubectl(ctx, path)
	if err != nil {
		return fmt.Errorf("cannot inspect Orka: kubectl is unavailable or unusable; check the local prerequisite")
	}

	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	runner := *a.Run
	runner.Context = callCtx
	freezeOrkaInspectionEnvironment(&runner)
	cmd := runner.Command(path, depKubectl.probe...)
	cmd.WaitDelay = time.Second
	out := &orkaBoundedBuffer{remaining: 64 << 10}
	diagnostics := &orkaBoundedBuffer{remaining: 4 << 10}
	cmd.Stdout, cmd.Stderr = out, diagnostics
	if err := cmd.Run(); err != nil || out.outputError() != nil || diagnostics.outputError() != nil {
		if cause := ctx.Err(); cause != nil {
			return orkaCancelledRequest(cause)
		}
		if callCtx.Err() != nil {
			return fmt.Errorf("kubectl request cancelled or timed out")
		}
		return fmt.Errorf("cannot inspect Orka: kubectl is unavailable or unusable; check the local prerequisite")
	}
	if err := recheckOrkaInspectionKubectl(ctx, path, identity); err != nil {
		return fmt.Errorf("cannot inspect Orka: kubectl changed during preflight")
	}
	a.inspectionKubectl, a.inspectionKubectlIdentity = path, identity
	return nil
}

func freezeOrkaInspectionEnvironment(runner *run.Runner) {
	runner.Unset = append(runner.Unset, "KUBERC", "KUBECTL_KUBERC")
	runner.Env = append(runner.Env, "KUBERC=off", "KUBECTL_KUBERC=false")
}

func (a *App) orkaInspectionCommand(ctx context.Context) (string, error) {
	if a.inspectionKubectl == "" {
		return depKubectl.name, nil
	}
	if err := recheckOrkaInspectionKubectl(ctx, a.inspectionKubectl, a.inspectionKubectlIdentity); err != nil {
		return "", fmt.Errorf("kubectl changed after inspection preflight")
	}
	return a.inspectionKubectl, nil
}

// orkaHumanText is the only projection for cluster-controlled human list/show
// fields. Machine output bypasses it and retains the original JSON/YAML values.
func orkaHumanText(value string, limit int) string {
	// Normalize C1 string/CSI controls into the ESC forms safeTerminal already
	// understands, then drop bidi overrides that can visually reorder a row.
	value = strings.NewReplacer(
		"\u0090", "\x1b]", "\u0098", "\x1b]", "\u009b", "\x1b[",
		"\u009c", "\a", "\u009d", "\x1b]", "\u009e", "\x1b]", "\u009f", "\x1b]",
	).Replace(value)
	value = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, safeTerminal(value))
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	const suffix = "…"
	end := limit - len(suffix)
	if end <= 0 {
		return ""
	}
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + suffix
}

func orkaHumanField(value string) string { return orkaHumanText(value, orkaHumanFieldLimit) }
