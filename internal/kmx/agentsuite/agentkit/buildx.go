package agentkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

type exportResult struct {
	AttestationsRequested bool
	Warnings              []string
}

type ociExporter interface {
	ExportOCI(context.Context, agentImage, io.Writer) (exportResult, error)
}

type buildxExporter struct {
	runner              buildxRunner
	builder             string
	disableAttestations bool
	requireAttestations bool
	verbose             bool
	progress            io.Writer
}

type buildxRunner interface {
	Run(context.Context, io.Writer, io.Writer, string, ...string) error
}

// Buildx v0.37.1 build/opt.go and build/utils.go report these explicit
// capability rejections. Unknown diagnostics must never trigger a downgrade.
// https://github.com/docker/buildx/blob/v0.37.1/build/opt.go
// https://github.com/docker/buildx/blob/v0.37.1/build/utils.go
var unsupportedAttestations = regexp.MustCompile(`(?m)^ERROR: failed to build: (?:Attestations are not supported by the current BuildKit daemon|Attestation is not supported for the [a-z][a-z0-9-]* driver\.\r?\nSwitch to a different driver, or turn on the containerd image store, and try again\.)(?: \(https://docs\.docker\.com/go/attestations/\))?\r?$`)
var unsupportedOCIExporter = regexp.MustCompile(`(?m)^ERROR: failed to build: OCI exporter is not supported for the [a-z][a-z0-9-]* driver\.\r?$`)

func (e buildxExporter) ExportOCI(ctx context.Context, image agentImage, dst io.Writer) (exportResult, error) {
	result := exportResult{AttestationsRequested: !e.disableAttestations}
	runner := e.runner
	if runner == nil {
		runner = localBuildxRunner{}
	}
	var diagnostics bytes.Buffer
	if err := runner.Run(ctx, io.Discard, &diagnostics, "docker", "buildx", "version"); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, fmt.Errorf("check Docker buildx availability: %w", ctxErr)
		}
		return result, fmt.Errorf("Docker with the buildx plugin is required: %w\n%s", err, strings.TrimSpace(diagnostics.String()))
	}
	workDir, err := os.MkdirTemp("", "kmx-agentkit-build-*")
	if err != nil {
		return result, fmt.Errorf("create AgentKit build directory: %w", err)
	}
	defer os.RemoveAll(workDir)
	agentkitFile := filepath.Join(workDir, "agentkitfile.yaml")
	if err := os.WriteFile(agentkitFile, image.AgentkitFile, 0o600); err != nil {
		return result, fmt.Errorf("write AgentKit build input: %w", err)
	}
	progressMode := "quiet"
	if e.verbose {
		progressMode = "plain"
	}
	args := []string{
		"buildx", "build",
		"--file", agentkitFile,
		"--platform", image.Platform,
		"--build-arg", "adapter=" + image.AdapterRef,
		"--build-arg", "SOURCE_DATE_EPOCH=" + strconv.FormatInt(image.SourceEpoch, 10),
		"--output", "type=oci,dest=-,rewrite-timestamp=true",
		"--progress", progressMode,
		"--tag", image.Name + ":latest",
	}
	if e.builder != "" {
		args = append(args, "--builder", e.builder)
	}
	for attempt := 0; attempt < 2; attempt++ {
		diagnostics.Reset()
		stderr := io.Writer(&diagnostics)
		progress := &archiveWriter{dst: e.progress}
		if e.verbose && e.progress != nil {
			stderr = io.MultiWriter(&diagnostics, progress)
		}
		attestationArgs := []string{"--sbom=true", "--provenance=mode=max"}
		if !result.AttestationsRequested {
			attestationArgs = []string{"--sbom=false", "--provenance=false"}
		}
		command := append([]string(nil), args...)
		command = append(command, attestationArgs...)
		command = append(command, workDir)
		output := &archiveWriter{dst: dst}
		err = runner.Run(ctx, output, stderr, "docker", command...)
		if output.err != nil {
			return result, fmt.Errorf("write OCI archive: %w", output.err)
		}
		if progress.err != nil {
			return result, fmt.Errorf("write Docker buildx progress: %w", progress.err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, fmt.Errorf("docker buildx build canceled: %w", ctxErr)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return result, fmt.Errorf("docker buildx build canceled: %w", err)
		}
		if err == nil {
			return result, nil
		}
		detail := strings.TrimSpace(diagnostics.String())
		if result.AttestationsRequested && output.written == 0 && unsupportedAttestations.MatchString(detail) {
			if e.requireAttestations {
				return result, fmt.Errorf("--require-attestations: selected builder does not support attestations; select an attestation-capable --builder: %w\n%s", err, detail)
			}
			result.AttestationsRequested = false
			result.Warnings = append(result.Warnings, "selected builder does not support attestations; output has neither SBOM nor provenance; select an attestation-capable --builder")
			continue
		}
		if unsupportedOCIExporter.MatchString(detail) {
			detail += "\nSelect an OCI-export-capable --builder (for example, a docker-container builder); disabling attestations does not enable OCI export with the classic Docker image store."
		}
		return result, fmt.Errorf("docker buildx build: %w\n%s", err, detail)
	}
	return result, fmt.Errorf("docker buildx build: %w", err)
}

// A failed output write is not a capability failure, even if it wrote no bytes.
type archiveWriter struct {
	dst     io.Writer
	written int64
	err     error
}

func (w *archiveWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	w.written += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
	}
	return n, err
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
