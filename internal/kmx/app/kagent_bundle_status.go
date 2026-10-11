package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"

	"golang.org/x/sys/unix"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type kagentBundleTargetStatus struct {
	Context        string `json:"context"`
	Namespace      string `json:"namespace"`
	ClusterUID     string `json:"clusterUID,omitempty"`
	State          string `json:"state"`
	Detail         string `json:"detail,omitempty"`
	NoReceipt      bool   `json:"noReceipt,omitempty"`
	Ready          bool   `json:"ready"`
	AgentUID       string `json:"agentUID,omitempty"`
	ModelConfigUID string `json:"modelConfigUID,omitempty"`
	DeploymentUID  string `json:"deploymentUID,omitempty"`
}
type kagentBundleStatusReport struct {
	Bundle         string                     `json:"bundle"`
	Runtime        string                     `json:"runtime"`
	RuntimeVersion string                     `json:"runtimeVersion"`
	PortableDigest string                     `json:"portableDigest"`
	RenderedDigest string                     `json:"renderedDigest"`
	Targets        []kagentBundleTargetStatus `json:"targets"`
}

func (a *App) statusKagentBundle(opt BundleStatusOptions) error {
	format, err := bundleStatusFormat(opt.Output)
	if err != nil {
		return err
	}
	worker, bundle, adapter, rendered, recheck, err := a.prepareKagentBundle(opt.BundleDir, opt.Bindings, opt.Context, opt.Namespace)
	if err != nil {
		return err
	}
	desired, err := kagentBundleFromRendered(rendered)
	if err != nil {
		return err
	}
	runtimeName := desired.Agent["spec"].(map[string]any)["declarative"].(map[string]any)["runtime"].(string)
	if _, _, _, err = worker.selectedKagentProfile().runtimeImage(runtimeName); err != nil {
		return err
	}
	namespace, name := kagentObjectNamespace(desired.Agent), kagentObjectName(desired.Agent)
	target := kagentBundleTargetStatus{Context: opt.Context, Namespace: namespace, State: "unreadable", NoReceipt: true}
	ctx := a.operationContext()
	clusterUID, readErr := worker.kagentClusterUID(ctx)
	if readErr == nil {
		target.ClusterUID = clusterUID
		root, err := openKagentStatusBundle(bundle, adapter.lift.bundleInfo)
		if err != nil {
			return err
		}
		expected, err := readKagentLiftReceiptAt(root, kagentLiftReceiptName(opt.Context, namespace, clusterUID), agentruntime.DeployTarget{Runtime: agentruntime.Kagent, Context: opt.Context, Namespace: namespace}, clusterUID, name)
		root.Close()
		if err != nil {
			target.Detail = "selected target receipt is invalid or unreadable"
		} else {
			target.NoReceipt = expected == nil
			status, err := adapter.Status(ctx, agentruntime.AgentRef{Runtime: agentruntime.Kagent, Context: opt.Context, Namespace: namespace, Name: name}, agentruntime.StatusOptions{Desired: &rendered, Expected: expected})
			if err != nil {
				return err
			}
			target.State, target.Detail = kagentStatusField(status, "state"), kagentStatusField(status, "detail")
			target.Ready = target.State == "ready"
			target.AgentUID, target.ModelConfigUID, target.DeploymentUID = kagentStatusField(status, "agentUID"), kagentStatusField(status, "modelConfigUID"), kagentStatusField(status, "deploymentUID")
		}
	} else {
		target.Detail = "cannot confirm selected destination cluster identity"
	}
	if err = recheck(); err != nil {
		return err
	}
	report := kagentBundleStatusReport{Bundle: name, Runtime: "kagent", RuntimeVersion: "v0.10.3", PortableDigest: rendered.PortableDigest(), RenderedDigest: rendered.RenderedDigest(), Targets: []kagentBundleTargetStatus{target}}
	if format == "json" {
		return json.NewEncoder(a.Out).Encode(report)
	}
	fmt.Fprintf(a.Out, "%s\t%s\t%s\t%s\n", target.Context, target.Namespace, target.State, target.Detail)
	return nil
}

// Refuse pathname rebinding before receipt reads or live status observation.
// Status acquires no lock and creates no receipt history.
func openKagentStatusBundle(bundle string, prepared os.FileInfo) (*os.File, error) {
	root, err := os.OpenFile(bundle, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat()
	if err != nil || !os.SameFile(opened, prepared) {
		root.Close()
		return nil, fmt.Errorf("Kagent bundle directory changed while opening receipt history")
	}
	return root, nil
}

func kagentStatusField(status agentruntime.Status, key string) string {
	for _, field := range status.Fields {
		if field.Label == key {
			return field.Value
		}
	}
	return ""
}

func kagentPrimaryReadiness(object *kagentObject, id kagentIdentity) (string, string) {
	if object.Status.ObservedGeneration != id.Generation {
		return "failed", "pending: stale or missing observed generation"
	}
	if !kagentObjectConditionTrue(object.Status.Conditions, "Accepted", id) {
		for _, condition := range object.Status.Conditions {
			if condition.Type == "Accepted" && condition.Status == "False" && (condition.ObservedGeneration == id.Generation || id.Kind == "ModelConfig" && condition.ObservedGeneration == 0) {
				return "failed", "current-generation primary rejection"
			}
		}
		return "failed", "pending: primary acceptance unknown"
	}
	if id.Kind == "Agent" && !kagentReadyConditionTrue(object.Status.Conditions, id.Generation) {
		return "failed", "pending or failed: Agent is not current-generation DeploymentReady"
	}
	return "", ""
}

func kagentEvidenceState(err error) string {
	var failure *kagentEvidenceError
	if errors.As(err, &failure) {
		return failure.State
	}
	return "unreadable"
}

func (a *App) kagentBundleStatus(ctx context.Context, ref agentruntime.AgentRef, opt agentruntime.StatusOptions) (agentruntime.Status, error) {
	result := agentruntime.Status{Agent: ref}
	finish := func(state, detail string) (agentruntime.Status, error) {
		result.Fields = append(result.Fields, agentruntime.Field{Label: "state", Value: state}, agentruntime.Field{Label: "detail", Value: detail})
		return result, nil
	}
	if opt.Desired == nil {
		return result, fmt.Errorf("Kagent status requires Desired rendered bundle")
	}
	bundle, err := kagentBundleFromRendered(*opt.Desired)
	if err != nil {
		return result, err
	}
	if ref.Runtime != agentruntime.Kagent || ref.Name != kagentObjectName(bundle.Agent) || ref.Namespace != kagentObjectNamespace(bundle.Agent) || a.Cfg == nil || ref.Context != a.Cfg.KubeContext || a.selectedKagentProfile().Version != "v0.10.3" {
		return result, fmt.Errorf("Kagent Desired bundle does not match explicit status target/profile")
	}
	runtimeName := bundle.Agent["spec"].(map[string]any)["declarative"].(map[string]any)["runtime"].(string)
	if _, _, _, err = a.selectedKagentProfile().runtimeImage(runtimeName); err != nil {
		return result, err
	}
	ctx, cancel := a.waitContext(ctx, "kagent-preflight", kagentPreflightTimeout)
	defer cancel()
	service, err := a.preflightKagentInstallation(ctx, runtimeName, ref.Namespace)
	if err != nil {
		return finish("unreadable", "exact installed runtime qualification could not be proven")
	}
	clusterUID, err := a.kagentClusterUID(ctx)
	if err != nil {
		return finish("unreadable", "cluster identity unreadable")
	}
	expected := opt.Expected
	if expected != nil {
		if expected.Target.Runtime != ref.Runtime || expected.Target.Context != ref.Context || expected.Target.Namespace != ref.Namespace || expected.RuntimeVersion != "v0.10.3" || expected.ClusterUID != clusterUID || expected.Bundle != ref.Name || !expected.Ready || expected.ObservedAt.IsZero() || len(expected.Resources) != 2 || len(expected.Proofs) != 2 || len(expected.Workload) != 4 || len(expected.Dependencies) == 0 {
			return finish("unreadable", "receipt does not bind this runtime/version/target")
		}
		if expected.PortableDigest != opt.Desired.PortableDigest() || expected.RenderedDigest != opt.Desired.RenderedDigest() {
			return finish("drift", "desired source or bindings differ from saved deployment")
		}
	}
	docs, err := kagentLiftDocuments(bundle, *opt.Desired)
	if err != nil {
		return result, err
	}
	identities := make([]kagentIdentity, 2)
	objects := make([]*kagentObject, 2)
	for i, kind := range []string{"ModelConfig", "Agent"} {
		object, err := a.readKagentLiftObject(ctx, ref.Namespace, kind, ref.Name)
		if err != nil {
			return finish("unreadable", "primary resource could not be read")
		}
		objects[i] = object
		if object == nil {
			continue
		}
		label := "modelConfigUID"
		if kind == "Agent" {
			label = "agentUID"
			result.Agent.UID = object.Metadata.UID
		}
		result.Fields = append(result.Fields, agentruntime.Field{Label: label, Value: object.Metadata.UID})
		proof, err := expectedKagentObjectProof(docs[i], ref.Namespace)
		if err != nil {
			return result, err
		}
		identities[i] = kagentIdentity{Kind: kind, Name: ref.Name, UID: object.Metadata.UID, Generation: object.Metadata.Generation, Proof: proof}
	}
	if objects[0] == nil && objects[1] == nil {
		if expected != nil {
			return finish("drift", "saved primary resources are absent")
		}
		if err = a.kagentGeneratedChildrenAbsent(ctx, ref.Namespace, ref.Name); err != nil {
			var collision *kagentChildCollisionError
			if errors.As(err, &collision) {
				return finish("drift", "generated-child collision without primary placement")
			}
			return finish("unreadable", "generated-child absence could not be proven")
		}
		if err = a.reverifyKagentPlatformAndCluster(ctx, service, clusterUID); err != nil {
			return finish("unreadable", "runtime or cluster changed during observation")
		}
		return finish("absent", "both primary resources successfully read as absent")
	}
	if objects[0] == nil || objects[1] == nil {
		return finish("drift", "partial placement: Agent or ModelConfig absent")
	}
	for i, object := range objects {
		id := identities[i]
		if object.Metadata.DeletionTimestamp != nil || object.Metadata.Labels["app.kubernetes.io/managed-by"] != "kmx" {
			return finish("drift", "primary ownership or termination changed")
		}
		if err = validateKagentProofSpecAndMarkers(object.Spec, object.Metadata.Annotations, id.Proof); err != nil {
			return finish("drift", "primary ownership, digest or admitted spec differs from desired")
		}
		if expected != nil {
			saved, p := expected.Resources[i], expected.Proofs[i]
			fingerprint, _ := kagentSpecFingerprint(id.Proof.Spec)
			if saved.Kind != id.Kind || saved.Name != id.Name || saved.Namespace != ref.Namespace || saved.UID != id.UID || saved.Generation != id.Generation || p.UID != id.UID || p.SpecDigest != fingerprint {
				return finish("drift", "primary identity, generation or saved spec changed")
			}
		}
	}
	dependencies, tools, err := a.kagentLiftDependencies(ctx, bundle)
	if err != nil {
		var drift *kagentDependencyDriftError
		if errors.As(err, &drift) {
			return finish("drift", "dependency is absent, terminating or missing its referenced key")
		}
		return finish("unreadable", "dependency qualification or reads failed")
	}
	if expected != nil && !reflect.DeepEqual(dependencies, expected.Dependencies) {
		return finish("drift", "dependency identity, reference, spec or discovered tools changed")
	}
	for i, object := range objects {
		if state, detail := kagentPrimaryReadiness(object, identities[i]); state != "" {
			return finish(state, detail)
		}
	}
	workload, err := a.kagentLiftWorkload(ctx, ref.Namespace, identities[1])
	if err != nil {
		return finish(kagentEvidenceState(err), "owned workload readiness or identity could not be proven")
	}
	result.Fields = append(result.Fields, agentruntime.Field{Label: "deploymentUID", Value: workload[0].UID})
	if expected != nil && !reflect.DeepEqual(workload, expected.Workload) {
		return finish("drift", "owned workload identity, complete Deployment spec or resource version changed")
	}
	if err = a.verifyKagentLiftDependencies(ctx, bundle, dependencies, tools); err != nil {
		return finish("unreadable", "dependencies changed during observation")
	}
	for _, id := range identities {
		object, readErr := a.readKagentObject(ctx, ref.Namespace, id)
		if readErr != nil {
			return finish(kagentEvidenceState(readErr), "primary identity, read or admitted spec changed during observation")
		}
		if state, detail := kagentPrimaryReadiness(object, id); state != "" {
			return finish(state, detail)
		}
	}
	if err = a.reverifyKagentPlatformAndCluster(ctx, service, clusterUID); err != nil {
		return finish("unreadable", "runtime or cluster changed during observation")
	}
	if expected == nil {
		return finish("failed", "unbound: no complete selected-target deployment receipt; live readiness is not a saved deployment")
	}
	return finish("ready", "receipt-bound deployment readiness only; no execution success claimed")
}
