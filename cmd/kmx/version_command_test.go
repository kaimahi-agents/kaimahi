package main

import (
	"bytes"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func versionTestDependencies(out, errOut *bytes.Buffer) dependencies {
	deps, _ := testDependencies(out, errOut)
	deps.stdin = nil
	deps.buildInfo = func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, true }
	deps.loadConfig = func(string, string) (*config.Config, error) { panic("unreachable") }
	return deps
}

func TestVersionJSON(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, flags := range [][]string{{"-o", "json"}, {"-ojson"}, {"--output", "json"}, {"--output=json"}} {
			t.Run(goos+strings.Join(flags, "_"), func(t *testing.T) {
				var out, diagnostics bytes.Buffer
				deps := versionTestDependencies(&out, &diagnostics)
				deps.goos = goos
				if err := execute(append([]string{"version"}, flags...), deps); err != nil {
					t.Fatal(err)
				}
				r := decodeMachineRecord(t, out.Bytes())
				if len(r) != 5 || r["schemaVersion"] != float64(1) || r["kind"] != "kmx.version" || r["kmxVersion"] != "v1.2.3" || r["orkaVersion"] != app.OrkaVersion {
					t.Fatalf("bad version projection: %+v", r)
				}
				caps, ok := r["capabilities"].([]any)
				want := 6
				if goos == "windows" {
					want = 2
				}
				if !ok || len(caps) != want {
					t.Fatalf("advertises unimplemented operations: %+v", r)
				}
				capability := caps[0].(map[string]any)
				if len(capability) != 3 || capability["operation"] != "version" || capability["output"] != "json" || capability["schemaVersion"] != float64(1) {
					t.Fatalf("bad capability: %+v", capability)
				}
				if goos != "windows" {
					for i, operation := range []string{"cluster.up", "cluster.orka.install"} {
						c := caps[i+1].(map[string]any)
						if len(c) != 3 || c["operation"] != operation || c["output"] != "jsonl" || c["schemaVersion"] != float64(1) {
							t.Fatal(c)
						}
					}
				}
				first := 3
				if goos == "windows" {
					first = 1
				}
				for i, runner := range []string{"embedded", "copilotd"} {
					if goos == "windows" && runner == "copilotd" {
						continue
					}
					c := caps[first+i].(map[string]any)
					if c["operation"] != "file.test" || c["output"] != "jsonl" || c["runner"] != runner || c["schemaVersion"] != float64(1) {
						t.Fatal(c)
					}
					if goos == "windows" {
						if c["model"] != "fake" || len(c) != 5 {
							t.Fatal(c)
						}
					} else if len(c) != 4 {
						t.Fatal(c)
					}
				}
				if goos != "windows" {
					c := caps[5].(map[string]any)
					if c["operation"] != "cluster.orka.tools" || c["output"] != "json" || c["schemaVersion"] != float64(1) {
						t.Fatal(c)
					}
				}
				if diagnostics.Len() != 0 {
					t.Fatalf("version diagnostics: %q", diagnostics.String())
				}
			})
		}
	}
}

func TestVersionJSONErrors(t *testing.T) {
	for _, argv := range [][]string{
		{"version", "-o", "json", "private-operand"},
		{"version", "-ojson", "--help=false", "private-operand"},
		{"version", "-ojson", "-h=false", "private-operand"},
		{"version", "-ojson", "--help=private-value"},
		{"version", "-ojson", "--help=true", "--help=false", "private-operand"},
		{"version", "--unknown=private-body", "-o", "json"},
		{"version", "--unknown", "-o", "json"},
		{"version", "-o", "json", "--unknown"},
		{"version", "--unknown", "private-body", "--output=json"},
		{"version", "-o", "json", "--context", ""},
		{"version", "-o", "json", "-context="},
		{"--context=", "version", "--output=json"},
		{"version", "-o", "json", "--container-engine="},
	} {
		t.Run(strings.Join(argv, "_"), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps := versionTestDependencies(&out, &diagnostics)
			err := execute(argv, deps)
			if err == nil || exitCode(err) != 1 {
				t.Fatalf("error/exit: %v", err)
			}
			var safe *machineCommandError
			if !errors.As(err, &safe) || errors.Unwrap(safe) == nil {
				t.Fatalf("lost cause: %v", err)
			}
			r := decodeMachineRecord(t, out.Bytes())
			if r["kind"] != "kmx.result" || r["operation"] != "version" || r["seq"] != float64(1) || r["outcome"] != "failed" || r["verdict"] != "not_applicable" || r["exitCode"] != float64(1) {
				t.Fatalf("bad error record: %+v", r)
			}
			e := r["error"].(map[string]any)
			if e["code"] != "invalid_arguments" || e["message"] != "The command arguments are invalid." {
				t.Fatalf("bad safe error: %+v", e)
			}
			fmt.Fprintln(&diagnostics, err) // main's boundary prints the same Error().
			if strings.Contains(out.String()+diagnostics.String(), "private") || diagnostics.String() != "The command arguments are invalid.\n" {
				t.Fatalf("cause escaped: %s %s", out.String(), diagnostics.String())
			}
		})
	}
}

func TestVersionJSONBoundary(t *testing.T) {
	for _, tc := range []struct {
		argv     []string
		selected bool
	}{
		{[]string{"version", "-o", "json"}, true},
		{[]string{"version", "--context=x", "-o", "json"}, true},
		{[]string{"-context=x", "version", "-ojson"}, true},
		{[]string{"--context", "-ojson", "version", "-o", "json"}, true},
		{[]string{"--context", "version", "-o", "json"}, false},
		{[]string{"--container-engine", "version", "-o", "json"}, false},
		{[]string{"version", "--", "-o", "json"}, false},
		{[]string{"version", "-o", "text", "-ojson"}, false},
		{[]string{"version", "-ojson", "--output=json"}, false},
		{[]string{"version", "-hojson"}, false},
		{[]string{"__complete", "version", "-o", "json"}, false},
		{[]string{"__completeNoDesc", "version", "-o", "json"}, false},
		{[]string{"help", "version", "-ojson"}, false},
		{[]string{"version", "-ojson", "--help"}, true},
		{[]string{"version", "-ojson", "-h"}, true},
		{[]string{"unknown", "version", "-ojson"}, false},
	} {
		if got := versionJSONInvocation(tc.argv); got != tc.selected {
			t.Errorf("%q selected=%v want %v", tc.argv, got, tc.selected)
		}
	}
	for _, argv := range [][]string{{"version", "--context=x", "-ojson"}, {"--context", "-ojson", "version", "-ojson"}} {
		var out, diagnostics bytes.Buffer
		if err := execute(argv, versionTestDependencies(&out, &diagnostics)); err != nil {
			t.Fatal(err)
		}
		if r := decodeMachineRecord(t, out.Bytes()); r["kind"] != "kmx.version" {
			t.Fatal(r)
		}
	}
}

func TestVersionTextUnchanged(t *testing.T) {
	want := "kmx v1.2.3 (installed with go install)\n  kaimahi is pre-1.0 and incubating: minor versions may break behaviour, and say so in CHANGELOG.md\n  orka     " + app.OrkaVersion + "\n  model    qwen2.5:3b\n"
	for _, argv := range [][]string{{"version"}, {"version", "-o", "text"}} {
		var out, diagnostics bytes.Buffer
		deps := versionTestDependencies(&out, &diagnostics)
		if err := execute(argv, deps); err != nil || out.String() != want || diagnostics.Len() != 0 {
			t.Fatalf("text changed: %v, %q, %q", err, out.String(), diagnostics.String())
		}
		deps.stdout = &machineFaultWriter{short: true}
		if err := execute(argv, deps); err != nil {
			t.Fatalf("legacy text exit changed on short write: %v", err)
		}
	}
}

func TestVersionJSONOutputFailure(t *testing.T) {
	for _, output := range []*machineFaultWriter{{short: true}, {err: errors.New("private writer failure")}} {
		var out, diagnostics bytes.Buffer
		deps := versionTestDependencies(&out, &diagnostics)
		deps.stdout = output
		err := execute([]string{"version", "-ojson"}, deps)
		if err == nil || exitCode(err) != 1 || err.Error() != "Cannot write machine output." || output.calls != 1 || diagnostics.Len() != 0 {
			t.Fatalf("unsafe output failure/retry: %v calls=%d stderr=%s", err, output.calls, diagnostics.String())
		}
	}
}

func TestVersionJSONHelpAndCompletionRemainInert(t *testing.T) {
	for _, argv := range [][]string{{"version", "-ojson", "--help"}, {"version", "-ojson", "-h"}, {"__complete", "version", "--output", ""}, {"__completeNoDesc", "version", "--output", ""}} {
		var out, diagnostics bytes.Buffer
		deps := versionTestDependencies(&out, &diagnostics)
		deps.goos = "windows"
		if err := execute(argv, deps); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), `"kind":"kmx.result"`) || strings.Contains(out.String(), `"kind":"kmx.version"`) {
			t.Fatalf("help became machine result: %s", out.String())
		}
		if strings.Contains(argv[0], "__complete") {
			if !strings.Contains(out.String(), "json") || !strings.Contains(out.String(), "text") {
				t.Fatalf("missing output completion: %s", out.String())
			}
		} else if diagnostics.Len() != 0 || !strings.Contains(out.String(), "--output") {
			t.Fatalf("help output: %s %s", out.String(), diagnostics.String())
		}
	}
	var out, diagnostics bytes.Buffer
	if err := execute([]string{"version", "-o", "yaml"}, versionTestDependencies(&out, &diagnostics)); err == nil || out.Len() != 0 {
		t.Fatalf("unknown output accepted: %v %s", err, out.String())
	}
}
