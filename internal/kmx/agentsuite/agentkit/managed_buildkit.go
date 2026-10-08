package agentkit

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

const (
	managedBuildkitAddress     = "docker-container://kmx-buildkitd"
	managedBuildkitContainer   = "kmx-buildkitd"
	managedBuildkitImage       = "moby/buildkit@sha256:0168606be2315b7c807a03b3d8aa79beefdb31c98740cebdffdfeebf31190c9f"
	managedBuildkitLegacyImage = "moby/buildkit:v0.30.0"
	managedBuildkitLabel       = "dev.kaimahi.managed"
)

type buildkitManager interface {
	Ensure(context.Context) error
}

type managedBuildkitManager struct {
	runner      managedBuildkitRunner
	verbose     bool
	diagnostics io.Writer
}

type managedBuildkitRunner interface {
	Capture(context.Context, string, ...string) (string, error)
	Run(context.Context, string, ...string) error
}

func (m managedBuildkitManager) Ensure(ctx context.Context) error {
	runner := m.runner
	if runner == nil {
		runner = localManagedBuildkitRunner{}
	}
	state, err := inspectManagedBuildkit(ctx, runner)
	if err == nil {
		if err := m.progressf("BuildKit: found managed daemon %s\n", managedBuildkitContainer); err != nil {
			return err
		}
		return m.ensureRunning(ctx, runner, state)
	}
	if !strings.Contains(err.Error(), "No such object") && !strings.Contains(err.Error(), "No such container") {
		return fmt.Errorf("inspect container %s: %w", managedBuildkitContainer, err)
	}
	if err := m.progressf("BuildKit: creating managed daemon %s from %s\n",
		managedBuildkitContainer, managedBuildkitImage); err != nil {
		return err
	}
	if err := runner.Run(ctx, "docker", "run", "-d",
		"--name", managedBuildkitContainer,
		"--label", managedBuildkitLabel+"=true",
		"--restart", "unless-stopped",
		"--privileged",
		managedBuildkitImage,
	); err != nil {
		if state, inspectErr := inspectManagedBuildkit(ctx, runner); inspectErr == nil {
			return m.ensureRunning(ctx, runner, state)
		}
		return fmt.Errorf("create container %s: %w", managedBuildkitContainer, err)
	}
	return nil
}

func (m managedBuildkitManager) progressf(format string, args ...any) error {
	if !m.verbose || m.diagnostics == nil {
		return nil
	}
	if _, err := fmt.Fprintf(m.diagnostics, format, args...); err != nil {
		return fmt.Errorf("write managed BuildKit progress: %w", err)
	}
	return nil
}

func inspectManagedBuildkit(ctx context.Context, runner managedBuildkitRunner) (string, error) {
	return runner.Capture(ctx, "docker", "container", "inspect",
		"--format", `{{.State.Running}}|{{index .Config.Labels "`+managedBuildkitLabel+`"}}|{{.Config.Image}}`,
		managedBuildkitContainer)
}

func (m managedBuildkitManager) ensureRunning(ctx context.Context, runner managedBuildkitRunner, state string) error {
	fields := strings.Split(state, "|")
	if len(fields) != 3 {
		return fmt.Errorf("inspect container %s returned an unexpected state", managedBuildkitContainer)
	}
	managed := fields[1] == "true" ||
		(fields[1] == "" || fields[1] == "<no value>") &&
			(fields[2] == managedBuildkitImage || fields[2] == managedBuildkitLegacyImage)
	if !managed {
		return fmt.Errorf("container %s exists but is not managed by KMX", managedBuildkitContainer)
	}
	if fields[0] == "true" {
		return nil
	}
	if err := m.progressf("BuildKit: starting managed daemon %s\n", managedBuildkitContainer); err != nil {
		return err
	}
	if err := runner.Run(ctx, "docker", "container", "start", managedBuildkitContainer); err != nil {
		return fmt.Errorf("start container %s: %w", managedBuildkitContainer, err)
	}
	return nil
}

type localManagedBuildkitRunner struct{}

func (localManagedBuildkitRunner) Capture(ctx context.Context, name string, args ...string) (string, error) {
	return (&run.Runner{Context: ctx}).Capture(name, args...)
}

func (localManagedBuildkitRunner) Run(ctx context.Context, name string, args ...string) error {
	return (&run.Runner{Context: ctx, Stdout: io.Discard, Stderr: io.Discard}).Run(name, args...)
}
