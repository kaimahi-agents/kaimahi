package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// Private placement identities contain no authored or credential payload.
type suiteKagentSubject struct {
	Suite, SuiteDigest, GraphDigest, Member, MemberDigest     string
	InstructionsDigest, CompositionDigest, BuildProfileDigest string
	Renderer, ExecutionProfile, Model                         string
	Profile                                                   workspace.RuntimeProfile
	Target                                                    suiteNativeTarget
	Binding                                                   agentruntime.KagentBindings
	Secret                                                    agentruntime.ResourceEvidence
	Runtime                                                   suiteKagentRuntime
	BindingDigest, PayloadDigest, RenderedDigest              string
}
type suiteKagentRuntime struct {
	ServiceName, Namespace, ServiceUID, DeploymentName, DeploymentUID       string
	DeploymentGeneration                                                    int64
	Selector                                                                map[string]string
	ControllerAccount, ControllerImage, AuthMode                            string
	ConfigName, ConfigUID, WatchNamespaces, GoRegistry, GoRepository, GoTag string
	ControllerImages                                                        []kagentEvaluationImage
}

// This frame owns its generated documents, binding value and observed snapshots.
// It is never re-rendered from a caller's mutable NativeSuite.
type suiteKagentPrepared struct {
	subject   suiteKagentSubject
	worker    *App
	bundle    *scaffold.KagentBundle
	documents []map[string]any
	proofs    []kagentObjectProof
}

func (a *App) prepareSuiteKagent(ctx context.Context, s *workspace.NativeSuite, b agentruntime.KagentBindings) (*suiteKagentPrepared, error) {
	if s == nil || s.Runtime != (workspace.RuntimeProfile{ID: "kagent", Version: "v0.10.3", Engine: "go"}) || s.RendererVersion != "kmx.native-kagent/v2" || s.ExecutionProfile != "kmx.kagent-v0.10.3-go-linux-amd64-tool-free-openai/v1" || s.RequiredImages == nil || len(s.RequiredImages) != 0 {
		return nil, fmt.Errorf("suite Kagent requires exact tool-free v0.10.3 Go profile and empty member images")
	}
	if agentruntime.ValidateKagentBindings(b) != nil || b.ModelConfig.Provider != "openai" {
		return nil, fmt.Errorf("suite Kagent requires effective OpenAI bindings")
	}
	if a.validateSuiteNativeSelectors(b.Namespace, s.MemberID) != nil {
		return nil, fmt.Errorf("suite Kagent requires explicit configured context and namespace")
	}
	worker := *a
	cfg := *a.Cfg
	worker.Cfg = &cfg
	worker.guarded = false
	profile := kagentLiftProfile()
	if a.kagentProfile != nil && a.kagentProfile.Version == profile.Version {
		profile = *a.kagentProfile
	}
	worker.kagentProfile = &profile
	bundle, err := scaffold.GenerateSuiteKagent(scaffold.KagentSpec{Name: s.MemberID, Namespace: b.Namespace, Description: s.Description, Runtime: "go", Instructions: string(s.Instructions), ProviderType: b.ModelConfig.Provider, Model: s.Model, BaseURL: b.ModelConfig.BaseURL, SecretName: b.ModelConfig.SecretRef.Name, SecretKey: b.ModelConfig.SecretRef.Key})
	if err != nil {
		return nil, fmt.Errorf("suite Kagent packaged behavior cannot be rendered")
	}
	p := &suiteKagentPrepared{worker: &worker, bundle: bundle}
	target, secret, runtime, err := worker.readSuiteKagentInputs(ctx, b)
	if err != nil {
		return nil, err
	}
	p.subject = suiteKagentSubject{Suite: s.Name, SuiteDigest: s.Digest, GraphDigest: s.GraphDigest, Member: s.MemberID, MemberDigest: s.MemberDigest, InstructionsDigest: s.InstructionsDigest, CompositionDigest: s.CompositionDigest, BuildProfileDigest: s.BuildProfileDigest, Renderer: s.RendererVersion, ExecutionProfile: s.ExecutionProfile, Model: s.Model, Profile: s.Runtime, Target: target, Binding: b, Secret: secret, Runtime: runtime}
	p.subject.BindingDigest, err = suiteNativeDigest("kmx.native-kagent.binding/v1", struct {
		Model   string
		Binding agentruntime.KagentBindings
		Secret  agentruntime.ResourceEvidence
		Runtime suiteKagentRuntime
	}{s.Model, b, secret, runtime})
	if err != nil {
		return nil, err
	}
	raw := [][]byte{}
	for _, doc := range []map[string]any{bundle.ModelConfig, bundle.Agent} {
		body, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("suite Kagent payload cannot be framed")
		}
		raw = append(raw, body)
	}
	p.subject.PayloadDigest, err = suiteNativeDigest("kmx.native-kagent.payload/v1", raw)
	if err != nil {
		return nil, err
	}
	p.subject.RenderedDigest, err = suiteNativeDigest("kmx.native-kagent.revision/v1", p.subject)
	if err != nil {
		return nil, err
	}
	owner := suiteKagentOwner(p.subject)
	if !owner.valid() {
		return nil, fmt.Errorf("suite Kagent ownership identity is invalid")
	}
	for _, doc := range []map[string]any{bundle.ModelConfig, bundle.Agent} {
		desired, err := cloneOrkaDoc(doc)
		if err != nil {
			return nil, err
		}
		meta := desired["metadata"].(map[string]any)
		meta["annotations"] = owner.markers()
		meta["labels"] = map[string]any{"app.kubernetes.io/managed-by": "kmx"}
		proof, err := expectedSuiteKagentProof(desired, b.Namespace, owner)
		if err != nil {
			return nil, err
		}
		p.documents = append(p.documents, desired)
		p.proofs = append(p.proofs, proof)
	}
	return p, nil
}

func (a *App) readSuiteKagentInputs(ctx context.Context, b agentruntime.KagentBindings) (suiteNativeTarget, agentruntime.ResourceEvidence, suiteKagentRuntime, error) {
	var target suiteNativeTarget
	var runtime suiteKagentRuntime
	var secret agentruntime.ResourceEvidence
	fail := func() (suiteNativeTarget, agentruntime.ResourceEvidence, suiteKagentRuntime, error) {
		return target, secret, runtime, fmt.Errorf("suite Kagent target, dependency or exact runtime qualification failed")
	}
	if ctx.Err() != nil {
		return fail()
	}
	target.Context = a.Cfg.KubeContext
	target.Namespace = b.Namespace
	uid, err := a.kagentClusterUID(ctx)
	if err != nil {
		return fail()
	}
	target.ClusterUID = uid
	raw, err := a.kagentCapture(ctx, nil, "get", "namespace", b.Namespace, "-o", "json")
	if err != nil {
		return fail()
	}
	var ns struct {
		Kind     string
		Metadata kagentMetadata
	}
	if json.Unmarshal(raw, &ns) != nil || ns.Kind != "Namespace" || ns.Metadata.Name != b.Namespace || ns.Metadata.UID == "" || ns.Metadata.DeletionTimestamp != nil {
		return fail()
	}
	target.NamespaceUID = ns.Metadata.UID
	secret, err = a.kagentReferenceEvidence(ctx, b.Namespace, "Secret", b.ModelConfig.SecretRef.Name, b.ModelConfig.SecretRef.Key)
	if err != nil {
		return fail()
	}
	service, err := a.preflightKagentInstallation(ctx, "go", b.Namespace)
	if err != nil {
		return fail()
	}
	// The legacy recheck is intentionally not a full suite snapshot. Read fresh
	// qualified images, generation and consumed config values explicitly.
	deployment, err := a.readKagentControllerDeployment(ctx, service)
	if err != nil {
		return fail()
	}
	controller, err := exactKagentContainer(deployment.Spec.Template.Spec.Containers, "controller")
	if err != nil {
		return fail()
	}
	config, err := a.readKagentControllerConfigMap(ctx, service.Namespace, service.ConfigMapName, "go")
	if err != nil {
		return fail()
	}
	if deployment.Metadata.UID != service.DeploymentUID || deployment.Metadata.Generation != service.DeploymentGeneration || deployment.Metadata.ResourceVersion != service.DeploymentResourceVersion || config.Metadata.UID != service.ConfigMapUID || config.Metadata.ResourceVersion != service.ConfigMapResourceVersion || deployment.Spec.Template.Spec.ServiceAccountName != service.ControllerServiceAccount {
		return fail()
	}
	profile := a.selectedKagentProfile()
	auth, authErr := kagentControllerAuthMode(controller)
	if authErr != nil || auth != service.AuthMode || len(controller.Command) != 0 || len(controller.Args) != 0 || !profile.imageMatches(controller.Image, profile.ControllerImage, kagentControllerImageRepo, profile.ControllerDigests[:]) || validateKagentWatchNamespaces(config.Data["WATCH_NAMESPACES"], b.Namespace) != nil {
		return fail()
	}
	var images []kagentEvaluationImage
	if a.observeKagentDeploymentPods(ctx, service.Namespace, deployment, "controller", controller.Image, kagentControllerImageRepo, profile.ControllerDigests[:], &images) != nil || !reflect.DeepEqual(images, service.ControllerImages) {
		return fail()
	}
	if !suiteKagentAMD64(service.ControllerImages) {
		return fail()
	}
	runtime = suiteKagentRuntime{ServiceName: service.Name, Namespace: service.Namespace, ServiceUID: service.UID, DeploymentName: service.DeploymentName, DeploymentUID: service.DeploymentUID, DeploymentGeneration: service.DeploymentGeneration, Selector: copyKagentLabels(service.Selector), ControllerAccount: service.ControllerServiceAccount, ControllerImage: controller.Image, AuthMode: service.AuthMode, ConfigName: service.ConfigMapName, ConfigUID: service.ConfigMapUID, WatchNamespaces: config.Data["WATCH_NAMESPACES"], GoRegistry: config.Data["GO_IMAGE_REGISTRY"], GoRepository: config.Data["GO_IMAGE_REPOSITORY"], GoTag: config.Data["GO_IMAGE_TAG"], ControllerImages: slices.Clone(service.ControllerImages)}
	return target, secret, runtime, nil
}
func suiteKagentAMD64(images []kagentEvaluationImage) bool {
	if len(images) == 0 {
		return false
	}
	for _, image := range images {
		if image.OS != "linux" || image.Architecture != "amd64" {
			return false
		}
	}
	return true
}
func (a *App) verifySuiteKagentInputs(ctx context.Context, p *suiteKagentPrepared) error {
	target, secret, runtime, err := a.readSuiteKagentInputs(ctx, p.subject.Binding)
	if err != nil || !reflect.DeepEqual(target, p.subject.Target) || !reflect.DeepEqual(secret, p.subject.Secret) || !reflect.DeepEqual(runtime, p.subject.Runtime) {
		return fmt.Errorf("suite Kagent prepared inputs changed or cannot be qualified; no later effects attempted")
	}
	return nil
}

type suiteKagentOwnership struct {
	Suite, Digest, Member, Renderer, Binding, Revision string
	Profile                                            workspace.RuntimeProfile
}

func suiteKagentOwner(s suiteKagentSubject) *suiteKagentOwnership {
	return &suiteKagentOwnership{s.Suite, s.SuiteDigest, s.Member, s.Renderer, s.BindingDigest, s.RenderedDigest, s.Profile}
}
func (o *suiteKagentOwnership) valid() bool {
	return o != nil && o.Profile == (workspace.RuntimeProfile{ID: "kagent", Version: "v0.10.3", Engine: "go"}) && o.Renderer == "kmx.native-kagent/v2" && scaffold.ValidateObjectName(o.Suite) == nil && scaffold.ValidateObjectName(o.Member) == nil && strings.HasPrefix(o.Digest, "sha256:") && validOrkaMarkerDigest(strings.TrimPrefix(o.Digest, "sha256:")) && validOrkaMarkerDigest(o.Binding) && validOrkaMarkerDigest(o.Revision)
}
func (o *suiteKagentOwnership) markers() map[string]any {
	return map[string]any{orkaSuiteMarkers[0]: o.Suite, orkaSuiteMarkers[1]: o.Digest, orkaSuiteMarkers[2]: o.Member, orkaSuiteMarkers[3]: o.Renderer, orkaSuiteMarkers[4]: o.Binding, orkaSuiteMarkers[5]: o.Revision}
}
func (o *suiteKagentOwnership) check(ann map[string]string) error {
	if !o.valid() {
		return fmt.Errorf("invalid suite Kagent owner")
	}
	for _, key := range []string{orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker, kagentVersionMarker} {
		if _, present := ann[key]; present {
			return fmt.Errorf("legacy markers block suite Kagent proof")
		}
	}
	for key, value := range o.markers() {
		if ann[key] != value {
			return fmt.Errorf("suite Kagent ownership markers changed")
		}
	}
	return nil
}
func expectedSuiteKagentProof(doc map[string]any, ns string, owner *suiteKagentOwnership) (kagentObjectProof, error) {
	proof, err := commonKagentObjectProof(doc, ns)
	if err != nil {
		return proof, err
	}
	meta := doc["metadata"].(map[string]any)
	ann, err := kagentStringMap(meta["annotations"])
	if err != nil {
		return proof, err
	}
	if err = owner.check(ann); err != nil {
		return proof, err
	}
	proof.suite = owner
	proof.Markers = map[string]string{}
	for key, value := range owner.markers() {
		proof.Markers[key] = value.(string)
	}
	return proof, nil
}
