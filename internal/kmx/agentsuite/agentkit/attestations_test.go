package agentkit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

const unsupportedDriver = "ERROR: failed to build: Attestation is not supported for the docker driver.\nSwitch to a different driver, or turn on the containerd image store, and try again.\n"

type buildxAttempt struct {
	stdout, stderr string
	stderrChunks   []string
	err            error
}
type sequenceBuildxRunner struct {
	attempts []buildxAttempt
	builds   [][]string
}

func (r *sequenceBuildxRunner) Run(_ context.Context, stdout, stderr io.Writer, _ string, args ...string) error {
	if len(args) == 2 && args[1] == "version" {
		return nil
	}
	r.builds = append(r.builds, append([]string(nil), args...))
	a := r.attempts[len(r.builds)-1]
	if _, err := io.WriteString(stdout, a.stdout); err != nil {
		return err
	}
	if _, err := io.WriteString(stderr, a.stderr); err != nil {
		return err
	}
	for _, chunk := range a.stderrChunks {
		if _, err := io.WriteString(stderr, chunk); err != nil {
			return err
		}
	}
	return a.err
}

func TestBuildxAttestationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		disabled, required, verbose bool
		first                       buildxAttempt
		retry                       bool
		wantErr                     string
	}{
		{name: "default", first: buildxAttempt{stdout: "archive"}},
		{name: "opt out", disabled: true, first: buildxAttempt{stdout: "archive"}},
		{name: "unsupported driver", first: buildxAttempt{stderr: unsupportedDriver, err: errors.New("exit status 1")}, retry: true},
		{name: "unsupported daemon", first: buildxAttempt{stderr: "ERROR: failed to build: Attestations are not supported by the current BuildKit daemon\n", err: errors.New("exit status 1")}, retry: true},
		{name: "documented daemon URL", first: buildxAttempt{stderr: "ERROR: failed to build: Attestations are not supported by the current BuildKit daemon (https://docs.docker.com/go/attestations/)\n", err: errors.New("exit status 1")}, retry: true},
		{name: "incomplete driver rejection", first: buildxAttempt{stderr: "ERROR: failed to build: Attestation is not supported for the docker driver.\n", err: errors.New("exit status 1")}, wantErr: "Attestation is not supported"},
		{name: "scanner lookalike", first: buildxAttempt{stderr: "scanner: ERROR: failed to build: Attestations are not supported by the current BuildKit daemon\n", err: errors.New("exit status 1")}, wantErr: "scanner"},
		{name: "deadline exceeded", first: buildxAttempt{stderr: unsupportedDriver, err: context.DeadlineExceeded}, wantErr: "deadline exceeded"},
		{name: "verbose rejection", verbose: true, first: buildxAttempt{stderr: unsupportedDriver, err: errors.New("exit status 1")}, retry: true},
		{name: "required", required: true, first: buildxAttempt{stderr: unsupportedDriver, err: errors.New("exit status 1")}, wantErr: "--require-attestations"},
		{name: "partial stdout", first: buildxAttempt{stdout: "partial", stderr: unsupportedDriver, err: errors.New("exit status 1")}, wantErr: "Attestation is not supported"},
		{name: "canceled", first: buildxAttempt{stderr: unsupportedDriver, err: context.Canceled}, wantErr: "canceled"},
		{name: "generic capability words", first: buildxAttempt{stderr: "scanner: attestations unsupported", err: errors.New("exit status 1")}, wantErr: "scanner"},
		{name: "network", first: buildxAttempt{stderr: "failed to fetch: network timeout", err: errors.New("exit status 1")}, wantErr: "network timeout"},
		{name: "authentication", first: buildxAttempt{stderr: "pull access denied", err: errors.New("exit status 1")}, wantErr: "pull access denied"},
		{name: "scanner", first: buildxAttempt{stderr: "SBOM scanner exited with code 1", err: errors.New("exit status 1")}, wantErr: "scanner"},
		{name: "verbose diagnostics", verbose: true, first: buildxAttempt{stderr: "pull access denied", err: errors.New("exit status 1")}, wantErr: "pull access denied"},
		{name: "OCI exporter", disabled: true, first: buildxAttempt{stderr: "ERROR: failed to build: OCI exporter is not supported for the docker driver.\nSwitch to a different driver, or turn on the containerd image store, and try again.\n", err: errors.New("exit status 1")}, wantErr: "--builder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &sequenceBuildxRunner{attempts: []buildxAttempt{tc.first, {stdout: "archive"}}}
			var out, progress bytes.Buffer
			result, err := (buildxExporter{runner: r, builder: "selected", disableAttestations: tc.disabled, requireAttestations: tc.required, verbose: tc.verbose, progress: &progress}).ExportOCI(t.Context(), agentImage{Name: "writer", Platform: "linux/amd64"}, &out)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error=%v, want %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			wantBuilds := 1
			if tc.retry {
				wantBuilds = 2
			}
			if len(r.builds) != wantBuilds {
				t.Fatalf("build attempts=%d, want %d", len(r.builds), wantBuilds)
			}
			for i, args := range r.builds {
				command := strings.Join(args, " ")
				for _, want := range []string{"--builder selected", "--output type=oci,dest=-,rewrite-timestamp=true"} {
					if !strings.Contains(command, want) {
						t.Errorf("command %q missing %q", command, want)
					}
				}
				disabled := tc.disabled || i == 1
				sbom, prov := "--sbom=true", "--provenance=mode=max"
				if disabled {
					sbom, prov = "--sbom=false", "--provenance=false"
				}
				if !strings.Contains(command, sbom) || !strings.Contains(command, prov) || strings.Contains(command, "--push") {
					t.Errorf("command=%q", command)
				}
			}
			if tc.retry {
				if out.String() != "archive" || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "neither SBOM nor provenance") {
					t.Fatalf("output=%q result=%+v", out.String(), result)
				}
			}
			if tc.verbose && progress.String() != tc.first.stderr {
				t.Fatalf("progress=%q", progress.String())
			}
		})
	}
}

func TestBuildxAttestationDiagnosticTail(t *testing.T) {
	progress := strings.Repeat("#1 build progress\n", 32*1024)
	const unrelated = "ERROR: failed to build: network timeout\n"
	for _, tc := range []struct {
		name     string
		first    buildxAttempt
		required bool
		retry    bool
		wantErr  string
	}{
		{name: "final multiline rejection", first: buildxAttempt{stderr: progress + unsupportedDriver}, retry: true},
		{name: "rejection across writes", first: buildxAttempt{stderrChunks: []string{progress, unsupportedDriver[:70], unsupportedDriver[70:]}}, retry: true},
		{name: "final required rejection", first: buildxAttempt{stderr: progress + unsupportedDriver}, required: true, wantErr: "--require-attestations"},
		{name: "unrelated final error", first: buildxAttempt{stderr: progress + unrelated}, wantErr: "network timeout"},
		{name: "rejection lost from tail", first: buildxAttempt{stderr: unsupportedDriver + progress + unrelated}, wantErr: "network timeout"},
		{name: "incomplete rejection at cutoff", first: buildxAttempt{stderr: unsupportedDriver + strings.Repeat("x", testBuildxDiagnosticLimit-len(unsupportedDriver)+1)}, wantErr: "docker buildx build"},
		{name: "scanner prefix lost at cutoff", first: buildxAttempt{stderr: "scanner: " + unsupportedDriver + strings.Repeat("x", testBuildxDiagnosticLimit-len(unsupportedDriver))}, wantErr: "docker buildx build"},
		{name: "complete line at cutoff", first: buildxAttempt{stderr: progress + unsupportedDriver + strings.Repeat("x", testBuildxDiagnosticLimit-len(unsupportedDriver))}, retry: true},
		{name: "complete line after full replacement", first: buildxAttempt{stderr: progress, stderrChunks: []string{unsupportedDriver + strings.Repeat("x", testBuildxDiagnosticLimit-len(unsupportedDriver))}}, retry: true},
		{name: "scanner prefix lost after full replacement", first: buildxAttempt{stderr: "scanner: ", stderrChunks: []string{unsupportedDriver + strings.Repeat("x", testBuildxDiagnosticLimit-len(unsupportedDriver))}}, wantErr: "docker buildx build"},
		{name: "scanner prefix lost across small writes", first: buildxAttempt{stderr: "scanner: " + unsupportedDriver + strings.Repeat("x", testBuildxDiagnosticLimit-len(unsupportedDriver)-len("scanner: ")), stderrChunks: []string{"xxxxxxxxx"}}, wantErr: "docker buildx build"},
		{name: "complete line across small writes", first: buildxAttempt{stderr: "prefix\n" + unsupportedDriver + strings.Repeat("x", testBuildxDiagnosticLimit-len(unsupportedDriver)-len("prefix\n")), stderrChunks: []string{"xxxxxxx"}}, retry: true},
		{name: "partial stdout after large progress", first: buildxAttempt{stdout: "partial", stderr: progress + unsupportedDriver}, wantErr: "Attestation is not supported"},
		{name: "canceled after large progress", first: buildxAttempt{stderr: progress + unsupportedDriver, err: context.Canceled}, wantErr: "canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.first.err == nil {
				tc.first.err = errors.New("exit status 1")
			}
			runner := &sequenceBuildxRunner{attempts: []buildxAttempt{tc.first, {stdout: "archive"}}}
			var output, streamed bytes.Buffer
			result, err := (buildxExporter{runner: runner, requireAttestations: tc.required, verbose: true, progress: &streamed}).ExportOCI(
				t.Context(), agentImage{}, &output,
			)
			wantBuilds := 1
			if tc.retry {
				wantBuilds = 2
				if err != nil || result.AttestationsRequested || output.String() != "archive" || len(result.Warnings) != 1 {
					t.Errorf("result=%+v error=%v output=%q, want un-attested retry archive", result, err, output.String())
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error=%v, want %q", err, tc.wantErr)
			}
			if len(runner.builds) != wantBuilds {
				t.Errorf("build attempts=%d, want %d", len(runner.builds), wantBuilds)
			}
			if want := tc.first.stderr + strings.Join(tc.first.stderrChunks, ""); streamed.String() != want {
				t.Errorf("streamed progress length=%d, want %d", streamed.Len(), len(want))
			}
			if err != nil && tc.wantErr != "canceled" && !strings.Contains(err.Error(), "earlier diagnostics truncated") {
				t.Error("error omits diagnostic truncation notice")
			}
		})
	}
}

func TestBuildxDiagnosticTailResetBetweenAttempts(t *testing.T) {
	runner := &sequenceBuildxRunner{attempts: []buildxAttempt{
		{stderr: strings.Repeat("#1 progress\n", 32*1024) + unsupportedDriver, err: errors.New("exit status 1")},
		{stderr: "pull access denied", err: errors.New("exit status 1")},
	}}
	_, err := (buildxExporter{runner: runner}).ExportOCI(t.Context(), agentImage{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("error=%v, want retry failure", err)
	}
	if strings.Contains(err.Error(), "earlier diagnostics truncated") || strings.Contains(err.Error(), "Attestation is not supported") {
		t.Fatal("retry failure retains earlier attempt diagnostics or truncation state")
	}
	if len(runner.builds) != 2 {
		t.Fatalf("build attempts=%d, want 2", len(runner.builds))
	}
}

func TestBuildxOCIExporterDiagnosticTail(t *testing.T) {
	runner := &sequenceBuildxRunner{attempts: []buildxAttempt{{
		stderr: strings.Repeat("#1 progress\n", 32*1024) + "ERROR: failed to build: OCI exporter is not supported for the docker driver.\n",
		err:    errors.New("exit status 1"),
	}}}
	_, err := (buildxExporter{runner: runner}).ExportOCI(t.Context(), agentImage{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "OCI-export-capable --builder") || !strings.Contains(err.Error(), "earlier diagnostics truncated") {
		t.Fatalf("error=%v, want truncated diagnostics and OCI builder guidance", err)
	}
	if len(runner.builds) != 1 {
		t.Fatalf("build attempts=%d, want 1", len(runner.builds))
	}
}

func TestBuildxPreservesProgressWriteErrorsAfterLargeDiagnostics(t *testing.T) {
	progressErr := errors.New("progress device closed")
	for _, tc := range []struct {
		name   string
		writer io.Writer
		want   error
	}{
		{name: "failure", writer: progressErrorWriter{err: progressErr}, want: progressErr},
		{name: "short write", writer: shortProgressWriter{}, want: io.ErrShortWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &sequenceBuildxRunner{attempts: []buildxAttempt{{
				stderr: strings.Repeat("#1 progress\n", 32*1024) + unsupportedDriver,
				err:    errors.New("exit status 1"),
			}}}
			_, err := (buildxExporter{runner: runner, verbose: true, progress: tc.writer}).ExportOCI(t.Context(), agentImage{}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "write Docker buildx progress") || !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want progress write error %v", err, tc.want)
			}
			if len(runner.builds) != 1 {
				t.Fatalf("build attempts=%d, want 1", len(runner.builds))
			}
		})
	}
}

type progressErrorWriter struct{ err error }

func (w progressErrorWriter) Write([]byte) (int, error) { return 0, w.err }

type shortProgressWriter struct{}

func (shortProgressWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

type failingArchiveWriter struct{}

func (failingArchiveWriter) Write([]byte) (int, error) { return 0, errors.New("output write failed") }
func TestBuildxDoesNotRetryProgressWriteFailure(t *testing.T) {
	r := &sequenceBuildxRunner{attempts: []buildxAttempt{{stderr: unsupportedDriver, err: errors.New("exit status 1")}, {stdout: "archive"}}}
	_, err := (buildxExporter{runner: r, verbose: true, progress: failingArchiveWriter{}}).ExportOCI(t.Context(), agentImage{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "output write failed") || len(r.builds) != 1 {
		t.Fatalf("error=%v attempts=%d", err, len(r.builds))
	}
}

func TestBuildxDoesNotRetryOutputWriteFailure(t *testing.T) {
	r := &sequenceBuildxRunner{attempts: []buildxAttempt{{stdout: "archive", stderr: unsupportedDriver, err: errors.New("exit status 1")}, {stdout: "archive"}}}
	_, err := (buildxExporter{runner: r}).ExportOCI(t.Context(), agentImage{}, failingArchiveWriter{})
	if err == nil || !strings.Contains(err.Error(), "output write failed") || len(r.builds) != 1 {
		t.Fatalf("error=%v attempts=%d", err, len(r.builds))
	}
}
