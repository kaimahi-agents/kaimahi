package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

type chatLiftTarget struct{ Context, Subscription, ResourceGroup, Cluster, Location, Tenant, Kubeconfig string }

func liftDiscovery(ctx context.Context, command string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.WaitDelay = time.Second
	out := &orkaBoundedBuffer{remaining: 4 << 20}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s discovery failed; check login and read access", command)
	}
	return out.buffer.Bytes(), nil
}

func (b *orkaChatBackend) liftPick(ctx context.Context, title string, items []chatPickerItem) (int, bool, error) {
	// A lone creation action is not a catalog that benefits from search.
	return b.liftPicker(ctx, title, items, len(items) > 1)
}

func (b *orkaChatBackend) liftAction(ctx context.Context, title string, items []chatPickerItem) (int, bool, error) {
	return b.liftPicker(ctx, title, items, false)
}

func (b *orkaChatBackend) liftPicker(ctx context.Context, title string, items []chatPickerItem, searchable bool) (int, bool, error) {
	if len(items) == 0 {
		return 0, false, fmt.Errorf("no targets found for %s", title)
	}
	m, err := runChatPicker(ctx, b.app.Stdin, b.app.Out, chatPicker{title: "LIFT · " + title, items: items, vim: true, action: "select", searchEnabled: searchable, searchDefault: searchable, header: b.liftHeader})
	b.paintLiftHeader()
	if err != nil || !m.accepted {
		return 0, false, err
	}
	return m.matches()[m.selection], true, nil
}

func (b *orkaChatBackend) chooseLiftTarget(ctx context.Context) (chatLiftTarget, bool, error) {
	var target chatLiftTarget
	index, ok, err := b.liftAction(ctx, "Target source", []chatPickerItem{{name: "Kubeconfig context", detail: "Use an existing configured cluster"}, {name: "Azure AKS", detail: "Browse clusters across a subscription"}})
	if err != nil || !ok {
		return target, false, err
	}
	if index == 0 {
		raw, err := b.app.orkaCapture(ctx, nil, "config", "get-contexts", "-o", "name")
		if err != nil {
			return target, false, err
		}
		names := strings.Fields(string(raw))
		sort.Strings(names)
		items := make([]chatPickerItem, 0, len(names))
		for _, name := range names {
			items = append(items, chatPickerItem{name: name})
		}
		index, ok, err = b.liftRecentPick(ctx, "Kubeconfig contexts", items, names, "context")
		if err != nil || !ok {
			return target, false, err
		}
		target.Context = names[index]
		return target, true, nil
	}
	raw, err := b.liftSubscriptions(ctx)
	if err != nil {
		return target, false, err
	}
	var subscriptions []struct{ Name, ID, TenantID string }
	if err = json.Unmarshal(raw, &subscriptions); err != nil {
		return target, false, err
	}
	items := make([]chatPickerItem, 0, len(subscriptions))
	keys := make([]string, 0, len(subscriptions))
	for _, s := range subscriptions {
		items = append(items, chatPickerItem{name: s.Name, detail: s.ID})
		keys = append(keys, s.ID)
	}
	index, ok, err = b.liftRecentPick(ctx, "Azure subscription", items, keys, "subscription")
	if err != nil || !ok {
		return target, false, err
	}
	target.Subscription = subscriptions[index].ID
	target.Tenant = subscriptions[index].TenantID
	raw, err = b.liftClusters(ctx, target)
	if err != nil {
		return target, false, err
	}
	var clusters []azureCluster
	if err = json.Unmarshal(raw, &clusters); err != nil {
		return target, false, err
	}
	items = nil
	keys = nil
	for _, c := range clusters {
		items = append(items, chatPickerItem{name: c.Name, detail: "rg:" + c.ResourceGroup + " · " + c.Location + " · Kubernetes " + c.KubernetesVersion})
		keys = append(keys, c.ResourceGroup+"/"+c.Name)
	}
	index, ok, err = b.liftRecentPick(ctx, "AKS cluster", items, keys, "cluster/"+target.Subscription)
	if err != nil || !ok {
		return target, false, err
	}
	target.Cluster = clusters[index].Name
	target.ResourceGroup = clusters[index].ResourceGroup
	target.Location = clusters[index].Location
	target.Context = "aks-" + target.Subscription + "-" + target.ResourceGroup + "-" + target.Cluster
	if err := saveLiftPreference("context", target.Context); err != nil {
		return target, false, err
	}
	return target, true, nil
}

func aksListArgs(t chatLiftTarget) []string {
	return []string{"aks", "list", "--subscription", t.Subscription, "--query", "[].{name:name,resourceGroup:resourceGroup,location:location,kubernetesVersion:kubernetesVersion}", "-o", "json", "--only-show-errors"}
}
func aksCredentialsArgs(t chatLiftTarget, path string) []string {
	return []string{"aks", "get-credentials", "--subscription", t.Subscription, "--resource-group", t.ResourceGroup, "--name", t.Cluster, "--context", t.Context, "--file", path, "--only-show-errors"}
}

func (b *orkaChatBackend) liftAgent(ctx context.Context, renderer *chatRenderer) error {
	b.liftHeader = &liftHeader{agent: b.agent, source: b.app.Cfg.KubeContext}
	defer func() { b.liftHeader = nil }()
	b.paintLiftHeader()
	target, ok, err := b.chooseLiftTarget(ctx)
	if err != nil || !ok {
		return err
	}
	return b.liftAgentTo(ctx, renderer, target)
}

// liftAgentTo shares the full interactive deployment flow with the agent dashboard.
func (b *orkaChatBackend) liftAgentTo(ctx context.Context, renderer *chatRenderer, target chatLiftTarget) error {
	b.liftHeader = &liftHeader{agent: b.agent, source: b.app.Cfg.KubeContext}
	defer func() { b.liftHeader = nil }()
	b.paintLiftHeader()
	if target.Subscription == "" && target.Context == b.app.Cfg.KubeContext {
		return fmt.Errorf("select a different target cluster; this Agent already runs in %s", target.Context)
	}
	targetLabel := target.Context
	if target.Cluster != "" {
		targetLabel = target.Cluster + " · rg:" + target.ResourceGroup
	}
	b.liftStage(0, targetLabel)
	dir, err := os.MkdirTemp("", "kmx-lift-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	worker := *b.app
	cfg := *b.app.Cfg
	runner := *b.app.Run
	worker.Cfg = &cfg
	worker.Run = &runner
	runner.Context = ctx
	worker.guarded = false
	cfg.KubeContext, cfg.ContextSource = target.Context, config.SourceFlag
	if target.Kubeconfig != "" {
		located := appAtAgentLocation(&worker, agentLocation{Context: target.Context, Kubeconfig: target.Kubeconfig})
		runner.Env = located.Run.Env
	}
	if target.Subscription != "" {
		path := filepath.Join(dir, "kubeconfig")
		if _, err = b.liftAzureFetch(ctx, aksCredentialsArgs(target, path)...); err != nil {
			return err
		}
		runner.Env = append(append([]string(nil), runner.Env...), "KUBECONFIG="+path)
	}
	bundle, err := b.liftBundle(ctx)
	if err != nil {
		return err
	}
	raw, err := worker.orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return err
	}
	kube, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return err
	}
	posture, err := guard.Classify(kube, target.Context)
	if err != nil {
		return err
	}
	sourceLocation, err := b.app.chatLocation(ctx)
	if err != nil {
		return err
	}
	from := liftLocationLabel(strings.HasPrefix(sourceLocation, "local-"), b.app.Cfg.KubeContext, b.app.chatClusterName)
	to := liftLocationLabel(posture.Local, target.Context, target.Cluster)
	detail := fmt.Sprintf("Agent %s/%s\nFrom: %s\nTo: %s\nServer: %s\nSubscription: %s\nResource group: %s / AKS: %s", b.namespace, b.agent, from, to, posture.Host, target.Subscription, target.ResourceGroup, target.Cluster)
	// Target selection starts read-only prerequisite discovery. Each install or
	// provisioning action has its own explicit confirmation; Provider/Agent
	// writes are authorized by the single final deployment review below.
	// Online creation still owns schemas, collisions, admission and Ready waits.
	worker.guarded = true
	worker.liftReuse = true
	worker.Out = io.Discard
	worker.Err = io.Discard
	worker.Stdin = nil
	b.liftStage(1, "")
	if err := b.prepareLiftOrka(ctx, &worker, renderer); err != nil {
		return liftPreparationError(err)
	}
	b.liftStage(2, "")
	if err := b.selectLiftInference(ctx, &worker, target, bundle, renderer); err != nil {
		return liftPreparationError(err)
	}
	b.liftStage(3, "")
	if err := b.prepareLiftTools(ctx, &worker, bundle, renderer); err != nil {
		return liftPreparationError(err)
	}
	b.liftStage(4, "")
	providerSpec := bundle.Provider["spec"].(map[string]any)
	index, ok, err := b.liftAction(ctx, "Deploy\n"+detail+fmt.Sprintf("\nModel: %v\nEndpoint: %v\nProvider/Agent: create missing, reuse matching; conflicts stop deployment", providerSpec["defaultModel"], providerSpec["baseURL"]), []chatPickerItem{{name: "Cancel"}, {name: "Deploy Provider and Agent"}})
	if err != nil || !ok || index == 0 {
		return err
	}
	renderer.operation("LIFT", "", colorBlue, "Deploying "+b.agent+" to "+target.Context+"…")
	secret := bundle.Provider["spec"].(map[string]any)["secretRef"].(map[string]any)
	opt := CreateOptions{Name: b.agent, Namespace: b.namespace, Secret: secret["name"].(string), Out: filepath.Join(dir, "agent.yaml")}
	err = b.runLiftDeployment(ctx, &worker, "Deploy Agent", []string{"Validate schemas and prerequisites", "Validate server admission", "Create Provider", "Wait for Provider Ready", "Create Agent", "Wait for Agent Ready"}, func(worker *App) error {
		deployCtx, cancel := context.WithTimeout(worker.operationContext(), 5*time.Minute)
		defer cancel()
		// Lift has no portable source: its bundle is assembled from objects
		// that already exist in the source cluster, so it cannot render and
		// deploys the bundle it built directly.
		return worker.createOrkaOnline(deployCtx, opt, bundle)
	})
	if err != nil {
		return err
	}
	// Persist both ends before switching, so /agent can return to the source.
	b.liftStage(5, "")
	if _, err := rememberAgentLocation(ctx, b.app, agentLocation{Agent: b.agent, Namespace: b.namespace, Context: b.app.Cfg.KubeContext}); err != nil {
		return fmt.Errorf("Agent deployed, but source location could not be saved: %w", err)
	}
	if err := worker.quickstartResultReader(); err != nil {
		return fmt.Errorf("Agent deployed; result access setup failed: %w", err)
	}
	location, err := rememberAgentLocation(ctx, &worker, agentLocation{Agent: b.agent, Namespace: b.namespace, Context: target.Context, Cluster: target.Cluster, Subscription: target.Subscription, ResourceGroup: target.ResourceGroup})
	if err != nil {
		return fmt.Errorf("Agent deployed, but target location could not be saved: %w", err)
	}
	if err := b.connectAgentLocation(ctx, location); err != nil {
		return fmt.Errorf("Agent deployed and saved; connecting failed: %w", err)
	}
	renderer.operation("LIFT", "", colorGreen, "Connected to lifted Agent on "+target.Context+".")
	return nil
}

func liftLocationLabel(local bool, contextName, cluster string) string {
	if local {
		return "(local) " + contextName
	}
	if cluster != "" {
		return "(remote-aks) " + cluster
	}
	if locations, err := loadAgentLocations(); err == nil {
		for _, location := range locations {
			if location.Context == contextName && location.Cluster != "" {
				return "(remote-aks) " + location.Cluster
			}
		}
	}
	return "(remote-k8s) " + contextName
}

func (b *orkaChatBackend) liftBundle(ctx context.Context) (*scaffold.OrkaBundle, error) {
	raw, err := b.app.orkaCapture(ctx, nil, "-n", b.namespace, "get", "agents.core.orka.ai", b.agent, "-o", "json")
	if err != nil {
		return nil, err
	}
	var agent map[string]any
	if err = json.Unmarshal(raw, &agent); err != nil {
		return nil, err
	}
	spec, _ := agent["spec"].(map[string]any)
	ref, _ := spec["providerRef"].(map[string]any)
	name, _ := ref["name"].(string)
	ns, _ := ref["namespace"].(string)
	if ns == "" {
		ns = b.namespace
	}
	if name == "" {
		return nil, fmt.Errorf("lift requires an Agent Provider reference")
	}
	raw, err = b.app.orkaCapture(ctx, nil, "-n", ns, "get", "providers.core.orka.ai", name, "-o", "json")
	if err != nil {
		return nil, err
	}
	var provider map[string]any
	if err = json.Unmarshal(raw, &provider); err != nil {
		return nil, err
	}
	return portableLiftBundle(agent, provider, b.agent, b.namespace)
}

func portableLiftBundle(agent, provider map[string]any, name, namespace string) (*scaffold.OrkaBundle, error) {
	// Keep source objects untouched while rewriting target references.
	raw, err := json.Marshal([]map[string]any{agent, provider})
	if err != nil {
		return nil, err
	}
	var copies []map[string]any
	if err := json.Unmarshal(raw, &copies); err != nil {
		return nil, err
	}
	agent, provider = copies[0], copies[1]
	clean := func(kind string, doc map[string]any) map[string]any {
		metadata := map[string]any{"name": name, "namespace": namespace}
		if source, ok := doc["metadata"].(map[string]any); ok {
			if labels, ok := source["labels"].(map[string]any); ok {
				if version, ok := labels["app.kubernetes.io/version"].(string); ok && version != "" {
					metadata["labels"] = map[string]any{"app.kubernetes.io/version": version}
				}
			}
		}
		return map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": kind, "metadata": metadata, "spec": doc["spec"]}
	}
	agent, provider = clean("Agent", agent), clean("Provider", provider)
	spec, ok := agent["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Agent spec missing")
	}
	spec["providerRef"] = map[string]any{"name": name, "namespace": namespace}
	ps, _ := provider["spec"].(map[string]any)
	secret, _ := ps["secretRef"].(map[string]any)
	bundle := &scaffold.OrkaBundle{Agent: agent, Provider: provider, Secret: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": secret["name"], "namespace": namespace}}}
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	return bundle, nil
}
