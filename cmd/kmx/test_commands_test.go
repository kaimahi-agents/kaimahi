package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

const legacyEvalNotice = "kmx eval is deprecated; use kmx test (same arguments).\n"

// Missing canonical registration, shared flag storage, or a leaked compatibility
// route must fail without performing an operation.
func TestTestCommandTreesAndDiscovery(t *testing.T) {
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	root := newRootCommand(&commandState{deps: deps})
	canonical, _, err := root.Find([]string{"test"})
	if err != nil || canonical == root || canonical.Name() != "test" {
		t.Fatalf("canonical test missing: %v", err)
	}
	legacy, _, err := root.Find([]string{"eval"})
	if err != nil || !legacy.Hidden || canonical.Hidden || len(canonical.Aliases) != 0 {
		t.Fatalf("independent tree visibility: err=%v canonical=%v legacy=%v", err, canonical, legacy)
	}
	for _, cmd := range []*cobra.Command{canonical, legacy} {
		if !strings.Contains(cmd.Long, "use test explain") || strings.Contains(cmd.Long, "eval explain") {
			t.Fatalf("%s help teaches legacy failure inspection: %s", cmd.Name(), cmd.Long)
		}
	}
	for _, child := range []string{"", "cleanup", "diff", "explain", "export", "verify", "from-sessions"} {
		left, right := canonical, legacy
		if child != "" {
			left, _, err = canonical.Find([]string{child})
			if err != nil || left == canonical {
				t.Fatalf("missing canonical helper %s: %v", child, err)
			}
			right, _, err = legacy.Find([]string{child})
			if err != nil || right == legacy {
				t.Fatalf("missing legacy helper %s: %v", child, err)
			}
		}
		// Canonical Test additionally owns file preview; workspace helpers stay identical.
		if left == right || child != "" && left.Flags().FlagUsages() != right.Flags().FlagUsages() {
			t.Fatalf("helper %q shares commands or has different flags", child)
		}
		for _, flag := range []string{"workspace", "out", "output", "to-context", "sessions", "suite"} {
			if left.Flags().Lookup(flag) == nil {
				continue
			}
			before := right.Flags().Lookup(flag).Value.String()
			if err := left.Flags().Set(flag, "eval"); err != nil {
				t.Fatal(err)
			}
			if right.Flags().Lookup(flag).Changed || right.Flags().Lookup(flag).Value.String() != before {
				t.Fatalf("%s --%s shares mutable flag storage", child, flag)
			}
		}
	}
	for _, name := range []string{"case", "provider", "endpoint"} {
		if canonical.Flag(name) == nil || legacy.Flag(name) != nil {
			t.Fatalf("file flag %s leaked into legacy workspace eval", name)
		}
	}
	for _, args := range [][]string{{"--help"}, {"__complete", ""}, {"__complete", "help", ""}} {
		out.Reset()
		diag.Reset()
		deps, loads := testDependencies(&out, &diag)
		if err := execute(args, deps); err != nil || *loads != 0 {
			t.Fatalf("discovery operational: %v loads=%d", err, *loads)
		}
		if !strings.Contains(out.String(), "test") || strings.Contains(out.String(), "\n  eval ") || strings.Contains(out.String(), "\neval\t") || strings.Contains(diag.String(), legacyEvalNotice) {
			t.Fatalf("wrong discovery: stdout=%q stderr=%q", &out, &diag)
		}
	}
}

// Args runs before required-flag and operational hooks; flag/help handlers cover
// Cobra's earlier exits. Every node must own these wrappers, not inherit hooks.
func TestTestLegacyNoticeAtEveryNode(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent-state")
	t.Setenv("KMX_HOME", home)
	t.Chdir(t.TempDir())
	for _, child := range []string{"", "cleanup", "diff", "explain", "export", "verify", "from-sessions"} {
		for _, verb := range []string{"eval", "test"} {
			path := []string{verb}
			if child != "" {
				path = append(path, child)
			}
			for _, tail := range [][]string{{"--help"}, {"--unknown"}, {"--output"}, nil, {"one", "two", "three"}, {"--container-engine="}} {
				args := append(append([]string{}, path...), tail...)
				t.Run(strings.Join(args, " "), func(t *testing.T) {
					var out, diag bytes.Buffer
					deps, loads := testDependencies(&out, &diag)
					err := execute(args, deps)
					help := len(tail) > 0 && tail[0] == "--help"
					if (err == nil) != help || *loads != 0 || !help && out.Len() != 0 {
						t.Fatalf("unexpected boundary: err=%v loads=%d stdout=%q", err, *loads, &out)
					}
					want := ""
					if verb == "eval" {
						want = legacyEvalNotice
					}
					if diag.String() != want {
						t.Fatalf("stderr=%q want %q", &diag, want)
					}
					if help && strings.Contains(out.String(), "kmx eval") {
						// Cobra's generated usage retains the invoked spelling;
						// authored examples must teach only the canonical route.
						cmd, _, _ := newRootCommand(&commandState{deps: deps}).Find(path)
						if strings.Contains(cmd.Example+cmd.Long, "kmx eval") || strings.Contains(cmd.Long, "Use eval cleanup") {
							t.Fatalf("legacy help teaches retired spelling: %s", &out)
						}
					}
				})
			}
			for _, args := range [][]string{append([]string{"help"}, path...), append(append([]string{}, path...), "frob", "--help")} {
				var out, diag bytes.Buffer
				deps, loads := testDependencies(&out, &diag)
				if err := execute(args, deps); err != nil || *loads != 0 {
					t.Fatalf("explicit help failed: %v loads=%d", err, *loads)
				}
				want := ""
				if verb == "eval" {
					want = legacyEvalNotice
				}
				if diag.String() != want {
					t.Fatalf("%v: stderr=%q want %q", args, &diag, want)
				}
			}
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("help/refusal created state: %v", err)
	}
}

func TestTestLegacyNoticeRequiredFlagsAndOfflineHelpers(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	root, output := suiteEvalSource(t)
	for _, tail := range [][]string{
		{"explain", "--clear"},
		{"explain", "../../invalid"},
		{"from-sessions", "uid"},
		{"export", "--member", "greeter"},
		{"export", "--out", output},
		{"verify", "--suite", "missing", "--evidence", "missing"},
		{"diff", "missing-before", "missing-after"},
		{"export", "--workspace", root, "--member", "greeter", "--out", output},
		{"verify", "--suite", "missing", "--evidence", "missing", "--expect", "missing", "-o", "json"},
		{"from-sessions", "uid", "--sessions", "127.0.0.1:8080", "--assertions", "missing.yaml", "--out", output},
		{"cleanup", "--to-context", "kind-eval"},
		{"frob", "extra"},
		{"--", "eval", "extra"},
	} {
		var legacyError, legacyOut, legacyDiag string
		for _, verb := range []string{"eval", "test"} {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			boundary := errors.New("cleanup configured boundary")
			deps.loadConfig = func(string, string) (*config.Config, error) { *loads++; return nil, boundary }
			err := execute(append([]string{verb}, tail...), deps)
			if err == nil || out.Len() != 0 || *loads != 0 && tail[0] != "cleanup" {
				t.Fatalf("%s %v: err=%v loads=%d stdout=%q", verb, tail, err, *loads, &out)
			}
			wantCount := 0
			if verb == "eval" {
				wantCount = 1
			}
			if strings.Count(diag.String(), legacyEvalNotice) != wantCount {
				t.Fatalf("%s %v notice: %q", verb, tail, &diag)
			}
			cleanDiag := strings.TrimPrefix(diag.String(), legacyEvalNotice)
			if verb == "eval" {
				legacyError, legacyOut, legacyDiag = err.Error(), out.String(), cleanDiag
			} else if err.Error() != legacyError || out.String() != legacyOut || cleanDiag != legacyDiag {
				t.Fatalf("%v helper behavior changed: err=%v legacy=%s stdout=%q stderr=%q legacy=%q", tail, err, legacyError, &out, cleanDiag, legacyDiag)
			}
		}
	}
}

func TestTestExplicitHelpRefusalsNoticeOnce(t *testing.T) {
	for _, verb := range []string{"eval", "test"} {
		for _, tail := range [][]string{{"--unknown"}, {"diff", "--unknown"}, {"--container-engine="}, {"export", "--container-engine="}} {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			args := append([]string{"help", verb}, tail...)
			err := execute(args, deps)
			want := ""
			if verb == "eval" {
				want = legacyEvalNotice
			}
			if err == nil || *loads != 0 || out.Len() != 0 || diag.String() != want {
				t.Fatalf("%v: err=%v loads=%d stdout=%q stderr=%q want=%q", args, err, *loads, &out, &diag, want)
			}
		}
	}
}

func TestTestCompletionIsInert(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent-state")
	t.Setenv("KMX_HOME", home)
	for _, verb := range []string{"test", "eval"} {
		for _, mode := range []string{"__complete", "__completeNoDesc"} {
			for _, tail := range [][]string{{""}, {"--judge-base-url", ""}, {"cleanup", ""}, {"diff", ""}, {"explain", ""}, {"export", "--member", ""}, {"verify", "--output", ""}, {"from-sessions", "--sessions", ""}, {"--context="}, {"--help"}} {
				var out, diag bytes.Buffer
				deps, loads := testDependencies(&out, &diag)
				args := append([]string{mode, verb}, tail...)
				if err := execute(args, deps); err != nil || *loads != 0 || strings.Contains(diag.String()+out.String(), legacyEvalNotice) {
					t.Fatalf("%v completion operational/noticed: err=%v loads=%d stdout=%q stderr=%q", args, err, *loads, &out, &diag)
				}
			}
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("completion created state: %v", err)
	}
}

func TestTestLegacyNoticeMalformedContextAndOpaqueValues(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		legacy bool
	}{
		{[]string{"eval", "--context="}, true},
		{[]string{"eval", "cleanup", "--context"}, true},
		{[]string{"eval", "verify", "-context", ""}, true},
		{[]string{"--context=", "eval"}, true},
		{[]string{"--container-engine", "podman", "--context=", "eval", "diff"}, true},
		{[]string{"--context", "", "--container-engine=podman", "eval"}, true},
		{[]string{"help", "eval", "export", "--context="}, true},
		{[]string{"--context=", "help", "eval"}, true},
		{[]string{"--help", "--context=", "eval"}, true},
		{[]string{"test", "--context="}, false},
		{[]string{"create", "eval", "--context="}, false},
		{[]string{"run", "--agent", "eval", "--context="}, false},
		{[]string{"--context", "eval", "--context="}, false},
		{[]string{"--container-engine", "eval", "--context="}, false},
		{[]string{"--unknown", "eval", "--context="}, false},
		{[]string{"help=opaque", "eval", "--context="}, false},
		{[]string{"help", "test", "eval", "--context="}, false},
		{[]string{"--context=", "--", "eval"}, false},
	} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		err := execute(tc.args, deps)
		want := ""
		if tc.legacy {
			want = legacyEvalNotice
		}
		if err == nil || err.Error() != "--context needs a context name" || exitCode(err) != 1 || *loads != 0 || out.Len() != 0 || diag.String() != want {
			t.Fatalf("%v: err=%v loads=%d stdout=%q stderr=%q want=%q", tc.args, err, *loads, &out, &diag, want)
		}
	}
	for _, args := range [][]string{{"test", "--unknown", "eval"}, {"test", "--", "eval"}, {"test", "--workspace", "eval", "--output=yaml"}, {"help", "test"}, {"help", "frob", "eval"}, {"help", "--unknown", "eval"}, {"--unknown", "eval"}, {"--container-engine", "eval"}, {"--context", "eval"}, {"--", "eval"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		_ = execute(args, deps)
		if *loads != 0 || strings.Contains(diag.String(), legacyEvalNotice) {
			t.Fatalf("opaque eval was treated as legacy: %v loads=%d stderr=%q", args, *loads, &diag)
		}
	}
}

func TestTestEvaluationJSONAndRecoveryCompatibility(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	root, _ := suiteEvalSource(t)
	var legacyJSON []byte
	for _, verb := range []string{"eval", "test"} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		deps.executable = "/build with space/kmx"
		deps.loadConfig = func(ctx, engine string) (*config.Config, error) {
			*loads++
			return &config.Config{KubeContext: ctx, ContainerEngine: engine, KindCluster: "demo"}, nil
		}
		var operation *app.App
		deps.newApp = func(cfg *config.Config) *app.App {
			cfg.KubeContext = "different" // no native call; report preflight abort JSON
			operation = app.New(cfg)
			return operation
		}
		args := []string{"--container-engine", "podman", verb, "--workspace", root, "--to-context", "eval", "--inference", "provider:eval", "-o", "json"}
		err := execute(args, deps)
		var report map[string]any
		if err == nil || *loads != 1 || operation == nil || json.Unmarshal(out.Bytes(), &report) != nil || report["status"] != "failed" {
			t.Fatalf("%s missing JSON refusal: err=%v loads=%d stdout=%q", verb, err, *loads, &out)
		}
		if verb == "eval" {
			legacyJSON = append([]byte(nil), out.Bytes()...)
		} else if !bytes.Equal(legacyJSON, out.Bytes()) {
			t.Fatalf("machine JSON changed: legacy=%s canonical=%s", legacyJSON, &out)
		}
		wantCommand := "KIND_CLUSTER=demo CONTAINER_ENGINE=podman '/build with space/kmx' --container-engine podman " + verb + " --workspace " + root + " --to-context eval --inference provider:eval -o json"
		if operation.InvocationCommand != wantCommand || operation.TestPresentation != (verb == "test") {
			t.Fatalf("%s recovery rewrote opaque values/invoked route or lost presentation selection: %s test=%v", verb, operation.InvocationCommand, operation.TestPresentation)
		}
		want := 0
		if verb == "eval" {
			want = 1
		}
		if strings.Count(diag.String(), legacyEvalNotice) != want {
			t.Fatalf("operational notice: %q", &diag)
		}
	}
}

func TestTestCleanupRecoveryUsesInvokedRoot(t *testing.T) {
	for _, verb := range []string{"eval", "test"} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		deps.executable = "/build with space/kmx"
		deps.loadConfig = func(string, string) (*config.Config, error) {
			*loads++
			// Empty context refuses before any kubectl command.
			return &config.Config{}, nil
		}
		var operation *app.App
		deps.newApp = func(cfg *config.Config) *app.App {
			operation = app.New(cfg)
			return operation
		}
		err := execute([]string{verb, "cleanup", "--to-context", "eval's context", "--to-namespace", "team"}, deps)
		if err == nil || *loads != 1 || operation == nil || out.Len() != 0 {
			t.Fatalf("cleanup did not reach safe configured refusal: err=%v loads=%d stdout=%q", err, *loads, &out)
		}
		want := "'/build with space/kmx' " + verb + " cleanup --to-context 'eval'\"'\"'s context' --to-namespace team"
		if operation.InvocationCommand != want || operation.TestPresentation != (verb == "test") {
			t.Fatalf("%s cleanup lost invoked root/quoting/presentation: %q test=%v", verb, operation.InvocationCommand, operation.TestPresentation)
		}
	}
}

func TestTestUsagePreservesInvokedSpelling(t *testing.T) {
	for _, verb := range []string{"test", "eval"} {
		for _, tail := range [][]string{{"diff", "one"}, {"cleanup", "one", "two"}, {"from-sessions"}} {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			err := execute(append([]string{verb}, tail...), deps)
			if err == nil || !strings.HasPrefix(err.Error(), "usage: kmx "+verb+" "+tail[0]+" ") || out.Len() != 0 || *loads != 0 {
				t.Fatalf("%s %v wrong usage boundary: err=%v stdout=%q loads=%d", verb, tail, err, &out, *loads)
			}
		}
	}
}

func TestTestRecoveryArgsRetainOpaqueValuesAndRedactJudge(t *testing.T) {
	for _, verb := range []string{"eval", "test"} {
		args := []string{verb, "--workspace", "eval", "--bindings", "proof's eval.yaml", "--judge-base-url", "https://private.invalid/eval", "--judge-key-env", "eval", "--judge-base-url=https://other.invalid/eval", "--out", "eval"}
		want := []string{verb, "--workspace", "eval", "--bindings", "proof's eval.yaml", "--judge-key-env", "eval", "--out", "eval"}
		if got := suiteEvaluationRecoveryArgs(args); !reflect.DeepEqual(got, want) {
			t.Fatalf("redaction changed opaque values: got=%q want=%q", got, want)
		}
	}
}

// Helper hooks must not suppress the legacy notice, even if an operational
// ancestor is later added; idempotence also covers an Args call followed by help.
func TestTestLegacyArgsNoticeIndependentOfHooks(t *testing.T) {
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	root := newRootCommand(&commandState{deps: deps})
	legacy, _, _ := root.Find([]string{"eval"})
	legacy.PersistentPreRunE = func(*cobra.Command, []string) error { t.Fatal("helper inherited operational hook"); return nil }
	for _, child := range legacy.Commands() {
		if child.Args == nil {
			t.Fatalf("%s missing notice Args wrapper", child.Name())
		}
	}
	diff, _, _ := legacy.Find([]string{"diff"})
	_ = diff.Args(diff, nil)
	_ = diff.Help()
	out.Reset()
	root.SetArgs([]string{"eval", "diff", "missing-before", "missing-after"})
	if err := root.Execute(); err == nil || err.Error() != "invalid evaluation receipt" {
		t.Fatalf("helper did not retain offline dispatch: %v", err)
	}
	if diag.String() != legacyEvalNotice {
		t.Fatalf("notice repeated across Args/help: %q", &diag)
	}
}
