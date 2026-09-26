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

// kind reads KIND_EXPERIMENTAL_PROVIDER directly. Explicit Docker must remove
// an inherited Podman value, or --container-engine docker still queries and can
// delete Podman's separate cluster inventory.
func TestExplicitDockerClearsInheritedPodmanProviderFromKind(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "provider")
	t.Setenv("KIND_EXPERIMENTAL_PROVIDER", "podman")
	t.Setenv("KMX_TEST_PROVIDER_LOG", log)
	fakeTool(t, dir, "kind", `printf '%s' "${KIND_EXPERIMENTAL_PROVIDER-unset}" > "$KMX_TEST_PROVIDER_LOG"; exit 0`)
	fakeTool(t, dir, "kubectl", "exit 0")
	fakeTool(t, dir, "docker", "exit 0")
	t.Setenv("PATH", dir)

	cfg := &config.Config{KindCluster: "demo", KubeContext: "kind-demo", ContainerEngine: "docker"}
	a := New(cfg)
	a.Out, a.Err = &bytes.Buffer{}, &bytes.Buffer{}
	if err := a.stepCluster(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "unset" {
		t.Fatalf("kind inherited provider %q under explicit Docker", got)
	}
}

func TestExplicitPodmanReplacesAnInheritedKindProvider(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "provider")
	t.Setenv("KIND_EXPERIMENTAL_PROVIDER", "docker")
	t.Setenv("KMX_TEST_PROVIDER_LOG", log)
	fakeTool(t, dir, "kind", `printf '%s' "${KIND_EXPERIMENTAL_PROVIDER-unset}" > "$KMX_TEST_PROVIDER_LOG"; exit 0`)
	fakeTool(t, dir, "kubectl", "exit 0")
	fakeTool(t, dir, "podman", "case \"$*\" in ps*) exit 0;; esac")
	t.Setenv("PATH", dir)

	cfg := &config.Config{KindCluster: "demo", KubeContext: "kind-demo", ContainerEngine: "podman"}
	a := New(cfg)
	a.Out, a.Err = &bytes.Buffer{}, &bytes.Buffer{}
	if err := a.stepCluster(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "podman" {
		t.Fatalf("kind inherited provider %q under explicit Podman", got)
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
		a.quickstartNext(cliui.WithCapabilities(cliui.Capabilities{Rich: rich, Width: 80}), QuickstartResult{Next: []string{"agent chat", "agent create", "orka status"}})
		for _, want := range []string{"delete the cluster and everything in it", a.operationCommand("down"), "docs/kmx.md#kmx-agent-create", "what is installed, and what it can resolve"} {
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
