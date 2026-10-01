package app

import (
	"bytes"
	"context"
	"fmt"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

// kagentRuntimeAdapter is intentionally lifecycle-only. This slice renders
// and deploys into a preinstalled Kagent; it does not add Kagent discovery or
// chat behavior to the existing runtime registry.
type kagentRuntimeAdapter struct {
	app      *App
	create   *CreateOptions
	bindings *agentruntime.KagentBindings
}

var _ agentruntime.LifecycleAdapter = kagentRuntimeAdapter{}

func (kagentRuntimeAdapter) ID() agentruntime.ID { return agentruntime.Kagent }

func (kagentRuntimeAdapter) Probe(context.Context, agentruntime.Target) (agentruntime.Probe, error) {
	return agentruntime.Probe{}, fmt.Errorf("Kagent auto-detection is not supported; select it explicitly for create")
}

func (kagentRuntimeAdapter) Open(context.Context, agentruntime.Target) (agentruntime.Session, error) {
	return nil, fmt.Errorf("Kagent chat is not supported by this lifecycle adapter")
}

func (a kagentRuntimeAdapter) Capabilities() agentruntime.Capabilities {
	configured := a.create != nil && a.bindings != nil
	return agentruntime.Capabilities{Render: configured, Deploy: configured}
}

func (kagentRuntimeAdapter) ConsumedExtensions() []agentruntime.ID {
	return []agentruntime.ID{agentruntime.Kagent}
}

func (kagentRuntimeAdapter) SupportsCoordination() bool { return false }

func (a kagentRuntimeAdapter) lifecycleVerbError(declared bool, verb string) error {
	if declared {
		return nil
	}
	return &agentruntime.UnsupportedVerbError{Runtime: a.ID(), Verb: verb}
}

func (a kagentRuntimeAdapter) Render(_ context.Context, prepared *agentruntime.PreparedPortableRender, _ agentruntime.RenderOptions) (agentruntime.RenderedBundle, error) {
	if err := a.lifecycleVerbError(a.Capabilities().Render, agentruntime.VerbRender); err != nil {
		return agentruntime.RenderedBundle{}, err
	}
	portable, source, err := prepared.ForAdapter(a)
	if err != nil {
		return agentruntime.RenderedBundle{}, err
	}
	if portable.Extensions.Kagent == nil {
		return agentruntime.RenderedBundle{}, fmt.Errorf("portable agent %q has no extensions.kagent block", portable.Metadata.Name)
	}
	if err := agentruntime.ValidateKagentBindings(*a.bindings); err != nil {
		return agentruntime.RenderedBundle{}, fmt.Errorf("Kagent bindings: %w", err)
	}
	tools := make([]scaffold.KagentMCPToolBinding, 0, len(portable.Extensions.Kagent.Tools))
	for _, binding := range portable.Extensions.Kagent.Tools {
		tools = append(tools, scaffold.KagentMCPToolBinding{
			ServerKind: binding.Server.Kind,
			ServerName: binding.Server.Name,
			ToolNames:  append([]string(nil), binding.ToolNames...),
		})
	}
	bundle, err := scaffold.GenerateKagent(scaffold.KagentSpec{
		Name: portable.Metadata.Name, Namespace: a.bindings.Namespace,
		Description: portable.Spec.Description, Instructions: portable.Spec.Instructions,
		Runtime: portable.Extensions.Kagent.Runtime, Model: portable.Spec.Model.Name,
		ProviderType: a.bindings.ModelConfig.Provider, BaseURL: a.bindings.ModelConfig.BaseURL,
		SecretName: a.bindings.ModelConfig.SecretRef.Name, SecretKey: a.bindings.ModelConfig.SecretRef.Key,
		Tools: tools,
	})
	if err != nil {
		return agentruntime.RenderedBundle{}, err
	}
	documents := make([]agentruntime.Document, 0, 3)
	for _, doc := range bundle.Documents() {
		encoded, err := yaml.Marshal(doc)
		if err != nil {
			return agentruntime.RenderedBundle{}, fmt.Errorf("render Kagent bundle: %w", err)
		}
		if doc["kind"] == "Secret" {
			documents = append(documents, agentruntime.ReviewDocument(encoded))
		} else {
			documents = append(documents, agentruntime.ApplyDocument(encoded))
		}
	}
	return agentruntime.NewRenderedBundle(a.ID(), source, documents)
}

func (a kagentRuntimeAdapter) Deploy(ctx context.Context, rendered agentruntime.RenderedBundle, options agentruntime.DeployOptions) (agentruntime.DeployResult, error) {
	if err := a.lifecycleVerbError(a.Capabilities().Deploy, agentruntime.VerbDeploy); err != nil {
		return agentruntime.DeployResult{}, err
	}
	if options.Reconcile {
		return agentruntime.DeployResult{}, fmt.Errorf("Kagent create supports create-only deployment and refuses reconciliation or adoption")
	}
	bundle, err := kagentBundleFromRendered(rendered)
	if err != nil {
		return agentruntime.DeployResult{}, err
	}
	opt := *a.create
	opt.Name = kagentObjectName(bundle.Agent)
	opt.Namespace = kagentObjectNamespace(bundle.Agent)
	opt.Secret = kagentObjectName(bundle.Secret)
	return a.app.createKagentStaged(ctx, opt, bundle, rendered)
}

func (a kagentRuntimeAdapter) Status(context.Context, agentruntime.AgentRef, agentruntime.StatusOptions) (agentruntime.Status, error) {
	return agentruntime.Status{}, &agentruntime.UnsupportedVerbError{Runtime: a.ID(), Verb: agentruntime.VerbStatus}
}

func (a kagentRuntimeAdapter) Evaluate(context.Context, agentruntime.AgentRef, agentruntime.EvaluationRequest) (agentruntime.EvaluationReceipt, error) {
	return agentruntime.EvaluationReceipt{}, &agentruntime.UnsupportedVerbError{Runtime: a.ID(), Verb: agentruntime.VerbEvaluate}
}

func kagentBundleFromRendered(rendered agentruntime.RenderedBundle) (*scaffold.KagentBundle, error) {
	if rendered.AdapterID() != agentruntime.Kagent {
		return nil, fmt.Errorf("rendered bundle targets runtime %q, not %q", rendered.AdapterID(), agentruntime.Kagent)
	}
	artifact, deploy := rendered.Documents(), rendered.DeployDocuments()
	review := orkaReviewDocuments(artifact, deploy)
	if len(artifact) != 3 || len(review) != 1 || len(deploy) != 2 {
		return nil, fmt.Errorf("rendered Kagent bundle must contain one review-only Secret and deployable ModelConfig and Agent documents")
	}
	if !bytes.Equal(review[0], artifact[0]) || !bytes.Equal(deploy[0], artifact[1]) || !bytes.Equal(deploy[1], artifact[2]) {
		return nil, fmt.Errorf("rendered Kagent documents are not in Secret, ModelConfig, Agent order")
	}
	// KagentArtifact owns the strict rendered-byte decoder: exactly one YAML
	// mapping per document, no aliases, merges, duplicate keys, invalid UTF-8,
	// or structural drift. Deploy validates through it before decoding maps.
	if _, err := scaffold.KagentArtifact(artifact); err != nil {
		return nil, fmt.Errorf("rendered Kagent bundle is invalid: %w", err)
	}
	secret, err := decodeKagentRenderedDocument(review[0], "Secret")
	if err != nil {
		return nil, err
	}
	model, err := decodeKagentRenderedDocument(deploy[0], "ModelConfig")
	if err != nil {
		return nil, err
	}
	agent, err := decodeKagentRenderedDocument(deploy[1], "Agent")
	if err != nil {
		return nil, err
	}
	bundle := &scaffold.KagentBundle{Secret: secret, ModelConfig: model, Agent: agent}
	if err := bundle.Validate(); err != nil {
		return nil, fmt.Errorf("rendered Kagent bundle is not internally consistent: %w", err)
	}
	return bundle, nil
}

func decodeKagentRenderedDocument(raw []byte, want string) (map[string]any, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("rendered Kagent %s document is not valid YAML: %w", want, err)
	}
	if kind, _ := doc["kind"].(string); kind != want {
		return nil, fmt.Errorf("rendered Kagent document is %q where %q was expected", kind, want)
	}
	return doc, nil
}

func kagentObjectName(doc map[string]any) string {
	metadata, _ := doc["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	return name
}

func kagentObjectNamespace(doc map[string]any) string {
	metadata, _ := doc["metadata"].(map[string]any)
	namespace, _ := metadata["namespace"].(string)
	return namespace
}
