package agentkit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type agentImage struct {
	AgentkitFile []byte
	Name         string
	AdapterRef   string
	Platform     string
	SourceEpoch  int64
}

type ociExporter interface {
	ExportOCI(context.Context, agentImage, io.Writer) error
}

type buildxExporter struct {
	runner   buildxRunner
	verbose  bool
	progress io.Writer
}

type buildxRunner interface {
	Run(context.Context, io.Writer, io.Writer, string, ...string) error
}

func (e buildxExporter) ExportOCI(ctx context.Context, image agentImage, dst io.Writer) error {
	runner := e.runner
	if runner == nil {
		runner = localBuildxRunner{}
	}
	var diagnostics bytes.Buffer
	if err := runner.Run(ctx, io.Discard, &diagnostics, "docker", "buildx", "version"); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("check Docker buildx availability: %w", ctxErr)
		}
		detail := strings.TrimSpace(diagnostics.String())
		if detail == "" {
			return fmt.Errorf("Docker with the buildx plugin is required: %w", err)
		}
		return fmt.Errorf("Docker with the buildx plugin is required: %w\n%s", err, detail)
	}

	workDir, err := os.MkdirTemp("", "kmx-agentkit-build-*")
	if err != nil {
		return fmt.Errorf("create AgentKit build directory: %w", err)
	}
	defer os.RemoveAll(workDir)

	agentkitFile := filepath.Join(workDir, "agentkitfile.yaml")
	if err := os.WriteFile(agentkitFile, image.AgentkitFile, 0o600); err != nil {
		return fmt.Errorf("write AgentKit build input: %w", err)
	}

	progressMode := "quiet"
	diagnostics.Reset()
	stderr := io.Writer(&diagnostics)
	if e.verbose {
		progressMode = "plain"
		if e.progress != nil {
			stderr = e.progress
		}
	}
	err = runner.Run(ctx, dst, stderr, "docker",
		"buildx", "build",
		"--file", agentkitFile,
		"--platform", image.Platform,
		"--build-arg", "adapter="+image.AdapterRef,
		"--build-arg", "SOURCE_DATE_EPOCH="+strconv.FormatInt(image.SourceEpoch, 10),
		"--output", "type=oci,dest=-,rewrite-timestamp=true",
		"--provenance=false",
		"--progress", progressMode,
		"--tag", image.Name+":latest",
		workDir,
	)
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("docker buildx build canceled: %w", ctxErr)
	}
	detail := strings.TrimSpace(diagnostics.String())
	if detail == "" {
		return fmt.Errorf("docker buildx build: %w", err)
	}
	return fmt.Errorf("docker buildx build: %w\n%s", err, detail)
}

type localBuildxRunner struct{}

func (localBuildxRunner) Run(
	ctx context.Context,
	stdout io.Writer,
	stderr io.Writer,
	name string,
	args ...string,
) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Cancel = func() error {
		return command.Process.Signal(os.Interrupt)
	}
	command.WaitDelay = 10 * time.Second
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}
