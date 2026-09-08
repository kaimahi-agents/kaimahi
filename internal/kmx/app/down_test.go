package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// `kind delete cluster` deletes by CONTAINER name and never opens the
// kubeconfig. So the kubeconfig is not evidence about what exists — and
// treating an absent context as "nothing is there" is how a real cluster
// was deleted under a banner saying it had not been created yet. These
// pin the three ways that can go, on a stub kind that records what it was
// asked: what the delete DID is the only honest witness, since a refusal
// and a delete-that-failed both come back as errors.
func downHarness(t *testing.T, clusters, kubeconfig string) (*App, *bytes.Buffer, func() string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("KMX_HOME", filepath.Join(dir, "home"))
	log := filepath.Join(dir, "invocations")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("kind", `echo "kind $@" >> `+log+`
case "$1 $2" in
  "get clusters") printf '`+clusters+`' ;;
  "version "*|"version") echo "kind v0.20.0" ;;
esac
exit 0
`)
	// printf, not cat: PATH holds nothing but these stubs, so the script
	// may use only shell builtins.
	write("kubectl", `echo "kubectl $@" >> `+log+`
case "$*" in
  "config view -o json") printf '%s' '`+kubeconfig+`' ;;
esac
exit 0
`)
	write("docker", "exit 0\n")
	t.Setenv("PATH", dir)

	var out bytes.Buffer
	a := &App{
		Cfg: &config.Config{
			KindCluster: "kaimahi-p1", KubeContext: "kind-kaimahi-p1",
			ContextSource: config.SourceKubeCtx, ContainerEngine: "docker",
		},
		Run: &run.Runner{Stdout: &out, Stderr: &out},
		Out: &out, Err: &out,
	}
	return a, &out, func() string {
		b, err := os.ReadFile(log)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// The kubeconfig on this machine is stale, or points somewhere else
// entirely. The cluster is real and `kind delete` would find it by name.
func TestDownRefusesWhenTheKubeconfigDoesNotDescribeTheCluster(t *testing.T) {
	elsewhere := `{"clusters":[{"name":"c","cluster":{"server":"https://example.invalid:443"}}],"contexts":[{"name":"aks-prod","context":{"cluster":"c"}}],"current-context":"aks-prod"}`
	a, out, invocations := downHarness(t, "kaimahi-p1\\n", elsewhere)

	err := a.Down()
	if err == nil {
		t.Fatalf("a cluster the kubeconfig cannot vouch for was deleted without a question\n%s", out.String())
	}
	if strings.Contains(invocations(), "delete cluster") {
		t.Fatalf("it refused AND deleted:\n%s", invocations())
	}
	if strings.Contains(out.String(), "not created yet") {
		t.Errorf("the banner still says the context is not created yet:\n%s", out.String())
	}
}

// The other half, and the reason this is a refusal rather than a removal
// of the allowance: a cluster the kubeconfig knows is still deleted with
// no question, which is what CI and every ordinary teardown do.
func TestDownStillDeletesAClusterTheKubeconfigDescribes(t *testing.T) {
	known := `{"clusters":[{"name":"c","cluster":{"server":"https://127.0.0.1:36453"}}],"contexts":[{"name":"kind-kaimahi-p1","context":{"cluster":"c"}}],"current-context":"kind-kaimahi-p1"}`
	a, out, invocations := downHarness(t, "kaimahi-p1\\n", known)

	if err := a.Down(); err != nil {
		t.Fatalf("the ordinary teardown path must still work: %v\n%s", err, out.String())
	}
	if !strings.Contains(invocations(), "kind delete cluster --name kaimahi-p1") {
		t.Fatalf("nothing was deleted:\n%s", invocations())
	}
}

// Nothing to delete is not a refusal and not a deletion. Saying so before
// the guard runs keeps `kmx down` on an already-clean machine from asking
// a question about a cluster that is not there.
func TestDownSaysSoWhenThereIsNoSuchCluster(t *testing.T) {
	elsewhere := `{"clusters":[],"contexts":[],"current-context":""}`
	a, out, invocations := downHarness(t, "someone-elses\\n", elsewhere)

	if err := a.Down(); err != nil {
		t.Fatalf("an absent cluster is not an error: %v\n%s", err, out.String())
	}
	if strings.Contains(invocations(), "delete cluster") {
		t.Fatalf("it deleted something it had just said was not there:\n%s", invocations())
	}
	if !strings.Contains(out.String(), "nothing to delete") {
		t.Errorf("it did not say there was nothing to delete:\n%s", out.String())
	}
}
