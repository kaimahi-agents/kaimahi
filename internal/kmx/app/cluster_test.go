package app

import (
	"bytes"
	"os"
	"path/filepath"
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

func TestKindSyncOnlyOnNewLocalClusters(t *testing.T) {
	for _, tc := range []struct{ engine, context, existing string }{
		{"docker", "kind-test", ""}, {"podman", "kind-test", ""},
		{"docker", "kind-test", "test"}, {"podman", "kind-test", "test"},
		{"docker", "aks-test", ""}, {"podman", "aks-test", ""},
	} {
		t.Run(tc.engine+"/"+tc.context+"/"+tc.existing, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("KMX_KIND_EXISTING", tc.existing)
			t.Setenv("KMX_KIND_INPUT", filepath.Join(dir, "input"))
			t.Setenv("KMX_KIND_ARGV", filepath.Join(dir, "kind-argv"))
			t.Setenv("KMX_PODMAN_ARGV", filepath.Join(dir, "podman-argv"))
			fakeTool(t, dir, "kind", `case "$1 $2" in
"get clusters") echo "$KMX_KIND_EXISTING" ;;
"create cluster") printf '%s\000' "$@" > "$KMX_KIND_ARGV"; /bin/cat > "$KMX_KIND_INPUT" ;;
*) exit 97 ;; esac`)
			fakeTool(t, dir, "kubectl", `case "$*" in
"config view -o json") echo '{"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}],"clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}]}' ;;
*"get --raw=/readyz"|*"rollout status deployment/coredns --timeout=180s") ;;
*) exit 97 ;; esac`)
			fakeTool(t, dir, "podman", `printf '%s\000' "$@" >> "$KMX_PODMAN_ARGV"
case "$1" in ps) echo test-control-plane ;; start) ;; *) exit 97 ;; esac`)
			t.Setenv("PATH", dir)
			var out bytes.Buffer
			a := New(&config.Config{KindCluster: "test", KubeContext: tc.context, ContextSource: config.SourceKubeCtx, ContainerEngine: tc.engine})
			a.Out, a.Err, a.Run.Stdout, a.Run.Stderr = &out, &out, &out, &out
			if err := a.stepCluster(); (err != nil) != (tc.context == "aks-test") ||
				err != nil && !strings.Contains(err.Error(), "does not identify kind cluster") {
				t.Fatalf("unexpected cluster result: %v", err)
			}
			input, err := os.ReadFile(filepath.Join(dir, "input"))
			if tc.existing != "" || tc.context == "aks-test" {
				if !os.IsNotExist(err) {
					t.Fatalf("existing/recovered or AKS target received config: %q (%v)", input, err)
				}
				return
			}
			argv, argvErr := os.ReadFile(filepath.Join(dir, "kind-argv"))
			if argvErr != nil || string(argv) != "create\x00cluster\x00--name\x00test\x00--config\x00-\x00" {
				t.Fatalf("unexpected kind creation argv: %q (%v)", argv, argvErr)
			}
			if argv, err := os.ReadFile(filepath.Join(dir, "podman-argv")); !os.IsNotExist(err) {
				t.Fatalf("new cluster must use kind, not direct Podman recovery: %q (%v)", argv, err)
			}
			var cluster struct {
				Patches []string `yaml:"kubeadmConfigPatches"`
			}
			if err != nil || yaml.Unmarshal(input, &cluster) != nil || len(cluster.Patches) != 1 {
				t.Fatalf("missing kind creation config: %q (%v)", input, err)
			}
			var kubelet map[string]string
			if err := yaml.Unmarshal([]byte(cluster.Patches[0]), &kubelet); err != nil || kubelet["syncFrequency"] != "5s" {
				t.Fatalf("new %s node did not receive 5s: %v (%v)", tc.engine, kubelet, err)
			}
		})
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
