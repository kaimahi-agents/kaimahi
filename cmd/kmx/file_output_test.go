package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type machineCLIRunner struct{ fake cliPreviewFake }

func (r *machineCLIRunner) Run(ctx context.Context, req agentpreview.Request) (agentpreview.Outcome, error) {
	if req.TelemetryReady != nil {
		if err := req.TelemetryReady(func(...string) error { return nil }); err != nil {
			return agentpreview.Outcome{}, errors.Join(agentpreview.ErrDiagnosticsFailed, err)
		}
	}
	out, err := r.fake.Run(ctx, req)
	for i, row := range out.Examples {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if e := req.ObserveCase(i, agentpreview.Observation{Event: "case.started"}); e != nil {
			return out, e
		}
		if req.ExampleFinished != nil {
			req.ExampleFinished(row)
		}
	}
	return out, err
}
func fileRecords(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	if len(b) == 0 || b[len(b)-1] != '\n' {
		t.Fatal("incomplete stream", string(b))
	}
	records := []map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSuffix(b, []byte{'\n'}), []byte{'\n'}) {
		var r map[string]any
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err, string(b))
		}
		records = append(records, r)
	}
	id := records[0]["operationId"]
	for i, r := range records {
		if r["schemaVersion"] != float64(1) || r["seq"] != float64(i+1) || r["operation"] != "file.test" || r["operationId"] != id {
			t.Fatal("bad envelope", r)
		}
		if i < len(records)-1 && r["kind"] == "kmx.result" {
			t.Fatal("post-terminal record")
		}
	}
	if records[len(records)-1]["kind"] != "kmx.result" {
		t.Fatal("no terminal")
	}
	return records
}
func TestFileJSONLSelectorsAndVerdicts(t *testing.T) {
	for _, selector := range [][]string{{"-o", "jsonl"}, {"-ojsonl"}, {"--output", "jsonl"}, {"--output=jsonl"}} {
		for _, v := range []runtime.EvaluationVerdict{runtime.EvaluationPass, runtime.EvaluationFail, runtime.EvaluationUnknown} {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			runner := &machineCLIRunner{fake: cliPreviewFake{verdict: v}}
			deps.fileRunner = runner
			args := append([]string{"test", cliPreviewFile(t), "--verbose"}, selector...)
			err := execute(args, deps)
			want := 0
			if v == runtime.EvaluationFail {
				want = 1
			} else if v == runtime.EvaluationUnknown {
				want = 2
			}
			if err != nil && exitCode(err) != want || err == nil && want != 0 {
				t.Fatal(v, err)
			}
			records := fileRecords(t, out.Bytes())
			last := records[len(records)-1]
			if last["exitCode"] != float64(want) || last["verdict"] != string(v) || last["summaryCount"] != float64(1) || last["evidence"] != "diagnostic_preview" || last["effects"] != nil {
				t.Fatal(last)
			}
			previous := records[len(records)-2]
			if previous["event"] != "case.summary" || previous["session"].(map[string]any)["availability"] != "unavailable" {
				t.Fatal(previous)
			}
			if *loads != 0 || strings.Contains(out.String()+diag.String(), "PRIVATE") || strings.Contains(out.String(), "agent file.agent.md") || strings.Contains(diag.String(), "[verbose]") {
				t.Fatal("leak/config", out.String(), diag.String())
			}
		}
	}
}
func TestFileJSONLEarlyErrorsAndInertPaths(t *testing.T) {
	file := cliPreviewFile(t)
	for _, args := range [][]string{{"test", file, "--unknown=PRIVATE", "-ojsonl"}, {"test", file, "--context=", "-ojsonl"}, {"test", file, "--runner=PRIVATE", "-ojsonl"}, {"test", t.TempDir(), "-ojsonl"}, {"test", file, "--github-summary", "-ojsonl"}} {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		runner := &machineCLIRunner{}
		deps.fileRunner = runner
		err := execute(args, deps)
		if err == nil || runner.fake.calls != 0 {
			t.Fatal("invalid intake executed", args, err)
		}
		r := fileRecords(t, out.Bytes())
		if r[len(r)-1]["exitCode"] != float64(exitCode(err)) || strings.Contains(err.Error()+out.String()+diag.String(), "PRIVATE") {
			t.Fatal("unsafe error", out.String(), err)
		}
	}
	for _, args := range [][]string{{"test", file, "-ojsonl", "--help"}, {"__complete", "test", file, "-ojsonl", ""}} {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		_ = execute(args, deps)
		if strings.Contains(out.String(), `"kind":"kmx.event"`) {
			t.Fatal("inert prefix")
		}
	}
	for _, args := range [][]string{{"run", file, "-ojsonl"}, {"test", "-ojsonl"}} {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		if execute(args, deps) == nil || strings.Contains(out.String(), `"operation":"file.test"`) {
			t.Fatal("broadened run/folder", args, out.String())
		}
	}
}

type fileEventFailWriter struct {
	bytes.Buffer
	failed int
}

func (w *fileEventFailWriter) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte(`"event":"case.started"`)) {
		w.failed++
		return 0, errors.New("PRIVATE OUTPUT ERROR")
	}
	return w.Buffer.Write(b)
}

func TestFileJSONLCancellationFailureAndWindows(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		runner := &machineCLIRunner{fake: cliPreviewFake{cause: context.Canceled}}
		deps.fileRunner = runner
		err := execute([]string{"test", cliPreviewFile(t), "-ojsonl"}, deps)
		records := fileRecords(t, out.Bytes())
		last := records[len(records)-1]
		if err == nil || exitCode(err) != 130 || last["exitCode"] != float64(130) || last["outcome"] != "cancelled" || last["verdict"] != "unknown" {
			t.Fatal(last, err)
		}
	})
	t.Run("midstream failure and borrowed input", func(t *testing.T) {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		writer := &fileEventFailWriter{}
		stdin, openErr := os.CreateTemp(t.TempDir(), "input")
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer stdin.Close()
		if _, e := stdin.WriteString("PRIVATE STDIN"); e != nil {
			t.Fatal(e)
		}
		if _, e := stdin.Seek(0, io.SeekStart); e != nil {
			t.Fatal(e)
		}
		deps.stdout = writer
		deps.stdin = stdin
		runner := &machineCLIRunner{fake: cliPreviewFake{verdict: runtime.EvaluationPass}}
		deps.fileRunner = runner
		err := execute([]string{"test", cliPreviewFile(t), "-ojsonl"}, deps)
		position, seekErr := stdin.Seek(0, io.SeekCurrent)
		if err == nil || exitCode(err) != 1 || writer.failed != 1 || strings.Contains(writer.String(), `"kind":"kmx.result"`) || position != 0 || seekErr != nil || strings.Contains(err.Error()+diag.String(), "PRIVATE") {
			t.Fatal("write retry, terminal, input use or leak", err, writer.String(), stdin)
		}
	})
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "windows fake", true: "windows refuses native"}[native], func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, _ := testDependencies(&out, &diag)
			deps.goos = "windows"
			runner := &machineCLIRunner{fake: cliPreviewFake{verdict: runtime.EvaluationPass}}
			deps.fileRunner = runner
			args := []string{"test", cliPreviewFile(t), "-ojsonl"}
			if native {
				args = append(args, "--runner=copilotd", "--host=ws://127.0.0.1:8080")
			}
			err := execute(args, deps)
			r := fileRecords(t, out.Bytes())
			last := r[len(r)-1]
			if native {
				if err == nil || runner.fake.calls != 0 || last["error"].(map[string]any)["code"] != "unsupported" {
					t.Fatal(last, err)
				}
			} else if err != nil || runner.fake.calls != 1 || last["outcome"] != "complete" {
				t.Fatal(last, err)
			}
		})
	}
}

func TestFileJSONLRefusalDoesNotInventRunner(t *testing.T) {
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	err := execute([]string{"test", cliPreviewFile(t), "--runner=copilotd", "--github-summary", "-ojsonl"}, deps)
	if err == nil {
		t.Fatal("expected usage refusal")
	}
	r := fileRecords(t, out.Bytes())
	if runner := r[len(r)-1]["runner"]; runner != nil {
		t.Fatal("runner claimed before option admission", runner)
	}
}

func TestFileJSONLRecognitionAndOutputFailure(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"test", "file.agent.md", "-ojsonl"}, true},
		{[]string{"test", "--tests", "-ojsonl", "file.agent.md"}, false},
		{[]string{"test", "explain", "-ojsonl"}, false},
		{[]string{"test", "--", "file.agent.md", "-ojsonl"}, false},
		{[]string{"test", "file.agent.md", "-ojsonl", "-otext"}, false},
	} {
		if fileJSONInvocation(tc.args) != tc.want {
			t.Fatal(tc)
		}
	}
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	runner := &machineCLIRunner{fake: cliPreviewFake{verdict: runtime.EvaluationPass}}
	deps.fileRunner = runner
	deps.stdout = &machineFaultWriter{err: errors.New("private output")}
	err := execute([]string{"test", cliPreviewFile(t), "-ojsonl"}, deps)
	if err == nil || exitCode(err) != 1 || runner.fake.calls != 0 || strings.Contains(err.Error(), "private") {
		t.Fatal("output failure executed/leaked", err)
	}
}
