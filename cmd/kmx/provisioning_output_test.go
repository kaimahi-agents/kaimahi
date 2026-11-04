package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/spf13/cobra"
)

func setupResultRecord(t *testing.T, out []byte, exit int) map[string]any {
	t.Helper()
	rs := machineRecords(t, out)
	if len(rs) < 3 {
		t.Fatalf("missing preflight/result: %s", out)
	}
	terminal := 0
	for i, r := range rs {
		if r["seq"] != float64(i+1) || r["operationId"] != rs[0]["operationId"] {
			t.Fatal(r)
		}
		if r["kind"] == "kmx.result" {
			terminal++
			if i != len(rs)-1 {
				t.Fatal("post-terminal record")
			}
		}
	}
	r := rs[len(rs)-1]
	if terminal != 1 || r["exitCode"] != float64(exit) {
		t.Fatal(rs)
	}
	return r
}

func TestProvisioningJSONRecognition(t *testing.T) {
	for _, tc := range []struct {
		args []string
		op   string
	}{
		{[]string{"cluster", "up", "-ojsonl"}, "cluster.up"},
		{[]string{"cluster", "up", "-o", "jsonl"}, "cluster.up"},
		{[]string{"cluster", "up", "--output", "jsonl"}, "cluster.up"},
		{[]string{"--context", "c", "cluster", "orka", "install", "--output=jsonl"}, "cluster.orka.install"},
		{[]string{"cluster", "up", "--unknown", "-o", "jsonl"}, "cluster.up"},
		{[]string{"cluster", "up", "--step", "-ojsonl"}, ""},
		{[]string{"cluster", "orka", "install", "--model", "-ojsonl"}, ""},
		{[]string{"cluster", "up", "--offline-bundle", "-ojsonl"}, ""},
		{[]string{"--context", "cluster", "up", "-ojsonl"}, ""},
		{[]string{"cluster", "up", "--", "-ojsonl"}, ""},
		{[]string{"cluster", "up", "-ojsonl", "-ojsonl"}, ""},
		{[]string{"cluster", "down", "-ojsonl"}, ""},
	} {
		if got := provisioningJSONInvocation(tc.args); got != tc.op {
			t.Fatalf("%q got %q want %q", tc.args, got, tc.op)
		}
	}
}

func TestProvisioningEarlyErrors(t *testing.T) {
	cases := [][]string{
		{"cluster", "up", "-ojsonl", "--unknown=private-data"},
		{"cluster", "up", "--unknown", "-ojsonl"},
		{"cluster", "up", "-ojsonl", "--context="},
		{"cluster", "up", "-ojsonl", "private-data"},
		{"cluster", "orka", "install", "-ojsonl", "--offline-bundle=private-data", "--provider=-", "--no-apply", "--dry-run"},
		{"cluster", "up", "-ojsonl", "--help=false", "private-data"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute(args, deps)
			if err == nil || *loads != 0 {
				t.Fatal("early error loaded config", err, *loads)
			}
			r := setupResultRecord(t, out.Bytes(), exitCode(err))
			if r["error"].(map[string]any)["code"] != "invalid_arguments" || r["mode"] != "apply" || len(r["effects"].([]any)) != 0 {
				t.Fatal(r)
			}
			if strings.Contains(out.String()+diagnostics.String()+err.Error(), "private-data") {
				t.Fatal("unsafe error")
			}
		})
	}
	for _, kind := range []string{"config", "windows"} {
		t.Run(kind, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, _ := testDependencies(&out, &diagnostics)
			want := "configuration_invalid"
			cause := errors.New("private config")
			deps.loadConfig = func(string, string) (*config.Config, error) { return nil, cause }
			if kind == "windows" {
				deps.goos = "windows"
				want = "unsupported"
			}
			err := execute([]string{"cluster", "up", "-ojsonl"}, deps)
			r := setupResultRecord(t, out.Bytes(), 1)
			if r["error"].(map[string]any)["code"] != want {
				t.Fatal(r)
			}
			if kind == "config" && !errors.Is(err, cause) {
				t.Fatal("cause lost")
			}
		})
	}
}

func TestProvisioningHelpInert(t *testing.T) {
	for _, args := range [][]string{{"cluster", "up", "-ojsonl", "--help"}, {"cluster", "orka", "install", "-ojsonl", "--help"}, {"__complete", "cluster", "up", ""}} {
		var out, errOut bytes.Buffer
		deps, loads := testDependencies(&out, &errOut)
		if err := execute(args, deps); err != nil || *loads != 0 || strings.Contains(out.String(), "kmx.event") {
			t.Fatal(args, err, *loads, out.String())
		}
	}
}

func TestProvisioningModesAndEffects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sel    provisioningSelection
		stages int
		mode   string
	}{
		{"up", upSelection(app.UpOptions{}), 4, "apply"},
		{"step", upSelection(app.UpOptions{Step: "model"}), 1, "apply"},
		{"offline-up", upSelection(app.UpOptions{Step: "cluster", OfflineBundle: "bundle"}), 1, "apply"},
		{"install", installSelection(app.OrkaOptions{}), 4, "apply"},
		{"no-provider", installSelection(app.OrkaOptions{Provider: "-"}), 3, "apply"},
		{"dry", installSelection(app.OrkaOptions{DryRun: true}), 2, "dry_run"},
		{"noapply", installSelection(app.OrkaOptions{NoApply: true}), 0, "no_apply"},
		{"offline", installSelection(app.OrkaOptions{OfflineBundle: "bundle", Provider: "-"}), 1, "apply"},
		{"offline-noapply", installSelection(app.OrkaOptions{OfflineBundle: "bundle", NoApply: true}), 1, "no_apply"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			m := newProvisioningOutput(&out, "cluster.up", context.Background())
			defer m.stop()
			m.selectSetup(tc.sel)
			m.beginPreflight()
			m.endPreflight("done")
			if tc.sel.coarse {
				m.markEffect("runtime", "active")
				m.markEffect("runtime", "done")
			}
			if err := m.finishSetup(nil); err != nil {
				t.Fatal(err)
			}
			r := setupResultRecord(t, out.Bytes(), 0)
			es := r["effects"].([]any)
			if r["mode"] != tc.mode || len(es) != tc.stages {
				t.Fatal(r)
			}
			if tc.sel.coarse {
				e := es[0].(map[string]any)
				want := "possible"
				if tc.mode != "apply" {
					want = "none"
				}
				if e["attempt"] != "completed" || e["mutation"] != want {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestProvisioningDetachedStdin(t *testing.T) {
	input, feed, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer feed.Close()
	if _, err := feed.Write([]byte("borrowed")); err != nil {
		t.Fatal(err)
	}
	for _, consent := range []bool{false, true} {
		t.Run(fmt.Sprint(consent), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, _ := testDependencies(&out, &diagnostics)
			deps.stdin = input
			original := app.New(&config.Config{})
			deps.newApp = func(*config.Config) *app.App { return original }
			state := &commandState{deps: deps}
			cmd := &cobra.Command{Use: "fixture"}
			bindProvisioningOutput(cmd, state, "cluster.up", func() provisioningSelection { return upSelection(app.UpOptions{Step: "cluster"}) }, func(a *app.App) error {
				if a.Stdin != nil || original.Stdin != input {
					t.Fatal("stdin not detached on actual copy")
				}
				// A real existing consent guard: local context needs explicit consent here.
				cfg := []byte(`{"contexts":[{"name":"remote","context":{"cluster":"remote"}}],"clusters":[{"name":"remote","cluster":{"server":"https://example.invalid"}}]}`)
				return checkProvisioningConsent(a, cfg, consent)
			})
			cmd.SetOut(&out)
			cmd.SetErr(&diagnostics)
			cmd.SetArgs([]string{"-ojsonl"})
			err := cmd.Execute()
			if (err == nil) != consent {
				t.Fatal("consent behavior", err)
			}
			if strings.Contains(out.String()+diagnostics.String(), "Type") {
				t.Fatal("hidden prompt escaped")
			}
		})
	}
	if err := feed.Close(); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 8)
	if n, err := input.Read(data); err != nil || n != 8 || string(data) != "borrowed" {
		t.Fatal("consumed borrowed input", n, err, string(data))
	}
}

func TestProvisioningOutputAndCancellation(t *testing.T) {
	var out bytes.Buffer
	m := newProvisioningOutput(&out, "cluster.up", context.Background())
	defer m.stop()
	m.selectSetup(upSelection(app.UpOptions{}))
	m.beginPreflight()
	m.endPreflight("done")
	m.observe("Prepare kind cluster", "active", nil)
	m.observe("Prepare kind cluster", "done", nil)
	m.observe("Deploy Ollama", "active", nil)
	m.cancel()
	err := m.finishSetup(errors.New("private child killed"))
	if err == nil || exitCode(err) != 1 {
		t.Fatal(err)
	}
	r := setupResultRecord(t, out.Bytes(), 1)
	if r["outcome"] != "cancelled" || r["error"].(map[string]any)["code"] != "cancelled" {
		t.Fatal(r)
	}
	es := r["effects"].([]any)
	if es[0].(map[string]any)["mutation"] != "possible" || es[1].(map[string]any)["attempt"] != "started" || es[2].(map[string]any)["attempt"] != "not_started" {
		t.Fatal(es)
	}
	fault := &machineFaultWriter{short: true}
	broken := newProvisioningOutput(fault, "cluster.up", context.Background())
	defer broken.stop()
	if broken.beginPreflight() == nil || broken.ctx.Err() == nil {
		t.Fatal("writer did not cancel")
	}
	if broken.finishSetup(context.Canceled) == nil || fault.calls != 1 {
		t.Fatal("retried broken output")
	}
}

func TestProvisioningSafeSinks(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	state := &commandState{deps: deps}
	cmd := &cobra.Command{Use: "fixture", SilenceUsage: true, SilenceErrors: true}
	bindProvisioningOutput(cmd, state, "cluster.up", func() provisioningSelection { return upSelection(app.UpOptions{}) }, func(a *app.App) error {
		fmt.Fprint(a.Out, "private stdout")
		fmt.Fprint(a.Err, "private stderr")
		fmt.Fprint(a.Run.Stdout, "private child stdout")
		fmt.Fprint(a.Run.Stderr, "private child stderr")
		if a.Run.Echo || a.Stdin != nil {
			t.Fatal("unsafe invocation")
		}
		return errors.New("private failure")
	})
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"-ojsonl"})
	err := cmd.Execute()
	if err == nil || strings.Contains(out.String()+errOut.String()+err.Error(), "private") {
		t.Fatal(err, out.String(), errOut.String())
	}
	setupResultRecord(t, out.Bytes(), 1)
}

func TestProvisioningCancellationDuringStart(t *testing.T) {
	var out bytes.Buffer
	m := newProvisioningOutput(&out, "cluster.up", context.Background())
	defer m.stop()
	m.selectSetup(upSelection(app.UpOptions{}))
	m.beginPreflight()
	m.endPreflight("done")
	// Cancellation can arrive between runPhase's pre-check and its start callback.
	m.cancel()
	m.observe("Prepare kind cluster", "active", nil)
	rs := machineRecords(t, out.Bytes())
	if rs[2]["event"] != "stage.started" {
		t.Fatal("finish without start", rs[2])
	}
}

func checkProvisioningConsent(a *app.App, raw []byte, consent bool) error {
	cfg, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return err
	}
	confirm := ""
	if consent {
		confirm = "remote"
	}
	return guard.CheckContext(a.Run.Context, cfg, guard.Request{Context: "remote", Source: config.SourceFlag, Confirm: confirm}, a.Err, a.Stdin)
}
