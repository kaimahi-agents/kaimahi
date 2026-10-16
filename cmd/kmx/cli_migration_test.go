package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestCLIMigrationRoutesAndOfflineHelp(t *testing.T) {
	for _, path := range [][]string{
		{"create"}, {"chat"}, {"run"}, {"orka", "show"}, {"orka", "list"},
		{"eval"}, {"eval", "diff"}, {"eval", "from-sessions"}, {"push"}, {"task", "result"},
	} {
		t.Run(strings.Join(path, "_"), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			root := newRootCommand(&commandState{deps: deps})
			cmd, remaining, err := root.Find(path)
			if err != nil || cmd == root || len(remaining) != 0 {
				t.Fatalf("route %v: cmd=%v remaining=%v err=%v", path, cmd, remaining, err)
			}
			if err := execute(append(append([]string(nil), path...), "--help"), deps); err != nil || *loads != 0 {
				t.Fatalf("help: err=%v config loads=%d", err, *loads)
			}
			if !strings.Contains(out.String(), "kmx "+strings.Join(path, " ")) {
				t.Fatalf("help lost canonical path: %s", &out)
			}
			if strings.Contains(out.String(), "kmx agent ") {
				t.Fatalf("help teaches removed route: %s", &out)
			}
		})
	}
}

func TestCLIMigrationRemovedRoutesRejectBeforeConfiguration(t *testing.T) {
	paths := [][]string{{"agent"}, {"edit"}, {"show"}, {"list"}, {"result"}, {"evaluate"}, {"retire"}, {"verify"}}
	for _, verb := range []string{"create", "chat", "run", "show", "list", "evaluate", "lift", "retire", "verify", "status", "edit", "result", "eval"} {
		paths = append(paths, []string{"agent", verb})
	}
	paths = append(paths, []string{"agent", "eval", "diff"}, []string{"agent", "eval", "from-sessions"})
	for _, path := range paths {
		for _, suffix := range [][]string{nil, {"--help"}, {"--unknown-flag"}} {
			args := append(append([]string(nil), path...), suffix...)
			t.Run(strings.Join(args, "_"), func(t *testing.T) {
				var out, diagnostics bytes.Buffer
				deps, loads := testDependencies(&out, &diagnostics)
				deps.loadConfig = func(string, string) (*config.Config, error) {
					*loads++
					return nil, errors.New("poisoned config")
				}
				err := execute(args, deps)
				if err == nil || !strings.Contains(err.Error(), "unknown command") || *loads != 0 || out.Len() != 0 || diagnostics.Len() != 0 {
					t.Fatalf("removed route: err=%v loads=%d stdout=%q stderr=%q", err, *loads, &out, &diagnostics)
				}
			})
		}
	}
}

func TestCLIMigrationRemovedHelpTopicsRejectBeforeConfiguration(t *testing.T) {
	for _, args := range [][]string{{"help", "agent"}, {"help", "agent", "create"}, {"help", "agent", "eval", "diff"}, {"help", "agent", "edit"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute(args, deps)
			if err == nil || !strings.Contains(err.Error(), "unknown command \"agent\"") || *loads != 0 || out.Len() != 0 {
				t.Fatalf("removed help topic: err=%v loads=%d stdout=%q", err, *loads, &out)
			}
		})
	}
}

func TestCLIMigrationDiscoveryDoesNotAdvertiseRemovedPaths(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"__complete", ""}, {"__complete", "agent"}, {"__complete", "orka", ""}, {"__complete", "bundle", ""}, {"__complete", "eval", ""}} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(args, deps); err != nil || *loads != 0 {
			t.Fatalf("%v: err=%v loads=%d", args, err, *loads)
		}
		for _, line := range strings.Split(out.String(), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && (fields[0] == "agent" || fields[0] == "edit") {
				t.Fatalf("removed command advertised: %s", line)
			}
		}
	}
}

func TestCLIMigrationPushRefusalsPrecedeConfigurationAndPreparation(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"push", "team:v1"}, "no workspace found"},
		{[]string{"push", "directory", "team:v1"}, "at most one"},
		{[]string{"push", "--workspace=", "team:v1"}, "--workspace"},
		{[]string{"push", "--workspace", "missing", "directory", "team:v1"}, "at most one"},
		{[]string{"push", "--workspace", "missing", "team:v1", "--context", "kind-demo"}, "--context"},
		{[]string{"push", "--workspace", "missing", "team:v1", "--container-engine", "podman"}, "--container-engine"},
		{[]string{"push", "--workspace", "missing", "team:v1", "--to-layout="}, "--to-layout"},
		{[]string{"push", "--workspace", "missing", "team:v1", "--to-layout", "layout", "--force"}, "--force"},
		{[]string{"push", "--workspace", "missing", "team:v1", "--to-layout", "layout", "--plain-http"}, "--plain-http"},
		{[]string{"suite", "push", "--workspace", "missing", "team:v1"}, "unknown flag: --workspace"},
	} {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("KMX_HOME", state)
			t.Chdir(t.TempDir())
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute(tc.args, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) || *loads != 0 || out.Len() != 0 {
				t.Fatalf("refusal: err=%v loads=%d stdout=%q", err, *loads, &out)
			}
			entries, err := os.ReadDir(state)
			if err != nil || len(entries) != 0 {
				t.Fatalf("refusal prepared workspace state: %v %v", entries, err)
			}
		})
	}
}

// Future operational eval hooks must not turn the existing cluster-independent,
// no-inference helpers into cluster operations. Exercise the leaf handler.
func TestCLIMigrationEvalHelpersSkipParentOperationalHooks(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"eval", "diff", "missing-before.json", "missing-after.json"}, "invalid evaluation receipt"},
		{[]string{"eval", "from-sessions", "source-1", "--sessions", "127.0.0.1:8080", "--assertions", "missing.yaml", "--out", "private.json"}, "assertion"},
	} {
		t.Run(tc.args[1], func(t *testing.T) {
			t.Chdir(privateTempDir(t))
			t.Setenv("PATH", t.TempDir())
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			root := newRootCommand(&commandState{deps: deps})
			parent, remaining, err := root.Find([]string{"eval"})
			if err != nil || len(remaining) != 0 {
				t.Fatalf("eval route missing: %v %v", remaining, err)
			}
			hooks := 0
			parent.PersistentPreRunE = func(*cobra.Command, []string) error {
				hooks++
				return errors.New("operational eval hook")
			}
			root.SetArgs(tc.args)
			err = root.Execute()
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) || hooks != 0 || *loads != 0 {
				t.Fatalf("helper: err=%v parent hooks=%d loads=%d", err, hooks, *loads)
			}
		})
	}
}

func TestCLIMigrationEvalHelpersPreserveGlobalFlagValidation(t *testing.T) {
	for _, args := range [][]string{
		{"eval", "diff", "missing-before.json", "missing-after.json"},
		{"eval", "from-sessions", "source-1", "--sessions", "127.0.0.1:8080", "--assertions", "missing.yaml", "--out", "private.json"},
	} {
		t.Run(args[1], func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute(append(append([]string(nil), args...), "--container-engine="), deps)
			if err == nil || !strings.Contains(err.Error(), "--container-engine requires docker or podman") || *loads != 0 || out.Len() != 0 {
				t.Fatalf("%v: err=%v loads=%d stdout=%q", args, err, *loads, &out)
			}
		})
	}
}

func TestCLIMigrationOrkaStatusFormats(t *testing.T) {
	t.Setenv("KMX_TOOLCHAIN", "off")
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct {
		flags []string
		want  string
	}{
		{nil, "kubectl"},
		{[]string{"-o", "table"}, "kubectl"},
		{[]string{"--output", " TABLE "}, "kubectl"},
		{[]string{"-o", "json"}, "status has no json output"},
		{[]string{"-o", "yaml"}, "status has no yaml output"},
		{[]string{"-o", "invalid"}, `status output "invalid" is not supported`},
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		err := execute(append([]string{"orka", "status"}, tc.flags...), deps)
		if err == nil || !strings.Contains(err.Error(), tc.want) || *loads != 1 || out.Len() != 0 {
			t.Fatalf("orka status %v: err=%v loads=%d stdout=%q", tc.flags, err, *loads, &out)
		}
	}
	for _, path := range [][]string{{"orka", "status"}} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		args := append([]string{"__complete"}, path...)
		args = append(args, "--output", "")
		if err := execute(args, deps); err != nil || *loads != 0 || out.String() != "table\n:4\n" {
			t.Fatalf("%v: err=%v loads=%d completion=%q", args, err, *loads, &out)
		}
	}
	root := newRootCommand(&commandState{deps: productionDependencies()})
	orka, _, _ := root.Find([]string{"orka"})
	count := 0
	for _, child := range orka.Commands() {
		if child.Name() == "status" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Orka has %d status children", count)
	}
}

func TestCLIMigrationOrkaStatusDefaultAndTableReportsAndReadsAreIdentical(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("KMX_STATUS_LOG", log)
	t.Setenv("KMX_TOOLCHAIN", "off")
	t.Setenv("PATH", dir)
	body := `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_STATUS_LOG"
case "$*" in
*"config view"*) printf '%s' '{"current-context":"kind-test","contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}],"clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}]}';;
*"get deploy -o json") printf '%s' '{"items":[{"metadata":{"name":"chart-controller","labels":{"app.kubernetes.io/component":"controller","app.kubernetes.io/name":"orka"}},"spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/orka-agents/orka:0.2.0"}]}}}}]}';;
*"get deploy"*) printf '%s' 'orka-controller=1/1 ';;
*"get crd"*) printf '%s' 'agents.core.orka.ai providers.core.orka.ai ';;
*"get providers"*) printf '%s' 'local=true ';;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	var reports, reads []string
	for _, path := range [][]string{{"orka", "status"}, {"orka", "status", "-o", "table"}} {
		if err := os.WriteFile(log, nil, 0600); err != nil {
			t.Fatal(err)
		}
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(append(append([]string(nil), path...), "--context", "kind-test"), deps); err != nil || *loads != 1 {
			t.Fatalf("%v: err=%v loads=%d diagnostics=%s", path, err, *loads, &diagnostics)
		}
		for _, want := range []string{"version running", "0.2.0", "deployments", "orka-controller=1/1", "providers", "local=true"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("%v report lacks %q: %s", path, want, &out)
			}
		}
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		reports = append(reports, out.String())
		reads = append(reads, string(data))
	}
	if reports[0] != reports[1] || reads[0] != reads[1] {
		t.Fatalf("status report or reads changed: reports=%q reads=%q", reports, reads)
	}
}

func TestCLIMigrationPushMalformedNearestMarkerStopsDiscovery(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	t.Setenv("KMX_HOME", state)
	instructions := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(instructions, []byte("Answer briefly.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"create", "greeter", "--workspace", root, "--model", "test-model", "--instructions", instructions}, deps); err != nil {
		t.Fatal(err)
	}
	nearest := filepath.Join(root, "nested")
	cwd := filepath.Join(nearest, "deep")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nearest, "workspace.yaml"), []byte("invalid canary: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	out.Reset()
	layout := filepath.Join(t.TempDir(), "layout")
	// A valid ancestor must not hide an invalid nearer marker, even when the
	// destination is a registry that would otherwise require network access.
	for _, flags := range [][]string{nil, {"--to-layout", layout}} {
		err := execute(append([]string{"push", "registry.invalid/team:v1"}, flags...), deps)
		if err == nil || !strings.Contains(err.Error(), "invalid workspace descriptor") || strings.Contains(err.Error(), "canary") || *loads != 0 || out.Len() != 0 || diagnostics.Len() != 0 {
			t.Fatalf("nearest marker refusal: err=%v loads=%d stdout=%q stderr=%q", err, *loads, &out, &diagnostics)
		}
	}
	if _, err := os.Stat(layout); !os.IsNotExist(err) {
		t.Fatal("malformed marker wrote destination")
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatalf("malformed marker prepared workspace state: %v %v", entries, err)
	}
}
