package agentkit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestBuildxExporterWritesOCIArchive(t *testing.T) {
	runner := &fakeBuildxRunner{stdout: "oci archive"}
	exporter := buildxExporter{runner: runner}
	var output bytes.Buffer
	err := exporter.ExportOCI(t.Context(), agentImage{
		AgentkitFile: []byte("#syntax=frontend\n{}"),
		Name:         "writer",
		AdapterRef:   "registry.example/harness@" + testDigest,
		Platform:     "linux/amd64",
		SourceEpoch:  1790388400,
	}, &output)
	if err != nil {
		t.Fatalf("ExportOCI() error = %v", err)
	}
	if output.String() != "oci archive" || runner.name != "docker" {
		t.Fatalf("output=%q command=%q", output.String(), runner.name)
	}
	command := strings.Join(runner.args, " ")
	for _, want := range []string{
		"buildx build",
		"--platform linux/amd64",
		"--build-arg adapter=registry.example/harness@" + testDigest,
		"--build-arg SOURCE_DATE_EPOCH=1790388400",
		"--output type=oci,dest=-,rewrite-timestamp=true",
		"--provenance=false",
		"--progress quiet",
		"--tag writer:latest",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command = %q, want %q", command, want)
		}
	}
	if runner.agentkitFile != "#syntax=frontend\n{}" {
		t.Fatalf("AgentKit file = %q", runner.agentkitFile)
	}
	if _, err := os.Stat(runner.workDir); !os.IsNotExist(err) {
		t.Fatalf("temporary build directory still exists: %v", err)
	}
}

func TestBuildxExporterReportsDockerErrors(t *testing.T) {
	runner := &fakeBuildxRunner{
		stderr: "pull access denied for private.example/harness",
		err:    errors.New("exit status 1"),
	}
	err := (buildxExporter{runner: runner}).ExportOCI(t.Context(), agentImage{
		AgentkitFile: []byte("{}"), Name: "writer", AdapterRef: "private.example/harness@" + testDigest,
		Platform: "linux/amd64", SourceEpoch: 1,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("ExportOCI() error = %v", err)
	}
}

func TestBuildxExporterRequiresDockerBuildx(t *testing.T) {
	runner := &fakeBuildxRunner{
		versionStderr: "docker: 'buildx' is not a docker command",
		versionErr:    errors.New("exit status 1"),
	}
	err := (buildxExporter{runner: runner}).ExportOCI(t.Context(), agentImage{}, io.Discard)
	if err == nil ||
		!strings.Contains(err.Error(), "Docker with the buildx plugin is required") ||
		!strings.Contains(err.Error(), "not a docker command") {
		t.Fatalf("ExportOCI() error = %v", err)
	}
	if runner.workDir != "" {
		t.Fatalf("temporary build directory created before prerequisite check: %q", runner.workDir)
	}
}

func TestBuildxExporterStreamsVerboseProgress(t *testing.T) {
	runner := &fakeBuildxRunner{stderr: "build progress"}
	var progress bytes.Buffer
	err := (buildxExporter{runner: runner, verbose: true, progress: &progress}).ExportOCI(
		t.Context(),
		agentImage{
			AgentkitFile: []byte("{}"), Name: "writer",
			AdapterRef: "registry.example/harness@" + testDigest,
			Platform:   "linux/amd64", SourceEpoch: 1,
		},
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if progress.String() != "build progress" ||
		!strings.Contains(strings.Join(runner.args, " "), "--progress plain") {
		t.Fatalf("progress=%q args=%v", progress.String(), runner.args)
	}
}

func TestBuildxExporterReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runner := &fakeBuildxRunner{err: context.Canceled}
	err := (buildxExporter{runner: runner}).ExportOCI(ctx, agentImage{
		AgentkitFile: []byte("{}"), Name: "writer",
		AdapterRef: "registry.example/harness@" + testDigest,
		Platform:   "linux/amd64", SourceEpoch: 1,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("ExportOCI() error = %v", err)
	}
}

type fakeBuildxRunner struct {
	name          string
	args          []string
	stdout        string
	stderr        string
	err           error
	versionErr    error
	versionStderr string
	workDir       string
	agentkitFile  string
}

func (r *fakeBuildxRunner) Run(
	_ context.Context,
	stdout io.Writer,
	stderr io.Writer,
	name string,
	args ...string,
) error {
	r.name = name
	r.args = append([]string(nil), args...)
	if len(args) == 2 && args[0] == "buildx" && args[1] == "version" {
		_, _ = io.WriteString(stderr, r.versionStderr)
		return r.versionErr
	}
	r.workDir = args[len(args)-1]
	data, err := os.ReadFile(r.workDir + "/agentkitfile.yaml")
	if err != nil {
		return err
	}
	r.agentkitFile = string(data)
	_, _ = io.WriteString(stdout, r.stdout)
	_, _ = io.WriteString(stderr, r.stderr)
	return r.err
}
