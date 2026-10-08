package agentkit

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestManagedBuildkitReusesRunningContainer(t *testing.T) {
	runner := &fakeManagedBuildkitRunner{captureOutput: "true|true|" + managedBuildkitImage}
	if err := (managedBuildkitManager{runner: runner}).Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("commands = %v", runner.commands)
	}
}

func TestManagedBuildkitStartsStoppedContainer(t *testing.T) {
	runner := &fakeManagedBuildkitRunner{captureOutput: "false|true|" + managedBuildkitImage}
	if err := (managedBuildkitManager{runner: runner}).Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	want := [][]string{{"docker", "container", "start", managedBuildkitContainer}}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands = %v, want %v", runner.commands, want)
	}
}

func TestManagedBuildkitCreatesMissingContainer(t *testing.T) {
	runner := &fakeManagedBuildkitRunner{captureErr: errors.New("No such object: " + managedBuildkitContainer)}
	if err := (managedBuildkitManager{runner: runner}).Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %v", runner.commands)
	}
	got := strings.Join(runner.commands[0], " ")
	for _, want := range []string{
		"docker run -d",
		"--name " + managedBuildkitContainer,
		"--label " + managedBuildkitLabel + "=true",
		"--restart unless-stopped",
		"--privileged",
		managedBuildkitImage,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("command = %q, want %q", got, want)
		}
	}
}

func TestManagedBuildkitRefusesUnmanagedContainer(t *testing.T) {
	runner := &fakeManagedBuildkitRunner{captureOutput: "true|<no value>|example.invalid/buildkit:latest"}
	err := (managedBuildkitManager{runner: runner}).Ensure(t.Context())
	if err == nil || !strings.Contains(err.Error(), "not managed by KMX") {
		t.Fatalf("Ensure() error = %v", err)
	}
}

func TestManagedBuildkitAdoptsDocumentedLegacyContainer(t *testing.T) {
	runner := &fakeManagedBuildkitRunner{captureOutput: "true|<no value>|" + managedBuildkitLegacyImage}
	if err := (managedBuildkitManager{runner: runner}).Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
}

func TestManagedBuildkitReportsDockerFailures(t *testing.T) {
	tests := []struct {
		name   string
		runner *fakeManagedBuildkitRunner
		want   string
	}{
		{
			name:   "unexpected inspect output",
			runner: &fakeManagedBuildkitRunner{captureOutput: "true"},
			want:   "unexpected state",
		},
		{
			name:   "inspect failure",
			runner: &fakeManagedBuildkitRunner{captureErr: errors.New("Docker daemon unavailable")},
			want:   "inspect container",
		},
		{
			name: "start failure",
			runner: &fakeManagedBuildkitRunner{
				captureOutput: "false|true|" + managedBuildkitImage,
				runErr:        errors.New("start failed"),
			},
			want: "start container",
		},
		{
			name: "create failure",
			runner: &fakeManagedBuildkitRunner{
				captureErr: errors.New("No such container"),
				runErr:     errors.New("create failed"),
			},
			want: "create container",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (managedBuildkitManager{runner: test.runner}).Ensure(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Ensure() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestManagedBuildkitAdoptsConcurrentCreateWinner(t *testing.T) {
	runner := &fakeManagedBuildkitRunner{
		captureOutputs: []string{"", "true|true|" + managedBuildkitImage},
		captureErrs:    []error{errors.New("No such container"), nil},
		runErr:         errors.New("container name is already in use"),
	}
	if err := (managedBuildkitManager{runner: runner}).Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if runner.captureCalls != 2 || len(runner.commands) != 1 {
		t.Fatalf("capture calls=%d commands=%v", runner.captureCalls, runner.commands)
	}
}

func TestManagedBuildkitVerboseCreationProgress(t *testing.T) {
	runner := &fakeManagedBuildkitRunner{captureErr: errors.New("No such container")}
	var diagnostics bytes.Buffer
	manager := managedBuildkitManager{
		runner:      runner,
		verbose:     true,
		diagnostics: &diagnostics,
	}
	if err := manager.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if !strings.Contains(diagnostics.String(), "creating managed daemon kmx-buildkitd") {
		t.Fatalf("diagnostics = %q", diagnostics.String())
	}
}

func TestManagedBuildkitVerboseExistingContainerProgress(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  []string
	}{
		{
			name:  "running",
			state: "true|true|" + managedBuildkitImage,
			want:  []string{"found managed daemon kmx-buildkitd"},
		},
		{
			name:  "stopped",
			state: "false|true|" + managedBuildkitImage,
			want:  []string{"found managed daemon kmx-buildkitd", "starting managed daemon kmx-buildkitd"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			manager := managedBuildkitManager{
				runner:      &fakeManagedBuildkitRunner{captureOutput: test.state},
				verbose:     true,
				diagnostics: &diagnostics,
			}
			if err := manager.Ensure(t.Context()); err != nil {
				t.Fatalf("Ensure() error = %v", err)
			}
			for _, want := range test.want {
				if !strings.Contains(diagnostics.String(), want) {
					t.Fatalf("diagnostics = %q, want %q", diagnostics.String(), want)
				}
			}
		})
	}
}

func TestManagedBuildkitProgressWriteFailure(t *testing.T) {
	manager := managedBuildkitManager{
		runner:      &fakeManagedBuildkitRunner{captureOutput: "true|true|" + managedBuildkitImage},
		verbose:     true,
		diagnostics: failingWriter{},
	}
	err := manager.Ensure(t.Context())
	if err == nil || !strings.Contains(err.Error(), "write managed BuildKit progress") {
		t.Fatalf("Ensure() error = %v", err)
	}
}

func TestLocalManagedBuildkitRunnerExecutesCommands(t *testing.T) {
	runner := localManagedBuildkitRunner{}
	output, err := runner.Capture(t.Context(), "go", "version")
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if !strings.HasPrefix(output, "go version ") {
		t.Fatalf("Capture() output = %q", output)
	}
	if err := runner.Run(t.Context(), "go", "version"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

type fakeManagedBuildkitRunner struct {
	captureOutput  string
	captureErr     error
	captureOutputs []string
	captureErrs    []error
	captureCalls   int
	commands       [][]string
	runErr         error
}

func (f *fakeManagedBuildkitRunner) Capture(context.Context, string, ...string) (string, error) {
	if f.captureCalls < len(f.captureOutputs) || f.captureCalls < len(f.captureErrs) {
		index := f.captureCalls
		f.captureCalls++
		var output string
		if index < len(f.captureOutputs) {
			output = f.captureOutputs[index]
		}
		var err error
		if index < len(f.captureErrs) {
			err = f.captureErrs[index]
		}
		return output, err
	}
	f.captureCalls++
	return f.captureOutput, f.captureErr
}

func (f *fakeManagedBuildkitRunner) Run(_ context.Context, name string, args ...string) error {
	f.commands = append(f.commands, append([]string{name}, args...))
	return f.runErr
}
