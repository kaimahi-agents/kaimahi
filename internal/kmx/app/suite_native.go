package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

// These are App-private evidence inputs, not a receipt or public runtime format.
// Only identities and consumed references are serializable; neither source
// instructions, live status payloads nor credential values belong here.
type suiteNativeSubject struct {
	Suite              string             `json:"suite"`
	SuiteDigest        string             `json:"suiteDigest"` // S: OCI sha256:<64 hex>
	GraphDigest        string             `json:"graphDigest"` // G: JCS sha256:<64 hex>
	Member             string             `json:"member"`
	MemberDigest       string             `json:"memberDigest"`
	InstructionsDigest string             `json:"instructionsDigest"`
	Model              string             `json:"model"`
	CompositionDigest  string             `json:"compositionDigest"`
	BuildProfileDigest string             `json:"buildProfileDigest"`
	Renderer           string             `json:"renderer"`
	RequiredImages     []string           `json:"requiredImages"`
	Target             suiteNativeTarget  `json:"target"`
	Binding            suiteNativeBinding `json:"binding"`
	Runtime            suiteNativeRuntime `json:"runtime"`
	BindingDigest      string             `json:"bindingDigest"`  // B: framed 64 hex, not an OCI digest
	PayloadDigest      string             `json:"payloadDigest"`  // exact unmarked review/deploy documents
	RenderedDigest     string             `json:"renderedDigest"` // D: target/source/binding/render revision, 64 hex
}
type suiteNativeTarget struct {
	Context      string `json:"context"`
	ClusterUID   string `json:"clusterUID"`
	Namespace    string `json:"namespace"`
	NamespaceUID string `json:"namespaceUID"`
}
type suiteNativeBinding struct {
	Provider suiteNativeProvider `json:"provider"`
	Secret   suiteNativeSecret   `json:"secret"`
}
type suiteNativeProvider struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	UID             string `json:"uid"`
	Generation      int64  `json:"generation"`
	ResourceVersion string `json:"resourceVersion"`
	Type            string `json:"type"`
	BaseURL         string `json:"baseURL"`
	SecretName      string `json:"secretName"`
	SecretKey       string `json:"secretKey"`
}
type suiteNativeSecret struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
	Key             string `json:"key"`
	KeyPresent      bool   `json:"keyPresent"`
}
type suiteNativeHelmIdentity struct {
	Chart     string `json:"chart"`
	Instance  string `json:"instance"`
	App       string `json:"app"`
	ManagedBy string `json:"managedBy"`
}
type suiteNativeRuntime struct {
	ID                       string                  `json:"id"`
	Version                  string                  `json:"version"`
	Namespace                string                  `json:"namespace"`
	ControllerName           string                  `json:"controllerName"`
	ControllerUID            string                  `json:"controllerUID"`
	ControllerGeneration     int64                   `json:"controllerGeneration"`
	ControllerServiceAccount string                  `json:"controllerServiceAccount"`
	ControllerImage          string                  `json:"controllerImage"`
	Mode                     string                  `json:"mode"`
	WatchNamespace           string                  `json:"watchNamespace"`
	Helm                     suiteNativeHelmIdentity `json:"helm"`
	AIWorkerImage            string                  `json:"aiWorkerImage"`
	WorkerAccount            string                  `json:"workerAccount"`
	WorkerAccountUID         string                  `json:"workerAccountUID"`
	WorkerTrust              string                  `json:"workerTrust"`
}
type suiteNativePlan struct {
	Subject   suiteNativeSubject
	Before    []suiteNativePairObservation
	bundle    *scaffold.OrkaBundle
	documents [][]byte
	owner     orkaSuiteOwnership
	checks    []orkaReconcileCheck
}
type suiteNativePlacement struct {
	Subject   suiteNativeSubject           `json:"subject"`
	Before    []suiteNativePairObservation `json:"before"`
	Resources []suiteNativeResource        `json:"resources"`
	Complete  bool                         `json:"complete"`
	plan      *suiteNativePlan
}
type suiteNativeResource struct {
	Kind           string                       `json:"kind"`
	Name           string                       `json:"name"`
	Namespace      string                       `json:"namespace"`
	UID            string                       `json:"uid"`
	Generation     int64                        `json:"generation"`
	SpecDigest     string                       `json:"specDigest"`
	Outcome        agentruntime.ResourceOutcome `json:"outcome"`
	AttemptedWrite bool                         `json:"attemptedWrite"`
	Applied        bool                         `json:"applied"`
	Ready          bool                         `json:"ready"`
	Ambiguous      bool                         `json:"ambiguous"`
	// A residual observation is not successful placement attribution. In
	// particular, a lost create response must not be converted into adoption.
	Observed *suiteNativeOwnedIdentity `json:"observed,omitempty"`
}
type suiteNativePairObservation struct {
	Kind      string                    `json:"kind"`
	Name      string                    `json:"name"`
	Namespace string                    `json:"namespace"`
	Exists    bool                      `json:"exists"`
	Identity  *suiteNativeOwnedIdentity `json:"identity,omitempty"`
}

type suiteNativeOwnedIdentity struct {
	Kind       string             `json:"kind"`
	Name       string             `json:"name"`
	Namespace  string             `json:"namespace"`
	UID        string             `json:"uid"`
	Generation int64              `json:"generation"`
	SpecDigest string             `json:"specDigest"`
	Ownership  orkaSuiteOwnership `json:"ownership"`
	Ready      bool               `json:"ready"`
}

// NativeSuite is already verified by the packaged loader. Do not reinterpret
// its declared build-profile images or manufacture a PortableAgent bridge.
func (a *App) prepareSuiteNative(ctx context.Context, suite *workspace.NativeSuite, namespace, selectedProvider string) (*suiteNativePlan, error) {
	if a.Cfg == nil || a.Run == nil || a.Cfg.KubeContext == "" {
		return nil, fmt.Errorf("native suite requires an explicit configured context")
	}
	if suite == nil {
		return nil, fmt.Errorf("native suite packaged input is required")
	}
	for _, selection := range []string{a.Cfg.KubeContext, namespace, selectedProvider} {
		if scaffold.RefuseKeyShapes(selection) != nil {
			return nil, fmt.Errorf("refusing credential-shaped native suite selector")
		}
	}
	if scaffold.ValidateNamespace(namespace) != nil || scaffold.ValidateObjectName(selectedProvider) != nil {
		return nil, fmt.Errorf("native suite requires an explicit valid namespace and Provider")
	}
	if selectedProvider == suite.MemberID {
		return nil, fmt.Errorf("selected Provider collides with the suite-owned Provider; refusing source mutation")
	}
	if suite.RequiredImages == nil || len(suite.RequiredImages) != 0 {
		return nil, fmt.Errorf("native suite requires explicit empty requiredImages")
	}
	if strings.TrimSpace(suite.Model) == "" || strings.TrimSpace(string(suite.Instructions)) == "" {
		return nil, fmt.Errorf("native suite requires exact nonblank packaged model and instructions")
	}
	if err := a.liftPrerequisites(ctx, namespace); err != nil {
		return nil, err
	}
	target, binding, runtime, err := a.readSuiteNativeInputs(ctx, namespace, selectedProvider)
	if err != nil {
		return nil, err
	}
	bundle, err := scaffold.GenerateOrka(scaffold.OrkaSpec{Name: suite.MemberID, Namespace: namespace, Description: suite.Description, Model: suite.Model, Instructions: string(suite.Instructions), ProviderType: binding.Provider.Type, BaseURL: binding.Provider.BaseURL, SecretName: binding.Provider.SecretName, SecretKey: binding.Provider.SecretKey})
	if err != nil {
		return nil, err
	}
	subject := suiteNativeSubject{Suite: suite.Name, SuiteDigest: suite.Digest, GraphDigest: suite.GraphDigest, Member: suite.MemberID, MemberDigest: suite.MemberDigest, InstructionsDigest: suite.InstructionsDigest, Model: suite.Model, CompositionDigest: suite.CompositionDigest, BuildProfileDigest: suite.BuildProfileDigest, Renderer: suite.RendererVersion, RequiredImages: []string{}, Target: target, Binding: binding, Runtime: runtime}
	subject.BindingDigest, err = suiteNativeDigest("kmx.native-orka.binding/v1", struct {
		Model          string             `json:"model"`
		RequiredImages []string           `json:"requiredImages"`
		Binding        suiteNativeBinding `json:"binding"`
		Runtime        suiteNativeRuntime `json:"runtime"`
	}{subject.Model, subject.RequiredImages, binding, runtime})
	if err != nil {
		return nil, err
	}
	var documents [][]byte
	for _, doc := range bundle.Documents() {
		raw, e := yaml.Marshal(doc)
		if e != nil {
			return nil, e
		}
		documents = append(documents, raw)
	}
	subject.PayloadDigest = agentruntime.RenderedBundleDigest(documents)
	// D is computed while RenderedDigest is empty and before ownership is
	// overlaid on write candidates. No digest includes its own annotations.
	subject.RenderedDigest, err = suiteNativeDigest("kmx.native-orka.revision/v1", subject)
	if err != nil {
		return nil, err
	}
	owner := orkaSuiteOwnership{Suite: subject.Suite, Digest: subject.SuiteDigest, Member: subject.Member, Renderer: subject.Renderer, BindingDigest: subject.BindingDigest, RenderedDigest: subject.RenderedDigest}
	checks, err := a.planSuiteOrkaReconcile(ctx, bundle, namespace, owner)
	if err != nil {
		return nil, err
	}
	before := make([]suiteNativePairObservation, 0, len(checks))
	for _, check := range checks {
		observation := suiteNativePairObservation{Kind: check.id.Kind, Name: check.id.Name, Namespace: namespace, Exists: check.outcome != agentruntime.ResourceCreated}
		if observation.Exists {
			identity, err := a.readSuiteNativeOwned(ctx, namespace, check.id.Kind, check.id.Name)
			if err != nil {
				return nil, err
			}
			if identity.UID != check.id.UID || identity.Generation != check.id.Generation {
				return nil, fmt.Errorf("native suite owned pair changed during preparation")
			}
			observation.Identity = &identity
		}
		before = append(before, observation)
	}
	return &suiteNativePlan{Subject: subject, Before: before, bundle: bundle, documents: documents, owner: owner, checks: checks}, nil
}

// Canonical JSON is length/path framed by the existing rendered-digest helper.
// A separate domain document prevents binding/revision/spec identities aliasing.
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
func suiteNativeHelm(labels map[string]string) suiteNativeHelmIdentity {
	return suiteNativeHelmIdentity{Chart: labels["helm.sh/chart"], Instance: labels["app.kubernetes.io/instance"], App: labels["app.kubernetes.io/name"], ManagedBy: labels["app.kubernetes.io/managed-by"]}
}

func (a *App) readSuiteNativeInputs(ctx context.Context, namespace, provider string) (suiteNativeTarget, suiteNativeBinding, suiteNativeRuntime, error) {
	var target suiteNativeTarget
	var binding suiteNativeBinding
	var runtime suiteNativeRuntime
	if a.Cfg == nil || a.Cfg.KubeContext == "" {
		return target, binding, runtime, fmt.Errorf("native suite context identity is unavailable")
	}
	target.Context, target.Namespace = a.Cfg.KubeContext, namespace
	uid, err := a.liftClusterUID(ctx)
	if err != nil {
		return target, binding, runtime, err
	}
	target.ClusterUID = uid
	raw, err := a.orkaCapture(ctx, nil, "get", "namespace", namespace, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return target, binding, runtime, err
	}
	var ns struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name              string  `json:"name"`
			UID               string  `json:"uid"`
			DeletionTimestamp *string `json:"deletionTimestamp"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &ns) != nil || ns.Kind != "Namespace" || ns.Metadata.Name != namespace || ns.Metadata.UID == "" || ns.Metadata.DeletionTimestamp != nil {
		return target, binding, runtime, fmt.Errorf("native suite namespace has no valid live identity")
	}
	target.NamespaceUID = ns.Metadata.UID
	selected, identity, err := a.readLiftProviderBindings(ctx, namespace, provider)
	if err != nil {
		return target, binding, runtime, err
	}
	if selected.Provider.Type != "openai" {
		return target, binding, runtime, fmt.Errorf("native suite supports only a selected openai Provider")
	}
	if identity.ResourceVersion == "" {
		return target, binding, runtime, fmt.Errorf("selected Provider has no resourceVersion")
	}
	binding.Provider = suiteNativeProvider{Name: provider, Namespace: namespace, UID: identity.UID, Generation: identity.Generation, ResourceVersion: identity.ResourceVersion, Type: selected.Provider.Type, BaseURL: selected.Provider.BaseURL, SecretName: selected.Provider.SecretRef.Name, SecretKey: selected.Provider.SecretRef.Key}
	binding.Secret, err = a.readSuiteNativeSecret(ctx, namespace, selected.Provider.SecretRef.Name, selected.Provider.SecretRef.Key)
	if err != nil {
		return target, binding, runtime, err
	}
	runtime, err = a.readSuiteNativeRuntime(ctx, namespace)
	return target, binding, runtime, err
}

type suiteNativeSecretProjection struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name            string `json:"name"`
		Namespace       string `json:"namespace"`
		UID             string `json:"uid"`
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
	Terminating bool `json:"terminating"`
}

func (a *App) readSuiteNativeSecretMetadata(ctx context.Context, namespace, name string) (suiteNativeSecretProjection, error) {
	// Project only fixed metadata fields. In particular neither .data nor its
	// values (nor arbitrary annotations) enter this subprocess's output.
	projection := `go-template={"kind":{{printf "%q" .kind}},"metadata":{"name":{{printf "%q" .metadata.name}},"namespace":{{printf "%q" .metadata.namespace}},"uid":{{printf "%q" .metadata.uid}},"resourceVersion":{{printf "%q" .metadata.resourceVersion}}},"terminating":{{if .metadata.deletionTimestamp}}true{{else}}false{{end}}}`
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
func (a *App) readSuiteNativeSecret(ctx context.Context, namespace, name, key string) (suiteNativeSecret, error) {
	var out suiteNativeSecret
	before, err := a.readSuiteNativeSecretMetadata(ctx, namespace, name)
	if err != nil {
		return out, err
	}
	if err := a.orkaProviderSecretPresent(ctx, namespace, name, key); err != nil {
		return out, err
	}
	after, err := a.readSuiteNativeSecretMetadata(ctx, namespace, name)
	if err != nil {
		return out, err
	}
	if before != after {
		return out, fmt.Errorf("Provider Secret changed during key-presence inspection")
	}
	return suiteNativeSecret{Name: name, Namespace: namespace, UID: before.Metadata.UID, ResourceVersion: before.Metadata.ResourceVersion, Key: key, KeyPresent: true}, nil
}

// Accept the joined flag form emitted by the pinned chart only. Go permits
// single-dash and split forms; recognizing them as conflicts prevents a second
// effective flag from silently overriding the observed runtime identity.
func suiteNativeControllerFlag(args []string, name string) (string, error) {
	value := ""
	count := 0
	for _, arg := range args {
		if arg == "--" || !strings.HasPrefix(arg, "-") {
			return "", fmt.Errorf("Orka native runtime has unsupported positional controller arguments")
		}
		if strings.HasPrefix(arg, "--"+name+"=") {
			count++
			value = strings.TrimPrefix(arg, "--"+name+"=")
			continue
		}
		if arg == "--"+name || arg == "-"+name || strings.HasPrefix(arg, "-"+name+"=") {
			return "", fmt.Errorf("Orka native runtime has an unsupported or ambiguous %s flag", name)
		}
	}
	if count != 1 || value == "" {
		return "", fmt.Errorf("Orka native runtime requires one explicit %s flag", name)
	}
	return value, nil
}
func (a *App) readSuiteNativeRuntime(ctx context.Context, namespace string) (suiteNativeRuntime, error) {
	var out suiteNativeRuntime
	deployments, err := a.orkaDeployments(ctx)
	if err != nil {
		return out, err
	}
	controller, legacy, err := selectOrkaController(deployments)
	if err != nil {
		return out, err
	}
	if legacy || controller.Kind != "Deployment" || controller.Metadata.Namespace != OrkaNamespace || controller.Metadata.UID == "" || controller.Metadata.Generation < 1 || controller.Metadata.DeletionTimestamp != nil {
		return out, fmt.Errorf("native suite requires a valid installed chart controller identity")
	}
	helm := suiteNativeHelm(controller.Metadata.Labels)
	if helm.Chart != "orka-0.2.0" || helm.Instance == "" || helm.App != "orka" || helm.ManagedBy != "Helm" {
		return out, fmt.Errorf("native suite requires the pinned Orka chart identity")
	}
	status := controller.Status
	replicas := controller.Spec.Replicas
	if replicas < 1 || status.ObservedGeneration != controller.Metadata.Generation || status.UpdatedReplicas != replicas || status.ReadyReplicas != replicas || status.AvailableReplicas != replicas || status.UnavailableReplicas != 0 {
		return out, fmt.Errorf("Orka native controller is not Ready for its current generation")
	}
	count := 0
	for _, container := range controller.Spec.Template.Spec.Containers {
		if container.Name != "controller" {
			continue
		}
		count++
		if container.Image != "ghcr.io/orka-agents/orka@"+orkaControllerDigest || len(container.Command) != 0 {
			return out, fmt.Errorf("native suite requires the pinned Orka controller image and entrypoint")
		}
		out.ControllerImage = container.Image
		if out.Mode, err = suiteNativeControllerFlag(container.Args, "controller-mode"); err != nil {
			return out, err
		}
		if out.WatchNamespace, err = suiteNativeControllerFlag(container.Args, "watch-namespace"); err != nil {
			return out, err
		}
		if out.AIWorkerImage, err = suiteNativeControllerFlag(container.Args, "ai-worker-image"); err != nil {
			return out, err
		}
		if out.WorkerAccount, err = suiteNativeControllerFlag(container.Args, "ai-worker-service-account-name"); err != nil {
			return out, err
		}
	}
	if count != 1 || out.Mode != "harness-v2" || out.WatchNamespace != namespace || out.AIWorkerImage != "ghcr.io/orka-agents/orka/ai-worker@"+orkaAIWorkerDigest || scaffold.ValidateObjectName(controller.Spec.Template.Spec.ServiceAccountName) != nil {
		return out, fmt.Errorf("native suite runtime mode, watch namespace, worker image or controller account is unsupported")
	}
	worker, err := a.orkaAIWorkerAccount(ctx)
	if err != nil {
		return out, err
	}
	if worker != out.WorkerAccount {
		return out, fmt.Errorf("Orka configured worker changed during runtime inspection")
	}
	// The existing selector establishes configured-worker/Helm/trust identity.
	// This extra read retains only that account's UID and relevant labels.
	projection := `go-template={"kind":{{printf "%q" .kind}},"metadata":{"name":{{printf "%q" .metadata.name}},"namespace":{{printf "%q" .metadata.namespace}},"uid":{{printf "%q" .metadata.uid}},"labels":{`
	keys := []string{"helm.sh/chart", "app.kubernetes.io/instance", "app.kubernetes.io/name", "app.kubernetes.io/managed-by", "orka.ai/worker", "orka.ai/worker-trust"}
	for i, key := range keys {
		if i > 0 {
			projection += ","
		}
		projection += fmt.Sprintf(`%q:{{printf "%%q" (index .metadata.labels %q)}}`, key, key)
	}
	projection += `}},"terminating":{{if .metadata.deletionTimestamp}}true{{else}}false{{end}}}`
	raw, err := a.orkaCapture(ctx, nil, "-n", OrkaNamespace, "get", "serviceaccount", worker, "-o", projection)
	if err != nil {
		return out, err
	}
	var account struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name      string            `json:"name"`
			Namespace string            `json:"namespace"`
			UID       string            `json:"uid"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
		Terminating bool `json:"terminating"`
	}
	if spec.Unmarshal(raw, &account) != nil || account.Kind != "ServiceAccount" || account.Metadata.Name != worker || account.Metadata.Namespace != OrkaNamespace || account.Metadata.UID == "" || account.Terminating || suiteNativeHelm(account.Metadata.Labels) != helm || account.Metadata.Labels["orka.ai/worker"] != "true" || account.Metadata.Labels["orka.ai/worker-trust"] != "ai" {
		return out, fmt.Errorf("Orka AI worker account has no valid matching runtime identity")
	}
	// The reused helper reselects the controller. Do not splice its worker into
	// an older controller snapshot if installation moved between these reads.
	current, err := a.orkaDeployments(ctx)
	if err != nil {
		return out, err
	}
	again, _, err := selectOrkaController(current)
	if err != nil {
		return out, err
	}
	if !reflect.DeepEqual(controller.Metadata, again.Metadata) || !reflect.DeepEqual(controller.Spec, again.Spec) {
		return out, fmt.Errorf("Orka controller changed during runtime inspection")
	}
	out.ID, out.Version, out.Namespace = "orka", OrkaVersion, OrkaNamespace
	out.ControllerName, out.ControllerUID, out.ControllerGeneration = controller.Metadata.Name, controller.Metadata.UID, controller.Metadata.Generation
	out.ControllerServiceAccount = controller.Spec.Template.Spec.ServiceAccountName
	out.Helm, out.WorkerAccountUID, out.WorkerTrust = helm, account.Metadata.UID, "ai"
	return out, nil
}

func (a *App) verifySuiteNativeInputs(ctx context.Context, subject suiteNativeSubject) error {
	target, binding, runtime, err := a.readSuiteNativeInputs(ctx, subject.Target.Namespace, subject.Binding.Provider.Name)
	if err != nil {
		return fmt.Errorf("native suite subject inspection unknown: %w", err)
	}
	if target != subject.Target || binding != subject.Binding || runtime != subject.Runtime {
		return fmt.Errorf("native suite target, binding or runtime changed; subject is unknown")
	}
	return nil
}

func sameSuiteNativeDecision(before, after orkaReconcileCheck) bool {
	if before.id != after.id || before.outcome != after.outcome || before.markerRefresh != after.markerRefresh {
		return false
	}
	if before.markerRefresh {
		return before.markerRefreshVersion == after.markerRefreshVersion
	}
	if before.outcome == agentruntime.ResourceUpdated {
		b, _ := before.candidate["metadata"].(map[string]any)
		a, _ := after.candidate["metadata"].(map[string]any)
		return b["resourceVersion"] == a["resourceVersion"]
	}
	return true
}

// A non-nil partial result accompanies every placement failure. Never turn an
// ambiguous write into a retry, rollback or successful receipt by observing it.
func (a *App) placeSuiteNative(ctx context.Context, plan *suiteNativePlan) (placement *suiteNativePlacement, err error) {
	placement = &suiteNativePlacement{Resources: []suiteNativeResource{}, plan: plan}
	defer func() {
		if err == nil {
			return
		}
		// Retain bounded, read-only residual observations for every attempted
		// resource, including failures after a successful write or Ready wait.
		// A canceled/unreadable observation remains unknown, never successful.
		for i := range placement.Resources {
			r := &placement.Resources[i]
			if observed, readErr := a.readSuiteNativeOwned(ctx, r.Namespace, r.Kind, r.Name); readErr == nil {
				r.Observed = &observed
			}
		}
	}()
	if plan == nil {
		return placement, fmt.Errorf("native suite plan is required")
	}
	placement.Subject = plan.Subject
	placement.Before = append([]suiteNativePairObservation(nil), plan.Before...)
	placement.Subject.RequiredImages = append([]string{}, plan.Subject.RequiredImages...)
	ns := plan.Subject.Target.Namespace
	if a.Cfg == nil || a.Cfg.KubeContext != plan.Subject.Target.Context {
		return placement, fmt.Errorf("native suite context changed before placement")
	}
	if err := a.guardOrkaMutation(ctx, CreateOptions{Name: plan.Subject.Member, Namespace: ns}, "create or update suite-owned Orka Provider and Agent in "+ns); err != nil {
		return placement, err
	}
	// The guard may wait for confirmation. All inputs and both ownership/schema/
	// admission decisions must still hold before the first persistent mutation.
	if err := a.verifySuiteNativeInputs(ctx, plan.Subject); err != nil {
		return placement, err
	}
	checks, err := a.planSuiteOrkaReconcile(ctx, plan.bundle, ns, plan.owner)
	if err != nil {
		return placement, err
	}
	for i, check := range checks {
		if !sameSuiteNativeDecision(plan.checks[i], check) {
			return placement, fmt.Errorf("native suite owned pair changed after preparation")
		}
	}
	for i, doc := range []map[string]any{plan.bundle.Provider, plan.bundle.Agent} {
		if err := a.verifySuiteNativeInputs(ctx, plan.Subject); err != nil {
			return placement, err
		}
		check, err := a.inspectSuiteOrkaReconcile(ctx, ns, doc, plan.owner)
		if err != nil {
			return placement, err
		}
		if !sameSuiteNativeDecision(checks[i], check) {
			return placement, fmt.Errorf("native suite %s changed before mutation", check.id.Kind)
		}
		resource := suiteNativeResource{Kind: check.id.Kind, Name: check.id.Name, Namespace: ns, UID: check.id.UID, Generation: check.id.Generation, Outcome: check.outcome, AttemptedWrite: check.outcome != agentruntime.ResourceReused || check.markerRefresh}
		placement.Resources = append(placement.Resources, resource)
		r := &placement.Resources[len(placement.Resources)-1]
		id, err := a.applyOrkaReconcile(ctx, ns, check)
		if err != nil {
			r.Ambiguous = r.AttemptedWrite
			return placement, fmt.Errorf("native suite placement stopped; inspect residual resources, no retry or rollback: %w", err)
		}
		r.UID, r.Generation, r.Applied = id.UID, id.Generation, true
		if err := a.waitOrkaReady(ctx, ns, id); err != nil {
			return placement, err
		}
		if err := a.verifySuiteOrkaReconcile(ctx, ns, doc, plan.owner, id); err != nil {
			return placement, err
		}
		observed, err := a.readSuiteNativeOwned(ctx, ns, id.Kind, id.Name)
		if err != nil {
			return placement, err
		}
		if observed.UID != id.UID || observed.Generation != id.Generation || observed.Ownership != plan.owner || !observed.Ready {
			return placement, fmt.Errorf("native suite owned resource changed during final observation")
		}
		r.SpecDigest, r.Ready = observed.SpecDigest, true
	}
	placement.Complete = true
	if err := a.verifySuiteNative(ctx, placement); err != nil {
		placement.Complete = false
		return placement, err
	}
	return placement, nil
}

func (a *App) readSuiteNativeOwned(ctx context.Context, namespace, kind, name string) (suiteNativeOwnedIdentity, error) {
	var out suiteNativeOwnedIdentity
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", orkaPlural(kind), name, "-o", "json")
	if err != nil {
		return out, err
	}
	var live map[string]any
	if spec.Unmarshal(raw, &live) != nil {
		return out, fmt.Errorf("invalid native suite owned resource response")
	}
	var object orkaObject
	if json.Unmarshal(raw, &object) != nil || object.Kind != kind || object.Metadata.Name != name || object.Metadata.Namespace != namespace || object.Metadata.UID == "" || object.Metadata.Generation < 1 || object.Metadata.DeletionTimestamp != nil || live["spec"] == nil {
		return out, fmt.Errorf("native suite owned resource has no valid live identity")
	}
	meta, _ := live["metadata"].(map[string]any)
	ann, _ := meta["annotations"].(map[string]any)
	if orkaHasBundleMarkers(ann) {
		return out, fmt.Errorf("native suite owned resource has mixed ownership")
	}
	field := func(key string) string { s, _ := ann[key].(string); return s }
	owner := orkaSuiteOwnership{Suite: field(orkaSuiteMarker), Digest: field(orkaSuiteDigestMarker), Member: field(orkaSuiteMemberMarker), Renderer: field(orkaSuiteRendererMarker), BindingDigest: field(orkaSuiteBindingMarker), RenderedDigest: field(orkaSuiteRenderedMarker)}
	if err := owner.validate(); err != nil {
		return out, err
	}
	digest, err := suiteNativeDigest("kmx.native-orka.owned-spec/v1", live["spec"])
	if err != nil {
		return out, err
	}
	return suiteNativeOwnedIdentity{Kind: kind, Name: name, Namespace: namespace, UID: object.Metadata.UID, Generation: object.Metadata.Generation, SpecDigest: digest, Ownership: owner, Ready: orkaCurrentGenerationReady(&object, object.Metadata.Generation)}, nil
}

// Verification hook for before/after execution and judging, and run completion.
// Verification is read-only; failed or incomplete attribution is unknown.
func (a *App) verifySuiteNative(ctx context.Context, placement *suiteNativePlacement) error {
	if placement == nil || !placement.Complete || placement.plan == nil || len(placement.Resources) != 2 {
		return fmt.Errorf("native suite placement is incomplete; subject is unknown")
	}
	if err := a.verifySuiteNativeInputs(ctx, placement.Subject); err != nil {
		return err
	}
	plan := placement.plan
	for i, doc := range []map[string]any{plan.bundle.Provider, plan.bundle.Agent} {
		r := placement.Resources[i]
		if !r.Applied || !r.Ready || r.Ambiguous {
			return fmt.Errorf("native suite owned pair is not attributable")
		}
		id := orkaIdentity{Kind: r.Kind, Name: r.Name, UID: r.UID, Generation: r.Generation}
		if err := a.verifySuiteOrkaReconcile(ctx, placement.Subject.Target.Namespace, doc, plan.owner, id); err != nil {
			return err
		}
		current, err := a.readSuiteNativeOwned(ctx, r.Namespace, r.Kind, r.Name)
		if err != nil {
			return err
		}
		if current.UID != r.UID || current.Generation != r.Generation || current.SpecDigest != r.SpecDigest || current.Ownership != plan.owner || !current.Ready {
			return fmt.Errorf("native suite owned resource changed; subject is unknown")
		}
	}
	return nil
}
