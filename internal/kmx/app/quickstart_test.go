package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// An unrecognised --output is refused BEFORE a cluster is created. Being told
// "unknown output" four minutes into a bring-up would be the worst possible
// moment to find out.
func TestQuickstartRejectsAnUnknownOutputBeforeDoingAnything(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KMX_TOOLCHAIN", "off")
	a := &App{Cfg: &config.Config{ContainerEngine: "docker"}, Run: &run.Runner{}, Err: &bytes.Buffer{}, Out: &bytes.Buffer{}}
	err := a.Quickstart(QuickstartOptions{Output: "yaml"})
	if err == nil || !strings.Contains(err.Error(), "unknown --output") {
		t.Fatalf("got %v, want a refusal naming the output", err)
	}
}

func TestLocalClusterCreationRefusesDisagreeingContextBeforeCommands(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		for _, entry := range []string{"quickstart", "up", "up cluster", "stepCluster"} {
			t.Run(engine+"/"+entry, func(t *testing.T) {
				dir := t.TempDir()
				log := filepath.Join(dir, "commands")
				t.Setenv("KMX_IDENTITY_LOG", log)
				t.Setenv("KMX_TOOLCHAIN", "off")
				for _, tool := range []string{"kind", "kubectl", "helm", engine} {
					fakeTool(t, dir, tool, `printf '%s\n' "$0 $*" >> "$KMX_IDENTITY_LOG"; exit 99`)
				}
				t.Setenv("PATH", dir)
				a := &App{Cfg: &config.Config{KindCluster: "other", KubeContext: "kind-reviewed", ContainerEngine: engine},
					Run: &run.Runner{}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
				var err error
				switch entry {
				case "quickstart":
					err = a.Quickstart(QuickstartOptions{})
				case "up":
					err = a.Up("")
				case "up cluster":
					err = a.Up("cluster")
				case "stepCluster":
					err = a.stepCluster()
				}
				if err == nil || !strings.Contains(err.Error(), "does not identify kind cluster") || !strings.Contains(err.Error(), "kind-other") {
					t.Fatalf("identity disagreement was not refused: %v", err)
				}
				if commands, err := os.ReadFile(log); !os.IsNotExist(err) {
					t.Fatalf("ran commands before refusing identity: %s (%v)", commands, err)
				}
			})
		}
	}
}

func TestInstallKagentStillAcceptsManagedContext(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "helm-commands")
	t.Setenv("KMX_IDENTITY_LOG", log)
	fakeTool(t, dir, "helm", `printf '%s\n' "$*" >> "$KMX_IDENTITY_LOG"`)
	fakeTool(t, dir, "kubectl", `exit 99`)
	t.Setenv("PATH", dir)
	a := &App{Cfg: &config.Config{KindCluster: "unrelated-local", KubeContext: "managed-aks", KagentVersion: config.DefaultKagentVersion},
		Run: &run.Runner{}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := a.installKagent(); err != nil {
		t.Fatalf("shared managed-cluster installation was blocked: %v", err)
	}
	commands, err := os.ReadFile(log)
	if err != nil || strings.Count(string(commands), "--kube-context managed-aks") != 2 {
		t.Fatalf("Helm did not retain the managed context: %s (%v)", commands, err)
	}
	if !strings.Contains(string(commands), "--wait --wait-for-jobs --timeout 420s") {
		t.Fatalf("application install did not wait for its workloads/jobs: %s", commands)
	}
}

func TestQuickstartDeletionConsequenceSurvivesRichAndPlainOutput(t *testing.T) {
	for _, rich := range []bool{false, true} {
		var out bytes.Buffer
		a := &App{Cfg: &config.Config{KindCluster: "test", KubeContext: "kind-test", ContainerEngine: "podman"}, Err: &out}
		a.quickstartNext(cliui.WithCapabilities(cliui.Capabilities{Rich: rich, Width: 50}), QuickstartResult{Next: []string{"chat", "orka install", "up"}})
		for _, want := range []string{"delete the cluster and everything in it", a.operationCommand("down"), "prerequisite for Orka authoring", "docs/kmx.md#kmx-agent-create"} {
			if !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(out.String(), "│", "")), " "), want) {
				t.Errorf("rich=%v lost %q:\n%s", rich, want, out.String())
			}
		}
	}
}

// The structured result is what an agent in a harness reads. Two fields carry
// weight beyond their type: `ok` is the whole answer to "did this work", and
// `governed` remains false for governance enabled by this command. Its
// invocation-only meaning is documented without extending the wire schema.
func TestQuickstartResultScopesGovernanceToThisInvocation(t *testing.T) {
	// A struct literal written by the test says nothing about what kmx
	// reports, so the claim is read where it is actually made: at the one
	// place a result is built.
	source, err := os.ReadFile("quickstart.go")
	if err != nil {
		t.Fatal(err)
	}
	governed := regexp.MustCompile(`Governed:\s*(\w+)`).FindAllStringSubmatch(string(source), -1)
	if len(governed) == 0 {
		t.Fatal("no result sets Governed at all; the field would then marshal as false by accident rather than by decision")
	}
	for _, set := range governed {
		if set[1] != "false" {
			t.Errorf("the fast path deploys no plane, but a result claims Governed: %s", set[1])
		}
	}

	// The wire contract in both directions. A harness parses this document,
	// so a field that vanishes breaks it and a field that appears unannounced
	// is one nobody decided to publish. A zero value is marshalled on purpose:
	// every key must survive it, which is what forbids `omitempty` on the
	// answer to "did this work".
	raw, err := json.Marshal(QuickstartResult{})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(decoded))
	for key := range decoded {
		got = append(got, key)
	}
	sort.Strings(got)
	want := []string{"agent", "answer", "cluster", "context", "elapsed_seconds",
		"governed", "manifest", "next", "ok", "question", "tools"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the structured output's fields are %v, want %v: %s", got, want, raw)
	}
}

func TestQuickstartValidatesBeforeCompletingQuestionPhase(t *testing.T) {
	for _, tc := range []struct {
		name, state, answer, exit, output string
		wantOK                            bool
	}{
		{"completed", "completed", "Hello", "0", "text", true},
		{"existing full release", "completed", "Hello", "0", "text", true},
		{"nonzero", "completed", "Hello", "7", "text", false},
		{"failed with text", "failed", "Partial reply", "0", "text", false},
		{"working with text", "working", "Partial reply", "0", "text", false},
		{"empty", "completed", "  ", "0", "text", false},
		{"controls only", "completed", "\x1b[2J\a", "0", "text", false},
		{"human sanitized", "completed", "Hi\x1b[2J\a there", "0", "text", true},
		{"json preserved", "completed", "Hi\x1b[2J\a there", "0", "json", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDriverFixture(t, &fakePlane{})
			f.reply(t, 0, tc.state, tc.answer)
			t.Setenv("KMX_TEST_KAGENT_EXIT", tc.exit)
			t.Setenv("KMX_TOOLCHAIN", "off")
			bin := filepath.Dir(f.app.Cfg.KagentBin)
			for _, tool := range []string{"kind", "helm", "docker"} {
				fakeTool(t, bin, tool, "exit 0")
			}
			fakeTool(t, bin, "helm", `if [ "$1" = list ]; then printf '[]\n'; fi`)
			if tc.name == "existing full release" {
				fakeTool(t, bin, "helm", `case "$1" in
version) exit 0 ;;
list) printf '[{"name":"kagent","namespace":"kagent","revision":"1","status":"deployed"}]\n' ;;
get) printf '{"kagent-tools":{"enabled":true},"kmcp":{"enabled":true},"ui":{"replicas":1}}\n' ;;
*) exit 99 ;;
esac`)
			}
			// Only fake commands are reachable, including on a governance-preserving rerun.
			kubectl := strings.Replace(fakeDriverKubectl, "case \"$*\" in", `case "$*" in
  *"jsonpath={.spec.declarative.modelConfig}"*) printf 'governed-ollama'; exit 0 ;;`, 1)
			if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(kubectl), 0o755); err != nil {
				t.Fatal(err)
			}
			f.app.Cfg.ContainerEngine = "docker"
			f.app.Cfg.Model = "test-model"
			f.app.guarded = true
			err := f.app.Quickstart(QuickstartOptions{Output: tc.output})
			if (err == nil) != tc.wantOK {
				t.Fatalf("error=%v, want success=%v\n%s", err, tc.wantOK, f.errOut.String())
			}
			log := f.errOut.String()
			if tc.name == "existing full release" && !strings.Contains(log, "preserving it") {
				t.Fatalf("rerun failed to preserve the release: %s", log)
			}
			if strings.Contains(log, "DONE   [6/6]") != tc.wantOK || strings.Contains(log, "FAILED [6/6]") == tc.wantOK {
				t.Fatalf("question phase reported the wrong outcome:\n%s", log)
			}
			if !tc.wantOK {
				if strings.Contains(log, "COMPLETE") || f.out.Len() != 0 {
					t.Fatalf("failed quickstart reported success: %s\n%s", log, f.out.String())
				}
				return
			}
			if tc.output == "json" {
				var result QuickstartResult
				if err := json.Unmarshal(f.out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Answer != tc.answer || result.Governed {
					t.Fatalf("unexpected result: %+v", result)
				}
				wantNext := []string{
					"kmx --context kind-no-such-cluster-kmx-test agent chat hello-world 'ask it something else'",
					"kmx --context kind-no-such-cluster-kmx-test orka install",
					"KIND_CLUSTER=no-such-cluster-kmx-test CONTAINER_ENGINE=docker kmx --context kind-no-such-cluster-kmx-test up",
					"KIND_CLUSTER=no-such-cluster-kmx-test CONTAINER_ENGINE=docker kmx --context kind-no-such-cluster-kmx-test plane",
					"kmx --context kind-no-such-cluster-kmx-test govern release-agent",
				}
				if !reflect.DeepEqual(result.Next, wantNext) {
					t.Fatalf("follow-ups must separate Orka prerequisites from kagent chat/governance: got %q, want %q", result.Next, wantNext)
				}
				if !strings.Contains(result.Manifest, "kagent") || strings.Contains(result.Manifest, "`kmx agent create` writes your own") {
					t.Fatalf("manifest suggests Orka create replaces the kagent example: %s", result.Manifest)
				}
				for _, command := range result.Next {
					if !strings.Contains(command, "--context "+f.app.Cfg.KubeContext) {
						t.Errorf("next action lost context: %s", command)
					}
				}
			} else {
				if f.out.String() != "\n"+safeTerminal(tc.answer)+"\n" {
					t.Fatalf("unsafe human answer: %q", f.out.String())
				}
				for _, want := range []string{"This command does not enable governance", "delete the cluster and everything in it", "KIND_CLUSTER=" + f.app.Cfg.KindCluster} {
					if !strings.Contains(log, want) {
						t.Errorf("missing %q: %s", want, log)
					}
				}
				if strings.Contains(log, "NOT deployed") || strings.Contains(log, "Nothing that agent does") {
					t.Fatalf("rerun invented absent governance: %s", log)
				}
			}
		})
	}
}

func TestQuickstartHelmReleaseStateAndVersionContract(t *testing.T) {
	for _, version := range []string{"3", "4"} {
		for _, tc := range []struct {
			name, listing, listExit, rolloutExit, installExit string
			wantInstall, wantRollout, wantErr                 bool
		}{
			{"absent", `[]`, "0", "0", "0", true, false, false},
			{"deployed", `[{"name":"kagent","namespace":"kagent","revision":"1","status":"deployed"}]`, "0", "0", "0", false, true, false},
			{"controller unready", `[{"name":"kagent","namespace":"kagent","revision":"1","status":"deployed"}]`, "0", "1", "0", false, true, true},
			{"failed", `[{"name":"kagent","namespace":"kagent","revision":"1","status":"failed"}]`, "0", "0", "0", false, false, true},
			{"pending install", `[{"name":"kagent","namespace":"kagent","revision":"1","status":"pending-install"}]`, "0", "0", "0", false, false, true},
			{"pending upgrade", `[{"name":"kagent","namespace":"kagent","revision":"1","status":"pending-upgrade"}]`, "0", "0", "0", false, false, true},
			{"pending rollback", `[{"name":"kagent","namespace":"kagent","revision":"1","status":"pending-rollback"}]`, "0", "0", "0", false, false, true},
			{"uninstalled", `[{"name":"kagent","namespace":"kagent","revision":"1","status":"uninstalled"}]`, "0", "0", "0", false, false, true},
			{"uninstalling", `[{"name":"kagent","namespace":"kagent","revision":"1","status":"uninstalling"}]`, "0", "0", "0", false, false, true},
			{"superseded", `[{"name":"kagent","namespace":"kagent","revision":"1","status":"superseded"}]`, "0", "0", "0", false, false, true},
			{"unknown", `[{"name":"kagent","namespace":"kagent","revision":"1","status":"unknown"}]`, "0", "0", "0", false, false, true},
			{"wrong name", `[{"name":"kagent-other","namespace":"kagent","status":"deployed"}]`, "0", "0", "0", false, false, true},
			{"wrong namespace", `[{"name":"kagent","namespace":"other","revision":"1","status":"deployed"}]`, "0", "0", "0", false, false, true},
			{"duplicate", `[{"name":"kagent","namespace":"kagent","revision":"1","status":"deployed"},{"name":"kagent","namespace":"kagent","revision":"1","status":"failed"}]`, "0", "0", "0", false, false, true},
			{"list error", `[]`, "1", "0", "0", false, false, true},
			{"malformed", `{broken`, "0", "0", "0", false, false, true},
			{"empty output", ``, "0", "0", "0", false, false, true},
			{"null", `null`, "0", "0", "0", false, false, true},
			{"install failed or raced", `[]`, "0", "0", "1", true, false, true},
		} {
			t.Run("Helm"+version+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				log := filepath.Join(dir, "commands")
				t.Setenv("KMX_HELM_LOG", log)
				t.Setenv("KMX_HELM_LIST", tc.listing)
				t.Setenv("KMX_HELM_LIST_EXIT", tc.listExit)
				t.Setenv("KMX_HELM_INSTALL_EXIT", tc.installExit)
				t.Setenv("KMX_ROLLOUT_EXIT", tc.rolloutExit)
				t.Setenv("KMX_HELM_MAJOR", version)
				fakeTool(t, dir, "helm", `
printf 'helm %s\n' "$*" >> "$KMX_HELM_LOG"
if [ "$1" = list ]; then
  # Helm 4 rejects --all; Helm 3 would hide pending states by default.
  case " $* " in *" --all "*) exit 90 ;; esac
	case "$2" in --deployed|--failed|--pending|--uninstalled|--superseded|--uninstalling) ;; *) exit 91 ;; esac
	expected="list $2 --namespace kagent --kube-context kind-test --filter ^kagent$ --output json"
	if [ "$*" != "$expected" ]; then printf 'Helm %s list contract mismatch\n' "$KMX_HELM_MAJOR" >&2; exit 91; fi
  printf '%s\n' "$KMX_HELM_LIST"
  exit "$KMX_HELM_LIST_EXIT"
fi
if [ "$1 $2" = 'get values' ]; then
  printf '%s\n' '{"kagent-tools":{"enabled":true},"kmcp":{"enabled":true},"ui":{"replicas":1}}'
  exit 0
fi
if [ "$1" = install ] && [ "$2" = kagent ]; then
  case " $* " in *" --wait --wait-for-jobs --timeout 420s "*) ;; *) exit 92 ;; esac
  exit "$KMX_HELM_INSTALL_EXIT"
fi
if [ "$1 $2 $3" = 'upgrade --install kagent-crds' ]; then exit 0; fi
exit 93`)
				fakeTool(t, dir, "kubectl", `
printf 'kubectl %s\n' "$*" >> "$KMX_HELM_LOG"
if [ "$*" != '--context kind-test -n kagent rollout status deployment/kagent-controller --timeout=420s' ]; then exit 94; fi
exit "$KMX_ROLLOUT_EXIT"`)
				t.Setenv("PATH", dir)
				var out bytes.Buffer
				a := &App{Cfg: &config.Config{KubeContext: "kind-test", KagentVersion: config.DefaultKagentVersion}, Run: &run.Runner{}, Err: &out}
				err := a.quickstartKagent()
				if (err != nil) != tc.wantErr {
					t.Fatalf("error=%v, want error=%v", err, tc.wantErr)
				}
				commands, readErr := os.ReadFile(log)
				if readErr != nil {
					t.Fatal(readErr)
				}
				got := string(commands)
				if strings.Contains(got, "helm install kagent ") != tc.wantInstall || strings.Contains(got, "rollout status") != tc.wantRollout {
					t.Fatalf("unexpected release actions:\n%s", got)
				}
				if !tc.wantInstall && strings.Contains(got, "helm upgrade") {
					t.Fatalf("existing or unreadable release was changed:\n%s", got)
				}
				if tc.wantInstall {
					for _, flag := range quickstartValues {
						if !strings.Contains(got, flag) {
							t.Errorf("minimal install lost %s: %s", flag, got)
						}
					}
				}
				if tc.name == "failed" && !strings.Contains(err.Error(), "Repair the release deliberately") {
					t.Fatalf("missing repair guidance: %v", err)
				}
			})
		}
	}
}

// --output json must leave stdout carrying one document and nothing else.
// The regression this pins was real: kind, helm and kubectl all write to
// kmx's stdout, so the caller's parser met `Creating cluster "..."` before it
// met the JSON.
func TestJSONOutputKeepsSubprocessChatterOffStdout(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KMX_TOOLCHAIN", "off")
	a := &App{Cfg: &config.Config{ContainerEngine: "docker", KindCluster: "test", KubeContext: "kind-test"}, Run: &run.Runner{}, Out: out, Err: errOut}
	a.Run.Stdout = out
	// It fails at preflight on an empty PATH — long after the routing
	// decision, which is the part under test.
	_ = a.Quickstart(QuickstartOptions{Output: "json"})
	if a.Run.Stdout != errOut {
		t.Error("subprocess output still goes to stdout under --output json")
	}
	if out.Len() != 0 {
		t.Errorf("stdout is not empty before the JSON document: %q", out.String())
	}
}

// The answer is read out of the A2A task the same way `chat` reads it, and a
// response with no readable reply must not be reported as an answer.
func TestParseTaskFindsTheReplyAndRefusesRubbish(t *testing.T) {
	good := `some kagent logging
{"artifacts":[{"parts":[{"kind":"text","text":"I am a declarative kagent agent."}]}],"status":{"state":"completed"}}`
	if got := strings.TrimSpace(firstText(parseTask(good))); got != "I am a declarative kagent agent." {
		t.Errorf("reply not found: %q", got)
	}
	for _, bad := range []string{"", "no json here", "{not json}"} {
		if got := firstText(parseTask(bad)); got != "" {
			t.Errorf("%q yielded a reply %q", bad, got)
		}
	}
}

// The first-answer profile must turn off exactly the components the first
// question cannot reach — and must not touch the model provider, which is
// the one thing the answer depends on.
func TestTheFirstAnswerProfileOnlyDefersUnreachableComponents(t *testing.T) {
	joined := strings.Join(quickstartValues, " ")
	for _, off := range []string{"kaimahi.profile=first-answer", "kagent-tools.enabled=false", "kmcp.enabled=false", "ui.replicas=0"} {
		if !strings.Contains(joined, off) {
			t.Errorf("the first-answer profile does not defer %s: %s", off, joined)
		}
	}
	if strings.Contains(joined, "providers") || strings.Contains(joined, "ollama") {
		t.Errorf("the first-answer profile must not touch the model provider: %s", joined)
	}
}
