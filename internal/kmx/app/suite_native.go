package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// Private observation inputs, not a receipt: retain identities and consumed
// references, never instructions, cases, credential values or arbitrary metadata.
type suiteNativeSubject struct {
	Suite, SuiteDigest, GraphDigest, Member, MemberDigest     string
	InstructionsDigest, CompositionDigest, BuildProfileDigest string
	Model, Renderer                                           string
	RequiredImages                                            []string
	Target                                                    suiteNativeTarget
	Binding                                                   suiteNativeBinding
	Runtime                                                   suiteNativeRuntime
	BindingDigest, PayloadDigest, RenderedDigest              string
}
type suiteNativeTarget struct{ Context, ClusterUID, Namespace, NamespaceUID string }
type suiteNativeBinding struct {
	Provider suiteNativeProvider
	Secret   suiteNativeSecret
}
type suiteNativeProvider struct {
	Name, Namespace, UID, ResourceVersion, Type, BaseURL, SecretName, SecretKey string
	Generation                                                                  int64
}
type suiteNativeSecret struct {
	Name, Namespace, UID, ResourceVersion, Key string
	KeyPresent                                 bool
}
type suiteNativeHelmIdentity struct{ Chart, Instance, App, ManagedBy string }
type suiteNativeRuntime struct {
	ControllerName, ControllerUID, ControllerServiceAccount, ControllerImage string
	ControllerGeneration                                                     int64
	ControllerReplicas                                                       int32
	Mode, WatchNamespace, AIWorkerImage, WorkerAccount, WorkerAccountUID     string
	Helm                                                                     suiteNativeHelmIdentity
}
type suiteNativePrepared struct {
	Subject   suiteNativeSubject
	bundle    *scaffold.OrkaBundle
	documents [][]byte
}

// Load owns packaged-input validation; preparation takes only the render frame
// it needs. Installed-schema support and readiness do not prove admission or auth.
func (a *App) prepareSuiteNative(ctx context.Context, suite *workspace.NativeSuite, namespace, selectedProvider string) (*suiteNativePrepared, error) {
	if err := a.validateSuiteNativeSelectors(namespace, selectedProvider); err != nil {
		return nil, err
	}
	if suite == nil {
		return nil, fmt.Errorf("native suite packaged input is required")
	}
	if selectedProvider == suite.MemberID {
		return nil, fmt.Errorf("selected Provider collides with the native member Provider")
	}
	if suite.RequiredImages == nil || len(suite.RequiredImages) != 0 {
		return nil, fmt.Errorf("native suite requires explicit empty requiredImages")
	}
	if strings.TrimSpace(suite.Model) == "" || strings.TrimSpace(string(suite.Instructions)) == "" {
		return nil, fmt.Errorf("native suite requires exact nonblank packaged model and instructions")
	}
	target, binding, runtime, err := a.readSuiteNativeInputs(ctx, namespace, selectedProvider)
	if err != nil {
		return nil, err
	}
	bundle, err := scaffold.GenerateOrka(scaffold.OrkaSpec{Name: suite.MemberID, Namespace: namespace, Description: suite.Description, Model: suite.Model, Instructions: string(suite.Instructions), ProviderType: binding.Provider.Type, BaseURL: binding.Provider.BaseURL, SecretName: binding.Provider.SecretName, SecretKey: binding.Provider.SecretKey})
	if err != nil {
		return nil, err
	}
	// Installed's three CRDs are validator prerequisites; no Task is rendered.
	crds := map[string][]byte{}
	for _, kind := range []string{"Agent", "Provider", "Task"} {
		raw, err := a.orkaCapture(ctx, nil, "get", "crd", orkaPlural(kind), "-o", "json")
		if err != nil {
			return nil, fmt.Errorf("cannot read installed %s CRD (no offline fallback): %w", kind, err)
		}
		crds[kind] = raw
	}
	validator, err := orkaschema.Installed(crds)
	if err != nil {
		return nil, err
	}
	if err := validateOrkaBundle(bundle, validator); err != nil {
		return nil, err
	}
	rendered, err := orkaRenderedDocuments(bundle)
	if err != nil {
		return nil, err
	}
	documents := make([][]byte, 0, len(rendered))
	for _, doc := range rendered {
		documents = append(documents, doc.Bytes())
	}
	subject := suiteNativeSubject{Suite: suite.Name, SuiteDigest: suite.Digest, GraphDigest: suite.GraphDigest, Member: suite.MemberID, MemberDigest: suite.MemberDigest, InstructionsDigest: suite.InstructionsDigest, CompositionDigest: suite.CompositionDigest, BuildProfileDigest: suite.BuildProfileDigest, Model: suite.Model, Renderer: suite.RendererVersion, RequiredImages: []string{}, Target: target, Binding: binding, Runtime: runtime}
	subject.BindingDigest, err = suiteNativeDigest("kmx.native-orka.binding/v1", struct {
		Model          string
		RequiredImages []string
		Binding        suiteNativeBinding
		Runtime        suiteNativeRuntime
	}{subject.Model, subject.RequiredImages, binding, runtime})
	if err != nil {
		return nil, err
	}
	subject.PayloadDigest = agentruntime.RenderedBundleDigest(documents)
	// D is unset during hashing; S/G are loader identities, not portable digests.
	subject.RenderedDigest, err = suiteNativeDigest("kmx.native-orka.revision/v1", subject)
	if err != nil {
		return nil, err
	}
	return &suiteNativePrepared{Subject: subject, bundle: bundle, documents: documents}, nil
}

func suiteNativeDigest(domain string, value any) (string, error) {
	raw, err := spec.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("cannot canonicalize native suite identity")
	}
	if scaffold.RefuseKeyShapes(string(raw)) != nil {
		return "", fmt.Errorf("refusing credential-shaped native suite identity")
	}
	return agentruntime.RenderedBundleDigest([][]byte{[]byte(domain), raw}), nil
}

func (a *App) validateSuiteNativeSelectors(namespace, provider string) error {
	if a.Cfg == nil || a.Run == nil || strings.TrimSpace(a.Cfg.KubeContext) == "" {
		return fmt.Errorf("native suite requires an explicit configured context")
	}
	for _, selector := range []string{a.Cfg.KubeContext, namespace, provider} {
		if scaffold.RefuseKeyShapes(selector) != nil {
			return fmt.Errorf("refusing credential-shaped native suite selector")
		}
	}
	if scaffold.ValidateNamespace(namespace) != nil || scaffold.ValidateObjectName(provider) != nil {
		return fmt.Errorf("native suite requires an explicit valid namespace and Provider")
	}
	return nil
}

func (a *App) readSuiteNativeInputs(ctx context.Context, namespace, provider string) (suiteNativeTarget, suiteNativeBinding, suiteNativeRuntime, error) {
	var target suiteNativeTarget
	var binding suiteNativeBinding
	var runtime suiteNativeRuntime
	if err := a.validateSuiteNativeSelectors(namespace, provider); err != nil {
		return target, binding, runtime, err
	}
	target.Context, target.Namespace = a.Cfg.KubeContext, namespace
	uid, terminating, err := a.readLiftClusterUID(ctx)
	if err != nil {
		return target, binding, runtime, err
	}
	if terminating {
		return target, binding, runtime, fmt.Errorf("native suite cluster identity namespace is terminating")
	}
	target.ClusterUID = uid
	raw, err := a.orkaCapture(ctx, nil, "get", "namespace", namespace, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return target, binding, runtime, err
	}
	var ns struct {
		Kind     string
		Metadata struct {
			Name, UID         string
			DeletionTimestamp *string
		}
	}
	if json.Unmarshal(raw, &ns) != nil || ns.Kind != "Namespace" || ns.Metadata.Name != namespace || ns.Metadata.UID == "" || ns.Metadata.DeletionTimestamp != nil {
		return target, binding, runtime, fmt.Errorf("native suite namespace has no valid live identity")
	}
	target.NamespaceUID = ns.Metadata.UID
	selected, identity, err := a.readLiftProviderBindings(ctx, namespace, provider)
	if err != nil {
		return target, binding, runtime, err
	}
	if selected.Provider.Type != "openai" || identity.ResourceVersion == "" {
		return target, binding, runtime, fmt.Errorf("native suite requires an openai Provider with resourceVersion")
	}
	binding.Provider = suiteNativeProvider{Name: provider, Namespace: namespace, UID: identity.UID, Generation: identity.Generation, ResourceVersion: identity.ResourceVersion, Type: selected.Provider.Type, BaseURL: selected.Provider.BaseURL, SecretName: selected.Provider.SecretRef.Name, SecretKey: selected.Provider.SecretRef.Key}
	before, err := a.readSuiteNativeSecretMetadata(ctx, namespace, selected.Provider.SecretRef.Name)
	if err != nil {
		return target, binding, runtime, err
	}
	if err := a.orkaProviderSecretPresent(ctx, namespace, selected.Provider.SecretRef.Name, selected.Provider.SecretRef.Key); err != nil {
		return target, binding, runtime, err
	}
	after, err := a.readSuiteNativeSecretMetadata(ctx, namespace, selected.Provider.SecretRef.Name)
	if err != nil {
		return target, binding, runtime, err
	}
	if before != after {
		return target, binding, runtime, fmt.Errorf("Provider Secret changed during key-presence inspection")
	}
	binding.Secret = suiteNativeSecret{Name: before.Metadata.Name, Namespace: before.Metadata.Namespace, UID: before.Metadata.UID, ResourceVersion: before.Metadata.ResourceVersion, Key: selected.Provider.SecretRef.Key, KeyPresent: true}
	runtime, err = a.readSuiteNativeRuntime(ctx, namespace)
	return target, binding, runtime, err
}

type suiteNativeSecretProjection struct {
	Kind        string
	Metadata    struct{ Name, Namespace, UID, ResourceVersion string }
	Terminating bool
}

func (a *App) readSuiteNativeSecretMetadata(ctx context.Context, namespace, name string) (suiteNativeSecretProjection, error) {
	// Fixed output cannot capture data values or arbitrary annotations.
	const projection = `go-template={"Kind":{{printf "%q" .kind}},"Metadata":{"Name":{{printf "%q" .metadata.name}},"Namespace":{{printf "%q" .metadata.namespace}},"UID":{{printf "%q" .metadata.uid}},"ResourceVersion":{{printf "%q" .metadata.resourceVersion}}},"Terminating":{{if .metadata.deletionTimestamp}}true{{else}}false{{end}}}`
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", "secret", name, "--ignore-not-found=true", "-o", projection)
	var out suiteNativeSecretProjection
	if err != nil {
		return out, err
	}
	if spec.Unmarshal(raw, &out) != nil || out.Kind != "Secret" || out.Metadata.Name != name || out.Metadata.Namespace != namespace || out.Metadata.UID == "" || out.Metadata.ResourceVersion == "" || out.Terminating {
		return out, fmt.Errorf("Provider Secret metadata has no valid live identity")
	}
	return out, nil
}

func suiteNativeHelm(labels map[string]string) suiteNativeHelmIdentity {
	return suiteNativeHelmIdentity{labels["helm.sh/chart"], labels["app.kubernetes.io/instance"], labels["app.kubernetes.io/name"], labels["app.kubernetes.io/managed-by"]}
}

// Only joined chart flags are supported. Reject alternate Go flag syntax that
// could override the observed value, and positional arguments that stop parsing.
func suiteNativeControllerFlag(args []string, name string) (string, error) {
	value, count := "", 0
	for _, arg := range args {
		if arg == "--" || !strings.HasPrefix(arg, "-") {
			return "", fmt.Errorf("unsupported positional Orka controller argument")
		}
		if strings.HasPrefix(arg, "--"+name+"=") {
			count++
			value = strings.TrimPrefix(arg, "--"+name+"=")
		} else if arg == "--"+name || arg == "-"+name || strings.HasPrefix(arg, "-"+name+"=") {
			return "", fmt.Errorf("unsupported or ambiguous Orka %s flag", name)
		}
	}
	if count != 1 || value == "" {
		return "", fmt.Errorf("Orka requires one explicit %s flag", name)
	}
	return value, nil
}

func suiteNativeControllerRuntime(controller orkaDeployment, namespace string) (suiteNativeRuntime, error) {
	var out suiteNativeRuntime
	if controller.Kind != "Deployment" || controller.Metadata.Namespace != OrkaNamespace || controller.Metadata.UID == "" || controller.Metadata.Generation < 1 || controller.Metadata.DeletionTimestamp != nil {
		return out, fmt.Errorf("native suite requires a live chart controller identity")
	}
	out.Helm = suiteNativeHelm(controller.Metadata.Labels)
	if out.Helm.Chart != "orka-0.2.0" || out.Helm.Instance == "" || out.Helm.App != "orka" || out.Helm.ManagedBy != "Helm" || controller.Metadata.Labels["app.kubernetes.io/component"] != "controller" {
		return out, fmt.Errorf("native suite requires the pinned Orka chart identity")
	}
	status, replicas := controller.Status, controller.Spec.Replicas
	if replicas < 1 || status.ObservedGeneration != controller.Metadata.Generation || status.Replicas != replicas || status.UpdatedReplicas != replicas || status.ReadyReplicas != replicas || status.AvailableReplicas != replicas || status.UnavailableReplicas != 0 {
		return out, fmt.Errorf("Orka controller is not Ready for its current generation")
	}
	controllers := 0
	for _, container := range controller.Spec.Template.Spec.Containers {
		if container.Name != "controller" {
			continue
		}
		controllers++
		if container.Image != "ghcr.io/orka-agents/orka@"+orkaControllerDigest || len(container.Command) != 0 {
			return out, fmt.Errorf("native suite requires the pinned controller image and entrypoint")
		}
		out.ControllerImage = container.Image
		for _, field := range []struct {
			name string
			dst  *string
		}{{"controller-mode", &out.Mode}, {"watch-namespace", &out.WatchNamespace}, {"ai-worker-image", &out.AIWorkerImage}, {"ai-worker-service-account-name", &out.WorkerAccount}} {
			value, err := suiteNativeControllerFlag(container.Args, field.name)
			if err != nil {
				return out, err
			}
			*field.dst = value
		}
	}
	if controllers != 1 || out.Mode != "harness-v2" || out.WatchNamespace != namespace || out.AIWorkerImage != "ghcr.io/orka-agents/orka/ai-worker@"+orkaAIWorkerDigest || scaffold.ValidateObjectName(controller.Spec.Template.Spec.ServiceAccountName) != nil {
		return out, fmt.Errorf("unsupported native controller mode, watch namespace, worker image or account")
	}
	out.ControllerName, out.ControllerUID, out.ControllerGeneration = controller.Metadata.Name, controller.Metadata.UID, controller.Metadata.Generation
	out.ControllerReplicas, out.ControllerServiceAccount = replicas, controller.Spec.Template.Spec.ServiceAccountName
	return out, nil
}

func (a *App) readSuiteNativeRuntime(ctx context.Context, namespace string) (suiteNativeRuntime, error) {
	var out suiteNativeRuntime
	deployments, err := a.orkaDeployments(ctx)
	if err != nil {
		return out, err
	}
	controller, _, err := selectOrkaController(deployments)
	if err != nil {
		return out, err
	}
	out, err = suiteNativeControllerRuntime(controller, namespace)
	if err != nil {
		return out, err
	}
	worker, err := a.orkaAIWorkerAccount(ctx)
	if err != nil {
		return out, err
	}
	if worker != out.WorkerAccount {
		return out, fmt.Errorf("Orka configured worker changed during inspection")
	}
	// Existing discovery/trust remains authoritative; add only live UID,
	// termination and fixed labels, not a second worker discovery mechanism.
	const projection = `go-template={"Kind":{{printf "%q" .kind}},"Metadata":{"Name":{{printf "%q" .metadata.name}},"Namespace":{{printf "%q" .metadata.namespace}},"UID":{{printf "%q" .metadata.uid}},"Helm":{"Chart":{{printf "%q" (index .metadata.labels "helm.sh/chart")}},"Instance":{{printf "%q" (index .metadata.labels "app.kubernetes.io/instance")}},"App":{{printf "%q" (index .metadata.labels "app.kubernetes.io/name")}},"ManagedBy":{{printf "%q" (index .metadata.labels "app.kubernetes.io/managed-by")}}}},"Worker":{{printf "%q" (index .metadata.labels "orka.ai/worker")}},"Trust":{{printf "%q" (index .metadata.labels "orka.ai/worker-trust")}},"Terminating":{{if .metadata.deletionTimestamp}}true{{else}}false{{end}}}`
	raw, err := a.orkaCapture(ctx, nil, "-n", OrkaNamespace, "get", "serviceaccount", worker, "-o", projection)
	if err != nil {
		return out, err
	}
	var account struct {
		Kind     string
		Metadata struct {
			Name, Namespace, UID string
			Helm                 suiteNativeHelmIdentity
		}
		Worker, Trust string
		Terminating   bool
	}
	if spec.Unmarshal(raw, &account) != nil || account.Kind != "ServiceAccount" || account.Metadata.Name != worker || account.Metadata.Namespace != OrkaNamespace || account.Metadata.UID == "" || account.Terminating || account.Metadata.Helm != out.Helm || account.Worker != "true" || account.Trust != "ai" {
		return out, fmt.Errorf("Orka worker has no valid matching runtime identity")
	}
	// The reused trust helper reselects the controller. Compare only consumed
	// semantic identity/configuration, ignoring RV and still-Ready status churn.
	current, err := a.orkaDeployments(ctx)
	if err != nil {
		return out, err
	}
	again, _, err := selectOrkaController(current)
	if err != nil {
		return out, err
	}
	observed, err := suiteNativeControllerRuntime(again, namespace)
	if err != nil {
		return out, err
	}
	if observed != out {
		return out, fmt.Errorf("Orka controller changed during runtime inspection")
	}
	out.WorkerAccountUID = account.Metadata.UID
	return out, nil
}

func (a *App) verifySuiteNativeInputs(ctx context.Context, subject suiteNativeSubject) error {
	if a.Cfg == nil || a.Cfg.KubeContext != subject.Target.Context {
		return fmt.Errorf("native suite context changed; subject is unknown")
	}
	target, binding, runtime, err := a.readSuiteNativeInputs(ctx, subject.Target.Namespace, subject.Binding.Provider.Name)
	if err != nil {
		return fmt.Errorf("native suite subject inspection unknown: %w", err)
	}
	if target != subject.Target || binding != subject.Binding || runtime != subject.Runtime {
		return fmt.Errorf("native suite target, binding or runtime changed; subject is unknown")
	}
	return nil
}
