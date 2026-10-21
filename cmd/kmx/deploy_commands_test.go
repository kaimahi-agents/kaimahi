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

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/spf13/cobra"
)

const legacyLiftNotice = "kmx lift is deprecated; use kmx deploy (same arguments).\n"

// Removing canonical registration or leaking the compatibility route into discovery
// must fail without ever loading operational configuration.
func TestDeployDiscoveryAndLegacyHelpAreInert(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"__complete", ""}, {"deploy", "--help"}, {"help", "deploy"}, {"lift", "--help"}, {"help", "lift"}, {"__complete", "lift", "--"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			if err := execute(args, deps); err != nil || *loads != 0 {
				t.Fatalf("help/completion was operational: %v, loads=%d", err, *loads)
			}
			discovery := len(args) == 1 || len(args) == 2 && args[0] == "__complete"
			if discovery {
				if !strings.Contains(out.String(), "deploy") || strings.Contains(out.String(), "lift") {
					t.Fatalf("canonical discovery: %s", &out)
				}
			} else {
				for _, flag := range []string{"--evidence", "--plan", "--update", "--to-context", "--show-content"} {
					if !strings.Contains(out.String(), flag) {
						t.Errorf("missing %s in %s", flag, &out)
					}
				}
			}
			wantNotice := args[0] == "lift" || args[0] == "help" && args[1] == "lift"
			if strings.Count(diag.String(), legacyLiftNotice) != map[bool]int{true: 1, false: 0}[wantNotice] {
				t.Fatalf("notice count: %q", &diag)
			}
		})
	}
}

// Notice emission must survive Cobra's early flag/argument refusals and must
// not change their empty stdout, failure, or offline boundary.
func TestDeployLegacyNoticeAndRefusals(t *testing.T) {
	for _, tc := range []struct {
		tail  []string
		want  string
		typed error
	}{
		{nil, "workspace VERB requires explicit --to NAME", nil},
		{[]string{"one", "two"}, "usage: kmx VERB --to NAME | kmx VERB <ref>@<digest> (--to NAME | --to-context <ctx>) (--inference provider:NAME | --bindings FILE) --evidence FILE [--from-layout DIR] [--promote] [--update] [--plan]", nil},
		{[]string{"--unknown"}, "unknown flag: --unknown", nil},
		{[]string{"--to", "prod", "--output", "yaml"}, "VERB output must be text or json", nil},
		{[]string{"--to", ""}, "VERB requires non-empty --to", nil},
		{[]string{"--context", "other"}, "--context cannot be used with suite VERB; use --to-context", nil},
		{[]string{"--container-engine", ""}, "--container-engine requires docker or podman", nil},
		{[]string{"repo/suite@sha256:" + strings.Repeat("a", 64), "--to-context", "target", "--inference", "provider:inference"}, "release lift planning refused; no native admission or placement: explicit-reference VERB requires --evidence FILE", app.ErrReleaseLift},
	} {
		for _, verb := range []string{"deploy", "lift"} {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			err := execute(append([]string{verb}, tc.tail...), deps)
			wantError := strings.ReplaceAll(tc.want, "VERB", verb)
			if err == nil || err.Error() != wantError || exitCode(err) != 1 || *loads != 0 || out.Len() != 0 {
				t.Errorf("%s %v: err=%v want=%q loads=%d stdout=%q", verb, tc.tail, err, wantError, *loads, &out)
			}
			if tc.typed != nil && !errors.Is(err, tc.typed) {
				t.Errorf("%s %v: lost typed refusal %v: %v", verb, tc.tail, tc.typed, err)
			}
			want := ""
			if verb == "lift" {
				want = legacyLiftNotice
			}
			if diag.String() != want {
				t.Errorf("%s %v: stderr=%q want %q", verb, tc.tail, &diag, want)
			}
		}
	}
}

func TestDeployConsoleHelpUsesRetirementCopy(t *testing.T) {
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	if err := execute([]string{"console", "--help"}, deps); err != nil || *loads != 0 {
		t.Fatalf("console help was operational: %v loads=%d", err, *loads)
	}
	if !strings.Contains(out.String(), "Deploy moved: kmx deploy <ref>@<digest> --to-context … --evidence …") || strings.Contains(out.String(), "Lift moved:") {
		t.Fatalf("console help differs from current retirement notice: %s", &out)
	}
}

func TestDeployLegacyNoticeOnMalformedContext(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		legacy bool
	}{
		{[]string{"lift", "--context="}, true},
		{[]string{"lift", "--context"}, true},
		{[]string{"lift", "--context", ""}, true},
		{[]string{"lift", "-context="}, true},
		{[]string{"lift", "-context", ""}, true},
		{[]string{"--context=", "lift"}, true},
		{[]string{"-context", "", "lift"}, true},
		{[]string{"--container-engine", "podman", "--context=", "lift"}, true},
		{[]string{"--context", "", "--container-engine=podman", "lift"}, true},
		{[]string{"help", "lift", "--context="}, true},
		{[]string{"--context=", "help", "lift"}, true},
		{[]string{"--help", "--context=", "lift"}, true},
		{[]string{"deploy", "--context="}, false},
		{[]string{"deploy", "--context"}, false},
		{[]string{"deploy", "--context", ""}, false},
		{[]string{"--context=", "deploy"}, false},
		{[]string{"--container-engine=podman", "--context", "", "deploy"}, false},
		// A command argument or flag value equal to lift is never a route.
		{[]string{"create", "lift", "--context="}, false},
		{[]string{"run", "--agent", "lift", "--context="}, false},
		{[]string{"--context", "lift", "--context="}, false},
		{[]string{"--container-engine", "lift", "--context="}, false},
		{[]string{"--unknown", "lift", "--context="}, false},
		{[]string{"help=opaque", "lift", "--context="}, false},
		{[]string{"--context=", "--", "lift"}, false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			err := execute(tc.args, deps)
			if err == nil || err.Error() != "--context needs a context name" || exitCode(err) != 1 {
				t.Fatalf("context refusal changed: %v", err)
			}
			if *loads != 0 || out.Len() != 0 {
				t.Fatalf("context refusal was operational: loads=%d stdout=%q", *loads, &out)
			}
			want := ""
			if tc.legacy {
				want = legacyLiftNotice
			}
			if diag.String() != want {
				t.Fatalf("notice: stderr=%q want %q", &diag, want)
			}
		})
	}
}

// The two spellings must reach the same frozen destination/update selection;
// unsupported modes must still refuse before configuration.
func TestDeployDigestDispatchCompatibility(t *testing.T) {
	ref := "repo/suite@sha256:" + strings.Repeat("a", 64)
	for _, mode := range [][]string{nil, {"--plan"}, {"--update"}, {"--update", "--plan"}, {"--bindings", "author.yaml", "--update"}} {
		for _, verb := range []string{"deploy", "lift"} {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			boundary := errors.New("configured digest boundary")
			deps.loadConfig = func(ctx, engine string) (*config.Config, error) {
				*loads++
				if ctx != "target" || engine != "podman" {
					t.Fatalf("selectors changed: %q %q", ctx, engine)
				}
				return nil, boundary
			}
			args := []string{verb, ref, "--to-context", "target", "--evidence", "private.json", "--container-engine", "podman"}
			unsupported := len(mode) > 0 && mode[0] == "--bindings"
			if !unsupported {
				args = append(args, "--inference", "provider:inference")
			}
			err := execute(append(args, mode...), deps)
			if unsupported {
				if !errors.Is(err, app.ErrReleaseLift) || *loads != 0 {
					t.Fatalf("unsupported update reached operation: %v loads=%d", err, *loads)
				}
			} else if !errors.Is(err, boundary) || *loads != 1 {
				t.Fatalf("%s %v dispatch: %v loads=%d", verb, mode, err, *loads)
			}
			want := ""
			if verb == "lift" {
				want = legacyLiftNotice
			}
			if out.Len() != 0 || diag.String() != want {
				t.Fatalf("result/notice changed: stdout=%q stderr=%q", &out, &diag)
			}
		}
	}
}

// A real cold plan tests formatting separately from the frozen JSON disclosure,
// including data deliberately containing the old spelling.
func TestDeployWorkspacePlanCompatibility(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	root, _ := suiteEvalSource(t)
	t.Chdir(root)
	raw := strings.ReplaceAll(composedDestinationConfig, "registry.example/agents", "registry.example/lift")
	if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	for _, update := range []bool{false, true} {
		var legacyJSON []byte
		for _, verb := range []string{"lift", "deploy"} {
			for _, output := range []string{"json", "text"} {
				var out, diag bytes.Buffer
				deps, loads := testDependencies(&out, &diag)
				args := []string{verb, "--to", "prod", "--plan", "-o", output}
				if update {
					args = append(args, "--update")
				}
				if err := execute(args, deps); err != nil || *loads != 0 {
					t.Fatalf("cold %s plan: %v loads=%d", verb, err, *loads)
				}
				stderr := strings.TrimPrefix(diag.String(), legacyLiftNotice)
				if output == "json" {
					var result map[string]any
					if err := json.Unmarshal(out.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					plan := result["plan"].(map[string]any)
					if result["lift"] == nil || result["deploy"] != nil || !strings.HasPrefix(plan["disclosure"].(string), "eval,push,lift;") || plan["repository"] != "registry.example/lift" {
						t.Fatalf("machine identity changed: %s", &out)
					}
					if verb == "lift" {
						legacyJSON = append([]byte(nil), out.Bytes()...)
					} else if !bytes.Equal(legacyJSON, out.Bytes()) {
						t.Fatalf("canonical JSON changed:\n%s\n%s", legacyJSON, &out)
					}
					if stderr != "" {
						t.Fatalf("JSON stages leaked: %q", stderr)
					}
				} else {
					label, disclosure := "Lift", "eval,push,lift;"
					if verb == "deploy" {
						label, disclosure = "Deploy", "eval,push,deploy;"
					}
					if out.Len() != 0 || len(strings.Split(strings.TrimSpace(stderr), "\n")) != 4 || !strings.Contains(stderr, "\n"+label+" · not-attempted · prod unchanged\n") || !strings.Contains(stderr, disclosure) || !strings.Contains(stderr, "registry.example/lift") {
						t.Fatalf("%s text contract: stdout=%q stderr=%q", verb, &out, stderr)
					}
				}
			}
		}
	}
}

func TestDeployWorkspaceExecutionKeepsFrozenSelections(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	root, _ := suiteEvalSource(t)
	t.Chdir(root)
	path := filepath.Join(root, "kmx.yaml")
	for _, verb := range []string{"deploy", "lift"} {
		if err := os.WriteFile(path, []byte(composedDestinationConfig), 0600); err != nil {
			t.Fatal(err)
		}
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		boundary := errors.New("frozen workspace execution boundary")
		deps.loadConfig = func(ctx, _ string) (*config.Config, error) {
			*loads++
			if ctx != "explicit-prod" {
				t.Fatalf("destination override lost: %q", ctx)
			}
			if err := os.WriteFile(path, []byte("broken: [\n"), 0600); err != nil {
				t.Fatal(err)
			}
			return nil, boundary
		}
		if err := execute([]string{verb, "--to", "prod", "--to-context", "explicit-prod", "--update"}, deps); !errors.Is(err, boundary) || *loads != 1 || out.Len() != 0 {
			t.Fatalf("%s selection: %v loads=%d stdout=%q", verb, err, *loads, &out)
		}
	}
}

func TestDeployAndLegacyFlagsMatch(t *testing.T) {
	var out bytes.Buffer
	deps, _ := testDependencies(&out, &out)
	root := newRootCommand(&commandState{deps: deps})
	deploy, _, err := root.Find([]string{"deploy"})
	if err != nil || deploy == root {
		t.Fatalf("canonical command missing: %v", err)
	}
	legacy, _, err := root.Find([]string{"lift"})
	if err != nil || !legacy.Hidden || deploy.Hidden {
		t.Fatalf("visibility: deploy=%v lift=%v err=%v", deploy.Hidden, legacy.Hidden, err)
	}
	if !reflect.DeepEqual(deploy.Flags().FlagUsages(), legacy.Flags().FlagUsages()) {
		t.Fatal("compatibility flags diverged")
	}
}

func TestDeployDigestResultStdoutAndExitCompatibility(t *testing.T) {
	summary := app.ReleaseLiftSummary{SuiteDigest: "sha256:abc", Member: "lift", Target: app.ReleaseLiftTarget{Context: "lift", Namespace: "team"}}
	result := app.ReleaseLiftResult{Summary: summary, State: "partial", Resources: []app.ReleaseLiftOutcome{{ReleaseLiftResource: app.ReleaseLiftResource{Kind: "Agent", Name: "lift", UID: "uid"}, Outcome: "created"}}}
	refusal := errors.New("retained partial outcome")
	var legacyJSON []byte
	for _, verb := range []string{"lift", "deploy"} {
		for _, output := range []string{"text", "json"} {
			var out, diag bytes.Buffer
			cmd := &cobra.Command{Use: verb}
			cmd.SetOut(&out)
			cmd.SetErr(&diag)
			err := reportReleaseLiftResult(cmd, output, summary, result, refusal)
			if !errors.Is(err, refusal) || exitCode(err) != 1 || diag.Len() != 0 {
				t.Fatalf("result changed failure/streams: %v stderr=%q", err, &diag)
			}
			if output == "json" {
				if verb == "lift" {
					legacyJSON = append([]byte(nil), out.Bytes()...)
				} else if !bytes.Equal(legacyJSON, out.Bytes()) {
					t.Fatalf("JSON changed: %s != %s", legacyJSON, &out)
				}
				continue
			}
			label := "Lift"
			if verb == "deploy" {
				label = "Deploy"
			}
			want := label + ": partial\nSuite: sha256:abc\nMember: lift\nTarget: \"lift\"/\"team\"\nAgent/lift: created (ready=false, UID=\"uid\")\n"
			if out.String() != want {
				t.Fatalf("%s stdout changed opaque data: got %q want %q", verb, &out, want)
			}
		}
	}
}

func TestDeployRecoveryPreservesExecutableSelectorsAndQuoting(t *testing.T) {
	for _, verb := range []string{"lift", "deploy"} {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		deps.executable = "/build with space/kmx"
		deps.loadConfig = func(ctx, engine string) (*config.Config, error) {
			return &config.Config{KubeContext: ctx, ContainerEngine: engine, KindCluster: "demo"}, nil
		}
		var operation *app.App
		deps.newApp = func(cfg *config.Config) *app.App {
			operation = app.New(cfg)
			return operation
		}
		ref := "repo/lift@sha256:" + strings.Repeat("a", 64)
		err := execute([]string{"--container-engine", "podman", verb, ref, "--to-context", "lift", "--inference", "provider:lift", "--evidence", "proof's lift.json", "--plan"}, deps)
		if !errors.Is(err, app.ErrReleaseLift) || operation == nil {
			t.Fatalf("missing-evidence plan reached wrong boundary: %v", err)
		}
		want := "KIND_CLUSTER=demo CONTAINER_ENGINE=podman '/build with space/kmx' --container-engine podman deploy " + ref + " --to-context lift --inference provider:lift --evidence 'proof'\"'\"'s lift.json' --plan"
		if operation.InvocationCommand != want {
			t.Fatalf("recovery changed executable/selectors/quoting:\ngot %s\nwant %s", operation.InvocationCommand, want)
		}
	}
}
