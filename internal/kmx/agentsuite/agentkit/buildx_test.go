package agentkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildxExporterWritesOCIArchive(t *testing.T) {
	runner := &fakeBuildxRunner{stdout: "oci archive"}
	exporter := buildxExporter{runner: runner}
	var output bytes.Buffer
	_, err := exporter.ExportOCI(t.Context(), agentImage{
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
		"--sbom=true",
		"--provenance=mode=max",
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
	_, err := (buildxExporter{runner: runner}).ExportOCI(t.Context(), agentImage{
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
	_, err := (buildxExporter{runner: runner}).ExportOCI(t.Context(), agentImage{}, io.Discard)
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
	_, err := (buildxExporter{runner: runner, verbose: true, progress: &progress}).ExportOCI(
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

const testBuildxDiagnosticLimit = 64 * 1024

func TestBuildxExporterBoundsFailureDiagnostics(t *testing.T) {
	const finalError = "ERROR: failed to build: pull access denied\n"
	log := "earlier diagnostics\n" + strings.Repeat("#1 build progress\n", 32*1024) + finalError
	wantTail := strings.TrimSpace(log[len(log)-testBuildxDiagnosticLimit:])
	for _, mode := range []string{"version", "quiet", "verbose"} {
		t.Run(mode, func(t *testing.T) {
			runner := &fakeBuildxRunner{stderr: log, err: errors.New("exit status 1")}
			if mode == "version" {
				runner.versionStderr, runner.versionErr = log, runner.err
			}
			var progress bytes.Buffer
			_, err := (buildxExporter{runner: runner, verbose: mode == "verbose", progress: &progress}).ExportOCI(
				t.Context(), agentImage{}, io.Discard,
			)
			if err == nil {
				t.Fatal("ExportOCI() succeeded, want build failure")
			}
			detail := err.Error()
			if !strings.Contains(detail, "earlier diagnostics truncated") {
				t.Errorf("failure diagnostics omit truncation notice (length %d)", len(detail))
			}
			if !strings.HasSuffix(detail, wantTail) || strings.Contains(detail, "earlier diagnostics\n") {
				t.Errorf("failure diagnostics did not retain only the exact final tail (length %d)", len(detail))
			}
			if len(detail) > testBuildxDiagnosticLimit+256 {
				t.Errorf("failure diagnostics length = %d, want bounded tail plus error context", len(detail))
			}
			if mode == "verbose" && progress.String() != log {
				t.Errorf("verbose progress length = %d, want all %d bytes", progress.Len(), len(log))
			}
			if mode != "verbose" && progress.Len() != 0 {
				t.Error("non-verbose exporter wrote progress")
			}
		})
	}
}

func TestBuildxDiagnosticTailWrites(t *testing.T) {
	quarter := testBuildxDiagnosticLimit / 4
	a, b := strings.Repeat("a", quarter), strings.Repeat("b", quarter)
	c, d := strings.Repeat("c", quarter), strings.Repeat("d", quarter)
	tail := a + b + c + d
	for _, tc := range []struct {
		name      string
		writes    []string
		want      string
		truncated bool
	}{
		{name: "empty", writes: []string{""}},
		{name: "partial", writes: []string{"first", " second"}, want: "first second"},
		{name: "exact limit", writes: []string{tail}, want: tail},
		{name: "single large write", writes: []string{strings.Repeat("discard", testBuildxDiagnosticLimit) + tail}, want: tail, truncated: true},
		{name: "multiple writes", writes: []string{a, b, c, d, "last"}, want: a[4:] + b + c + d + "last", truncated: true},
		{name: "full replacement", writes: []string{"discard", tail}, want: tail, truncated: true},
		{name: "chunked overflow", writes: []string{"discard", a, b, c, d}, want: tail, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var diagnostics buildxDiagnosticTail
			for _, text := range tc.writes {
				input := []byte(text)
				n, err := diagnostics.Write(input)
				if n != len(input) || err != nil {
					t.Fatalf("Write()=(%d, %v), want (%d, nil)", n, err, len(input))
				}
				// The tail must own its bytes, not retain the caller's large input.
				clear(input)
			}
			if got := diagnostics.String(); got != tc.want {
				t.Errorf("retained tail differs: length=%d, want %d", len(got), len(tc.want))
			}
			if diagnostics.size > testBuildxDiagnosticLimit || len(diagnostics.data) > testBuildxDiagnosticLimit {
				t.Error("retained diagnostic storage exceeds limit")
			}
			if got := strings.Contains(diagnostics.detail(), "earlier diagnostics truncated"); got != tc.truncated {
				t.Errorf("truncation notice=%v, want %v", got, tc.truncated)
			}
		})
	}
}

func TestBuildxDiagnosticTailDoesNotAllocateForLargeWrites(t *testing.T) {
	input := bytes.Repeat([]byte("progress\n"), testBuildxDiagnosticLimit)
	var diagnostics buildxDiagnosticTail
	if allocations := testing.AllocsPerRun(5, func() {
		diagnostics.Reset()
		_, _ = diagnostics.Write(input)
	}); allocations != 0 {
		t.Fatalf("large Write allocations=%v, want no input-sized temporary allocation", allocations)
	}
}

func TestBuildxDiagnosticTailReset(t *testing.T) {
	var diagnostics buildxDiagnosticTail
	_, _ = diagnostics.Write(bytes.Repeat([]byte("x"), testBuildxDiagnosticLimit+1))
	diagnostics.Reset()
	if diagnostics.String() != "" || diagnostics.detail() != "" || diagnostics.truncated || diagnostics.startsMidLine {
		t.Fatal("Reset() retains diagnostics or truncation state")
	}
	_, _ = diagnostics.Write([]byte("new attempt"))
	if diagnostics.String() != "new attempt" || diagnostics.detail() != "new attempt" {
		t.Fatal("Write() after Reset() retains earlier diagnostics")
	}
}

func TestBuildxExporterResetsVersionDiagnostics(t *testing.T) {
	runner := &fakeBuildxRunner{
		versionStderr: strings.Repeat("version output\n", 32*1024),
		stderr:        "pull access denied",
		err:           errors.New("exit status 1"),
	}
	_, err := (buildxExporter{runner: runner}).ExportOCI(t.Context(), agentImage{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("error=%v, want build failure", err)
	}
	if strings.Contains(err.Error(), "earlier diagnostics truncated") || strings.Contains(err.Error(), "version output") {
		t.Fatal("build failure retains prerequisite diagnostics or truncation state")
	}
}

func TestBuildxExporterReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runner := &fakeBuildxRunner{err: context.Canceled}
	_, err := (buildxExporter{runner: runner}).ExportOCI(ctx, agentImage{
		AgentkitFile: []byte("{}"), Name: "writer",
		AdapterRef: "registry.example/harness@" + testDigest,
		Platform:   "linux/amd64", SourceEpoch: 1,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("ExportOCI() error = %v", err)
	}
}

func TestLocalBuildxRunnerCancellation(t *testing.T) {
	switch runtime.GOOS {
	case "aix", "darwin", "dragonfly", "freebsd", "illumos", "linux", "netbsd", "openbsd", "solaris":
	default:
		t.Skip("requires Unix interrupt and kill semantics")
	}

	// Reverting to immediate Kill breaks the clean-exit and grace-period cases.
	// Removing WaitDelay leaves Run blocked on the descendant's stderr pipe.
	for _, tc := range []struct {
		name       string
		mode       string
		cancel     bool
		descendant bool
		cleanExit  bool
		killed     bool
		minElapsed time.Duration
		maxElapsed time.Duration
		wantErr    error
	}{
		{
			name: "interrupt allows clean exit", mode: "honor", cancel: true,
			cleanExit: true, maxElapsed: 8 * time.Second, wantErr: context.Canceled,
		},
		{
			name: "ignored interrupt is force killed after grace period", mode: "ignore", cancel: true,
			killed: true, minElapsed: 9 * time.Second, maxElapsed: 25 * time.Second,
		},
		{
			name: "clean exit with inherited stderr is bounded", mode: "honor-descendant", cancel: true,
			descendant: true, cleanExit: true, minElapsed: 9 * time.Second,
			maxElapsed: 25 * time.Second, wantErr: context.Canceled,
		},
		{
			name: "forced kill with inherited stderr is bounded", mode: "ignore-descendant", cancel: true,
			descendant: true, killed: true, minElapsed: 9 * time.Second, maxElapsed: 25 * time.Second,
		},
		{
			name: "uncanceled exit with inherited stderr reports wait delay", mode: "exit-descendant",
			descendant: true, cleanExit: true, minElapsed: 9 * time.Second,
			maxElapsed: 25 * time.Second, wantErr: exec.ErrWaitDelay,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			var stdout, stderr bytes.Buffer
			finished := make(chan struct{})
			var runErr error
			var finishedAt time.Time
			go func() {
				// Non-file writers force os/exec to copy both output pipes. Read the
				// buffers only after finished closes, including on watchdog failure.
				runErr = (localBuildxRunner{}).Run(ctx, &stdout, &stderr, executable,
					"-test.run=^TestLocalBuildxRunnerHelperProcess$", "--", "--buildx-helper", tc.mode, dir)
				finishedAt = time.Now()
				close(finished)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-finished:
				default:
					killBuildxHelper(t, filepath.Join(dir, "process-pid"))
				}
				// WaitDelay closes our pipe but does not terminate descendants.
				killBuildxHelper(t, filepath.Join(dir, "descendant-ready"))
				select {
				case <-finished:
				case <-time.After(15 * time.Second):
					t.Error("Run did not finish after explicit helper cleanup")
				}
			})

			waitBuildxHelperReady(t, filepath.Join(dir, "ready"), finished)
			if tc.descendant {
				// The direct child publishes readiness only after its descendant
				// has inherited stderr and installed its own signal disposition.
				waitBuildxHelperReady(t, filepath.Join(dir, "descendant-ready"), finished)
			}
			started := time.Now()
			if tc.cancel {
				cancel()
			}
			select {
			case <-finished:
			case <-time.After(25 * time.Second):
				t.Fatal("Run still blocked 25s after readiness/cancellation; inherited stderr was not bounded")
			}

			elapsed := finishedAt.Sub(started)
			if elapsed < tc.minElapsed || elapsed >= tc.maxElapsed {
				t.Errorf("Run returned after %v, want [%v, %v)", elapsed, tc.minElapsed, tc.maxElapsed)
			}
			if tc.wantErr != nil && !errors.Is(runErr, tc.wantErr) {
				t.Errorf("Run error = %v, want %v", runErr, tc.wantErr)
			}
			if tc.cancel && !errors.Is(ctx.Err(), context.Canceled) {
				t.Errorf("context error = %v, want context.Canceled", ctx.Err())
			}
			if !tc.cancel && ctx.Err() != nil {
				t.Errorf("uncanceled context error = %v", ctx.Err())
			}
			if tc.killed {
				var exitErr *exec.ExitError
				if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != -1 ||
					!strings.Contains(exitErr.ProcessState.String(), "signal: killed") {
					t.Errorf("Run error = %v, want direct child killed by SIGKILL", runErr)
				}
			}
			if got := strings.Contains(stderr.String(), "clean exit acknowledged\n"); got != tc.cleanExit {
				t.Errorf("clean exit acknowledgement = %v, want %v; stderr=%q", got, tc.cleanExit, stderr.String())
			}
			if !strings.Contains(stdout.String(), "direct child ready\n") {
				t.Errorf("stdout = %q, missing direct-child readiness output", stdout.String())
			}
			if tc.descendant && !strings.Contains(stderr.String(), "descendant holding stderr\n") {
				t.Errorf("stderr = %q, missing inherited-pipe acknowledgement", stderr.String())
			}
		})
	}
}

func waitBuildxHelperReady(t *testing.T, path string, finished <-chan struct{}) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(string(data)); err == nil && pid > 0 {
				return
			}
		}
		select {
		case <-finished:
			t.Fatalf("helper exited before readiness: %s", path)
		case <-deadline.C:
			t.Fatalf("helper did not become ready within 15s: %s", path)
		case <-tick.C:
		}
	}
}

func killBuildxHelper(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Errorf("read helper PID: %v", err)
		return
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil || pid <= 0 {
		t.Errorf("invalid helper PID %q", data)
		return
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Errorf("find helper process %d: %v", pid, err)
		return
	}
	defer process.Release()
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("kill helper process %d: %v", pid, err)
	}
}

func TestLocalBuildxRunnerHelperProcess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--buildx-helper" {
		return
	}
	mode, dir := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	publish := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if mode == "hold-stderr" {
		signal.Ignore(os.Interrupt)
		fmt.Fprintln(os.Stderr, "descendant holding stderr")
		publish("descendant-ready")
		// A final safety net if the outer test process itself is terminated.
		time.Sleep(60 * time.Second)
		os.Exit(3)
	}

	publish("process-pid")
	interrupt := make(chan os.Signal, 1)
	if strings.HasPrefix(mode, "ignore") {
		signal.Ignore(os.Interrupt)
	} else {
		signal.Notify(interrupt, os.Interrupt)
	}
	if strings.HasSuffix(mode, "-descendant") {
		child := exec.Command(os.Args[0], "-test.run=^TestLocalBuildxRunnerHelperProcess$",
			"--", "--buildx-helper", "hold-stderr", dir)
		child.Stdout, child.Stderr = io.Discard, os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		waitBuildxHelperReady(t, filepath.Join(dir, "descendant-ready"), make(chan struct{}))
		if err := child.Process.Release(); err != nil {
			fmt.Fprintf(os.Stderr, "release descendant process: %v\n", err)
			os.Exit(2)
		}
	}
	fmt.Fprintln(os.Stdout, "direct child ready")
	publish("ready")
	if mode != "exit-descendant" {
		select {
		case <-interrupt:
		case <-time.After(60 * time.Second):
			os.Exit(3)
		}
	}
	fmt.Fprintln(os.Stderr, "clean exit acknowledged")
	os.Exit(0)
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
