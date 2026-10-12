package app

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"go.yaml.in/yaml/v3"
)

// A kind cluster with no matching Podman containers is an inventory
// disagreement, not an empty recovery list. Refuse rather than report
// successful recovery without starting any node.
func TestPodmanNodesFailsClosedOnAnEmptyListing(t *testing.T) {
	for _, listing := range []string{"", "\n", "   \n\t\n"} {
		if _, err := podmanNodes(listing, "kaimahi-p1"); err == nil {
			t.Errorf("an empty node listing (%q) must refuse, not start nothing and carry on", listing)
		} else if !strings.Contains(err.Error(), "kaimahi-p1") {
			t.Errorf("the refusal must name the cluster: %v", err)
		}
	}
}

func TestPodmanNodesReturnsEveryNode(t *testing.T) {
	// A multi-node cluster lists one name per line; all of them get started.
	names, err := podmanNodes("p1-control-plane\np1-worker\np1-worker2\n", "p1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"p1-control-plane", "p1-worker", "p1-worker2"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", names, want)
	}
}

// Missing creation input or changing an existing node's configuration must fail
// at the executable boundary, not merely in a config-building helper.
func TestClusterConfigIsOnlySuppliedOnCreation(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		for _, state := range []string{"new", "existing"} {
			t.Run(engine+"/"+state, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("KMX_CLUSTER_FIXTURE", dir)
				t.Setenv("KMX_CLUSTER_STATE", state)
				fakeTool(t, dir, "kind", `printf '%s\n' "$*" >> "$KMX_CLUSTER_FIXTURE/kind-calls"
case "$1 $2" in
  "get clusters") if [ "$KMX_CLUSTER_STATE" = existing ]; then echo kaimahi-p1; fi ;;
  "create cluster") printf '%s\n' "$@" > "$KMX_CLUSTER_FIXTURE/argv"
    /bin/cat > "$KMX_CLUSTER_FIXTURE/stdin" ;;
  *) exit 97 ;;
esac`)
				fakeTool(t, dir, "kubectl", `printf '%s\n' "$*" >> "$KMX_CLUSTER_FIXTURE/kubectl-calls"
case "$*" in
  "config view -o json") printf '%s\n' '{"contexts":[{"name":"kind-kaimahi-p1","context":{"cluster":"kind-kaimahi-p1"}}],"clusters":[{"name":"kind-kaimahi-p1","cluster":{"server":"https://127.0.0.1:6443"}}]}' ;;
  "--context kind-kaimahi-p1 get --raw=/readyz"|"--context kind-kaimahi-p1 -n kube-system rollout status deployment/coredns --timeout=180s") ;;
  *) exit 97 ;;
esac`)
				fakeTool(t, dir, "podman", `printf '%s\n' "$*" >> "$KMX_CLUSTER_FIXTURE/podman-calls"
case "$1" in
  ps) printf '%s\n' kaimahi-p1-control-plane kaimahi-p1-worker ;;
  start) ;;
  *) exit 97 ;;
esac`)
				t.Setenv("PATH", dir)
				var out bytes.Buffer
				a := New(&config.Config{KindCluster: "kaimahi-p1", KubeContext: "kind-kaimahi-p1",
					ContextSource: config.SourceKubeCtx, ContainerEngine: engine})
				a.Out, a.Err, a.Run.Stdout, a.Run.Stderr = &out, &out, &out, &out
				if err := a.stepCluster(); err != nil {
					t.Fatal(err)
				}
				calls, err := os.ReadFile(filepath.Join(dir, "kind-calls"))
				if err != nil {
					t.Fatal(err)
				}
				if state == "existing" {
					if string(calls) != "get clusters\n" {
						t.Fatalf("reuse/recovery invoked kind beyond inventory: %s", calls)
					}
					if _, err := os.Stat(filepath.Join(dir, "stdin")); !os.IsNotExist(err) {
						t.Fatalf("reuse/recovery supplied creation config: %v", err)
					}
				} else {
					if string(calls) != "get clusters\ncreate cluster --name kaimahi-p1 --config -\n" {
						t.Errorf("expected exactly one creation after inventory, got %q", calls)
					}
					argv, err := os.ReadFile(filepath.Join(dir, "argv"))
					if err != nil {
						t.Fatal(err)
					}
					if string(argv) != "create\ncluster\n--name\nkaimahi-p1\n--config\n-\n" {
						t.Errorf("kind must read creation config from stdin; argv: %q", argv)
					}
					input, err := os.ReadFile(filepath.Join(dir, "stdin"))
					if err != nil {
						t.Fatal(err)
					}
					var cluster map[string]any
					if err := yaml.Unmarshal(input, &cluster); err != nil {
						t.Fatal(err)
					}
					if len(cluster) != 3 || cluster["apiVersion"] != "kind.x-k8s.io/v1alpha4" || cluster["kind"] != "Cluster" {
						t.Fatalf("expected minimal kind v1alpha4 config on stdin, got %q", input)
					}
					patches, ok := cluster["kubeadmConfigPatches"].([]any)
					if !ok || len(patches) != 1 {
						t.Fatalf("expected one kubeadm patch, got %v", cluster)
					}
					patch, ok := patches[0].(string)
					if !ok {
						t.Fatalf("expected a YAML patch string, got %v", patches[0])
					}
					var kubelet map[string]any
					if err := yaml.Unmarshal([]byte(patch), &kubelet); err != nil {
						t.Fatal(err)
					}
					want := map[string]any{"apiVersion": "kubelet.config.k8s.io/v1beta1", "kind": "KubeletConfiguration", "syncFrequency": "5s"}
					if !reflect.DeepEqual(kubelet, want) {
						t.Fatalf("expected only the exact 5s kubelet patch, got %v", kubelet)
					}
				}
				kubectlCalls, err := os.ReadFile(filepath.Join(dir, "kubectl-calls"))
				wantReads := "--context kind-kaimahi-p1 get --raw=/readyz\n--context kind-kaimahi-p1 -n kube-system rollout status deployment/coredns --timeout=180s\n"
				if state == "existing" {
					wantReads = "config view -o json\n" + wantReads
				}
				if err != nil || string(kubectlCalls) != wantReads {
					t.Fatalf("cluster preparation must not rewrite config through kubectl: %q (%v)", kubectlCalls, err)
				}
				podmanCalls, err := os.ReadFile(filepath.Join(dir, "podman-calls"))
				if engine == "podman" && state == "existing" {
					want := "ps -a --filter label=io.x-k8s.kind.cluster=kaimahi-p1 --format {{.Names}}\nstart kaimahi-p1-control-plane kaimahi-p1-worker\n"
					if err != nil || string(podmanCalls) != want {
						t.Fatalf("recovery must start exactly the owned nodes: %q (%v)", podmanCalls, err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("unexpected Podman recovery: %q (%v)", podmanCalls, err)
				}
			})
		}
	}
}

// A failed `kind get clusters` leaves the inventory unknown. Do not treat
// container-engine or socket failures as evidence that a cluster is missing
// and proceed to creation.
//
// The stub kind fails whatever it is asked, so a regression that read the
// error as absence would go on to `kind create cluster` and still return an
// error. Only the REFUSAL distinguishes the two, which is what this asserts.
func TestClusterRefusesRatherThanReadingAFailedListingAsAbsence(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "kind")
	// The stub records what it was asked before failing. Watching kmx's own
	// output for the word "create" would prove nothing: this Runner does not
	// echo, so the line searched for could never appear however kmx behaved.
	// What kind was actually invoked with is the only honest witness.
	log := filepath.Join(dir, "kind-invocations")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n" +
		"echo 'ERROR: failed to list clusters: permission denied while trying to connect to the Docker daemon socket' >&2\nexit 1\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	var out bytes.Buffer
	a := &App{
		Cfg: &config.Config{KindCluster: "kaimahi-p1", KubeContext: "kind-kaimahi-p1", ContextSource: config.SourceKubeCtx, ContainerEngine: "docker"},
		Run: &run.Runner{Stdout: &out, Stderr: &out},
		Out: &out, Err: &out,
	}
	err := a.stepCluster()
	if err == nil {
		t.Fatal("a failed `kind get clusters` was not an error at all")
	}
	if !strings.Contains(err.Error(), "refusing to guess") {
		t.Fatalf("the failure was not refused as unknowable — it reads as an absent cluster:\n%v", err)
	}
	if !strings.Contains(err.Error(), "kaimahi-p1") || !strings.Contains(err.Error(), "docker") {
		t.Errorf("the refusal must name the cluster and the engine: %v", err)
	}
	invocations, readErr := os.ReadFile(log)
	if readErr != nil {
		t.Fatalf("the stub kind was never invoked, so nothing here was tested: %v", readErr)
	}
	if !strings.Contains(string(invocations), "get clusters") {
		t.Errorf("kmx never asked kind what exists:\n%s", invocations)
	}
	if strings.Contains(string(invocations), "create cluster") {
		t.Errorf("kmx went on to create a cluster after it failed to look:\n%s", invocations)
	}
}

// An already-existing cluster records the invented context too, and that is
// the intended behaviour rather than an oversight.
//
// The invented default only survives the guard in two shapes: a machine with
// no clusters, and one where the name is already the context the operator is
// pointed at. In both, `kmx local up` has just verified this cluster is serving and
// the operator asked for it — so turning the invention into a recorded choice
// is exactly the point. Gating this on "did THIS run create it" would leave
// the second `kmx local up` resolving through the fallback again, which is the
// state the fallback exists to get out of.
func TestAnExistingClusterAlsoRecordsTheInventedContext(t *testing.T) {
	dir := t.TempDir()
	// kind lists the cluster; kubectl answers the serving check.
	if err := os.WriteFile(filepath.Join(dir, "kind"),
		[]byte("#!/bin/sh\necho kaimahi-p1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kubectl"),
		[]byte("#!/bin/sh\ncase \"$*\" in *'config view'*) printf '%s\\n' '{\"current-context\":\"kind-kaimahi-p1\",\"contexts\":[{\"name\":\"kind-kaimahi-p1\",\"context\":{\"cluster\":\"kind-kaimahi-p1\"}}],\"clusters\":[{\"name\":\"kind-kaimahi-p1\",\"cluster\":{\"server\":\"https://127.0.0.1:6443\"}}]}' ;; esac\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_HOME", t.TempDir())

	var out bytes.Buffer
	a := &App{
		Cfg: &config.Config{KindCluster: "kaimahi-p1", KubeContext: "kind-kaimahi-p1",
			ContextSource: config.SourceDefault, ContainerEngine: "docker"},
		Run: &run.Runner{Stdout: &out, Stderr: &out},
		Out: &out, Err: &out,
	}
	if err := a.stepCluster(); err != nil {
		t.Fatalf("stepCluster on an existing cluster: %v", err)
	}
	if a.Cfg.ContextSource != config.SourceSelected {
		t.Errorf("the invented context was not turned into a choice: source is %q", a.Cfg.ContextSource)
	}
	selected, err := config.ReadSelectedContext()
	if err != nil {
		t.Fatal(err)
	}
	if selected != "kind-kaimahi-p1" {
		t.Errorf("later commands will fall back again: recorded %q", selected)
	}
}

func TestExistingKindClusterRepairsMissingKubeconfigContext(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "commands")
	fakeTool(t, dir, "kind", `printf 'kind %s\n' "$*" >> "$COMMAND_LOG"
case "$1 $2" in "get clusters") echo kaimahi-p1;; esac`)
	fakeTool(t, dir, "kubectl", `printf 'kubectl %s\n' "$*" >> "$COMMAND_LOG"
case "$*" in *"config view"*) printf '%s\n' '{"current-context":"other","contexts":[{"name":"other","context":{"cluster":"other"}}],"clusters":[{"name":"other","cluster":{"server":"https://example.test"}}]}' ;; esac`)
	t.Setenv("PATH", dir)
	t.Setenv("COMMAND_LOG", log)
	t.Setenv("KMX_HOME", t.TempDir())
	var out bytes.Buffer
	a := &App{Cfg: &config.Config{KindCluster: "kaimahi-p1", KubeContext: "kind-kaimahi-p1", ContextSource: config.SourceDefault, ContainerEngine: "docker"}, Run: &run.Runner{Stdout: &out, Stderr: &out}, Out: &out, Err: &out}
	if err := a.stepCluster(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "kind export kubeconfig --name kaimahi-p1") {
		t.Fatalf("missing context was not repaired:\n%s", raw)
	}
	if !strings.Contains(out.String(), "repairing kubeconfig") {
		t.Fatalf("repair was not visible:\n%s", out.String())
	}
}

// A cluster that never came up records nothing: pointing every later command
// at something that is not there is worse than falling back.
func TestAFailedClusterRecordsNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kind"),
		[]byte("#!/bin/sh\ncase \"$1\" in get) exit 0;; *) echo 'create failed' >&2; exit 1;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_HOME", t.TempDir())

	var out bytes.Buffer
	a := &App{
		Cfg: &config.Config{KindCluster: "kaimahi-p1", KubeContext: "kind-kaimahi-p1",
			ContextSource: config.SourceDefault, ContainerEngine: "docker"},
		Run: &run.Runner{Stdout: &out, Stderr: &out},
		Out: &out, Err: &out,
	}
	if err := a.stepCluster(); err == nil {
		t.Fatal("a failed create was not an error")
	}
	if a.Cfg.ContextSource != config.SourceDefault {
		t.Errorf("a cluster that never came up was recorded as the chosen context")
	}
	if selected, _ := config.ReadSelectedContext(); selected != "" {
		t.Errorf("a cluster that never came up was recorded: %q", selected)
	}
}
