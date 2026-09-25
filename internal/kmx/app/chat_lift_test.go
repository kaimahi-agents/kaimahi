package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestLiftAzureCommandsPinSubscriptionAndIsolateCredentials(t *testing.T) {
	target := chatLiftTarget{Subscription: "sub-id", ResourceGroup: "my-rg", Cluster: "aks", Context: "kmx-lift-target"}
	want := []string{"aks", "get-credentials", "--subscription", "sub-id", "--resource-group", "my-rg", "--name", "aks", "--context", "kmx-lift-target", "--file", "/tmp/isolated", "--only-show-errors"}
	if got := aksCredentialsArgs(target, "/tmp/isolated"); !reflect.DeepEqual(got, want) {
		t.Fatalf("args=%v", got)
	}
	args := aksListArgs(target)
	if args[2] != "--subscription" || args[3] != "sub-id" || strings.Contains(strings.Join(args, " "), "--resource-group") || !strings.Contains(strings.Join(args, " "), "resourceGroup:resourceGroup") {
		t.Fatalf("args=%v", args)
	}
}

func TestLiftLocationLabels(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, tc := range []struct {
		local                  bool
		context, cluster, want string
	}{
		{true, "kind-demo", "", "(local) kind-demo"},
		{false, "aks-sub-rg-demo", "demo", "(remote-aks) demo"},
		{false, "production", "", "(remote-k8s) production"},
	} {
		if got := liftLocationLabel(tc.local, tc.context, tc.cluster); got != tc.want {
			t.Fatalf("got=%q want=%q", got, tc.want)
		}
	}
}

func TestLiftPickerVimNavigationAndSearch(t *testing.T) {
	m := chatPicker{vim: true, searchEnabled: true, items: []chatPickerItem{{name: "alpha"}, {name: "kube-prod"}, {name: "staging"}}}
	for _, key := range []tea.KeyPressMsg{{Code: 'j', Text: "j"}, {Code: 'k', Text: "k"}, {Code: '/', Text: "/"}, {Code: 'k', Text: "k"}, {Code: 'u', Text: "u"}} {
		updated, _ := m.Update(key)
		m = updated.(chatPicker)
	}
	if m.query != "ku" || len(m.matches()) != 1 || m.items[m.matches()[0]].name != "kube-prod" {
		t.Fatalf("picker=%+v", m)
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(chatPicker)
	if cmd != nil || m.accepted || m.searching {
		t.Fatal("search escape selected a target")
	}
	updated, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(chatPicker)
	if cmd == nil || !m.accepted {
		t.Fatal("navigation enter did not select")
	}
}

func TestLiftDiscoveryCancels(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "az", "exec sleep 30")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	a := &App{Run: &run.Runner{}}
	_, err := a.liftDiscovery(ctx, filepath.Join(dir, "az"), "account", "list")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

// Azure calls are prepared by the Runner, so a worker that added or removed an
// environment variable is obeyed. A raw exec.Command inherits the process
// environment and cannot take a variable away, which is the difference this
// covers: KMX_LIFT_KEPT is added by the Runner and KMX_LIFT_DROPPED is removed
// even though the process itself has it set.
func TestLiftAzureCallsCarryTheRunnerEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*App, context.Context) ([]byte, error)
	}{
		{"discovery", func(a *App, ctx context.Context) ([]byte, error) {
			return a.liftDiscovery(ctx, "az", "account", "list")
		}},
		{"write", func(a *App, ctx context.Context) ([]byte, error) {
			return a.liftAzureWrite(ctx, "cognitiveservices", "account", "create")
		}},
	} {
		dir := t.TempDir()
		fakeTool(t, dir, "az", `printf 'kept=%s dropped=%s' "$KMX_LIFT_KEPT" "$KMX_LIFT_DROPPED"`)
		t.Setenv("PATH", dir)
		t.Setenv("KMX_LIFT_DROPPED", "inherited")
		a := &App{Run: &run.Runner{Env: []string{"KMX_LIFT_KEPT=added"}, Unset: []string{"KMX_LIFT_DROPPED"}}}
		raw, err := tc.call(a, t.Context())
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := string(raw); got != "kept=added dropped=" {
			t.Fatalf("%s: az saw %q, so the Runner environment was not applied", tc.name, got)
		}
	}
}

// Raw az stderr can name subscriptions and resources, so a failure reports a
// fixed message and the diagnostic text is dropped rather than propagated.
func TestLiftAzureFailureDoesNotPropagateAzureDiagnostics(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "az", `printf 'subscription 00000000-0000-0000-0000-000000000000 denied' >&2; exit 1`)
	t.Setenv("PATH", dir)
	a := &App{Run: &run.Runner{}}
	_, err := a.liftDiscovery(t.Context(), "az", "account", "list")
	if err == nil {
		t.Fatal("a failing az call was reported as success")
	}
	if strings.Contains(err.Error(), "00000000") || strings.Contains(err.Error(), "denied") {
		t.Fatalf("azure diagnostics reached the caller: %v", err)
	}
}

func TestPortableLiftBundleDropsServerMetadataAndKeepsConfiguration(t *testing.T) {
	agent := map[string]any{"status": map[string]any{"ready": true}, "metadata": map[string]any{"uid": "old", "labels": map[string]any{"app.kubernetes.io/version": "v2", "cluster-only": "local"}}, "spec": map[string]any{"providerRef": map[string]any{"name": "shared"}, "tools": []any{map[string]any{"name": "k8s-get-resources"}}, "systemPrompt": map[string]any{"inline": "custom"}}}
	provider := map[string]any{"status": map[string]any{"ready": true}, "spec": map[string]any{"type": "openai", "defaultModel": "test", "secretRef": map[string]any{"name": "existing-key", "key": "api-key"}}}
	bundle, err := portableLiftBundle(agent, provider, "demo", OrkaNamespace)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Agent["status"] != nil || bundle.Agent["metadata"].(map[string]any)["uid"] != nil {
		t.Fatal("server metadata copied")
	}
	labels := bundle.Agent["metadata"].(map[string]any)["labels"].(map[string]any)
	if len(labels) != 1 || labels["app.kubernetes.io/version"] != "v2" {
		t.Fatal("lift lost release version or copied unrelated labels")
	}
	if bundle.Secret["data"] != nil || bundle.Secret["metadata"].(map[string]any)["name"] != "existing-key" {
		t.Fatal("credentials copied or reference changed")
	}
	if bundle.Agent["spec"].(map[string]any)["tools"] == nil {
		t.Fatal("tools lost")
	}
}
