package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestAgentLocationsPersistSeparateClustersPrivately(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	for _, cluster := range []string{"kind-local", "aks-remote"} {
		location, err := writeAgentLocation(agentLocation{Agent: "demo", Namespace: OrkaNamespace, Context: cluster}, []byte("apiVersion: v1\n"))
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(location.Kubeconfig)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("kubeconfig permissions=%v", info.Mode())
		}
	}
	locations, err := loadAgentLocations()
	if err != nil || len(locations) != 2 {
		t.Fatalf("locations=%v err=%v", locations, err)
	}
	if _, err := writeAgentLocation(locations[0], nil); err != nil {
		t.Fatal(err)
	}
	locations, err = loadAgentLocations()
	if err != nil || len(locations) != 2 {
		t.Fatal("saving same location duplicated it")
	}
}

func TestSwitchAgentLocationUsesRemoteConfigAndPreservesSource(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `case "$*" in *"--context aks-remote"*) ;; *) exit 1;; esac
[ "$KUBECONFIG" = "/private/remote-config" ] || exit 1
printf '%s' '{"kind":"Agent","metadata":{"name":"demo","namespace":"orka-system","uid":"remote-uid","generation":2},"status":{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":2}]}}'`)
	t.Setenv("PATH", dir)
	source := &App{Cfg: &config.Config{KubeContext: "kind-local"}, Run: &run.Runner{Env: []string{"KUBECONFIG=/source", "EXTRA=value"}}, chatInference: "copilot"}
	b := &orkaChatBackend{app: source, agent: "demo", namespace: OrkaNamespace}
	err := b.connectAgentLocation(context.Background(), agentLocation{Agent: "demo", Namespace: OrkaNamespace, Context: "aks-remote", Kubeconfig: "/private/remote-config"})
	if err != nil {
		t.Fatal(err)
	}
	if b.app.Cfg.KubeContext != "aks-remote" || b.app.chatInference != "local" || source.Cfg.KubeContext != "kind-local" || source.Run.Env[0] != "KUBECONFIG=/source" {
		t.Fatal("switch changed source or retained wrong inference")
	}
	if !strings.Contains(strings.Join(b.app.Run.Env, " "), "EXTRA=value") {
		t.Fatal("runner environment lost")
	}
}

func TestFailedAgentSwitchKeepsConnection(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", "exit 1")
	t.Setenv("PATH", dir)
	source := &App{Cfg: &config.Config{KubeContext: "kind-local"}, Run: &run.Runner{}}
	b := &orkaChatBackend{app: source, agent: "demo", namespace: OrkaNamespace}
	if err := b.connectAgentLocation(t.Context(), agentLocation{Agent: "remote", Namespace: OrkaNamespace, Context: "aks-remote"}); err == nil {
		t.Fatal("failed target accepted")
	}
	if b.app != source || b.agent != "demo" {
		t.Fatal("failed target replaced source")
	}
}

func TestPrivateAgentFileReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	for _, contents := range []string{"initial kubeconfig contents", "new"} {
		if err := writePrivateAgentFile(path, []byte(contents)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != contents {
			t.Fatalf("contents=%q err=%v", got, err)
		}
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 {
		t.Fatalf("temporary files remain: %v err=%v", files, err)
	}
}

func TestClusterDisplayUsesMetadataNotSubscriptionContext(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	contextName := "aks-subscription-guid-resource-group-cluster-with-hyphens"
	if _, err := writeAgentLocation(agentLocation{Agent: "demo", Namespace: OrkaNamespace, Context: contextName, Cluster: "cluster-with-hyphens"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := displayClusterName(contextName, ""); got != "cluster-with-hyphens" {
		t.Fatalf("display=%q", got)
	}
	app := appAtAgentLocation(&App{Cfg: &config.Config{}, Run: &run.Runner{}}, agentLocation{Context: contextName, Cluster: "cluster-with-hyphens"})
	if app.Cfg.KubeContext != contextName || app.chatClusterName != "cluster-with-hyphens" {
		t.Fatal("routing identity changed")
	}
}
