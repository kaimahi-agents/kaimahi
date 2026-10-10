package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// Categories are assigned where external evidence is read or validated, not
// inferred later from sanitized transport/error strings.
type kagentEvidenceError struct {
	State string
	Err   error
}

func (e *kagentEvidenceError) Error() string { return e.Err.Error() }
func (e *kagentEvidenceError) Unwrap() error { return e.Err }
func kagentFailure(state string, err error) error {
	return &kagentEvidenceError{State: state, Err: err}
}

type kagentChildCollisionError struct{ message string }

func (e *kagentChildCollisionError) Error() string { return e.message }

type kagentDependencyDriftError struct{}

func (*kagentDependencyDriftError) Error() string {
	return "Kagent dependency is absent, terminating or missing its referenced key"
}

func kagentSpecFingerprint(value any) (string, error) {
	canonical, err := canonicalKagentJSONValue(value)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func kagentLiftDocuments(bundle *scaffold.KagentBundle, rendered agentruntime.RenderedBundle) ([]map[string]any, error) {
	docs, err := kagentWriteDocuments(bundle, rendered)
	if err != nil {
		return nil, err
	}
	for _, doc := range docs {
		meta := doc["metadata"].(map[string]any)
		meta["annotations"].(map[string]any)[kagentVersionMarker] = "v0.10.3"
		meta["labels"] = map[string]any{"app.kubernetes.io/managed-by": "kmx"}
	}
	return docs, nil
}

// Not-found is proven only by a successful ignore-not-found read. A malformed
// nonempty response or failed read never authorizes a create.
func (a *App) readKagentLiftObject(ctx context.Context, namespace, kind, name string) (*kagentObject, error) {
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", kagentPlural(kind), name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var object kagentObject
	if json.Unmarshal(raw, &object) != nil || object.APIVersion != scaffold.KagentAPIVersion || object.Kind != kind || object.Metadata.Name != name || object.Metadata.Namespace != namespace || object.Metadata.UID == "" || object.Metadata.Generation < 1 {
		return nil, fmt.Errorf("Kagent primary read returned an invalid identity")
	}
	return &object, nil
}

func (a *App) kagentReferenceEvidence(ctx context.Context, namespace, kind, name, key string) (agentruntime.ResourceEvidence, error) {
	evidence := agentruntime.ResourceEvidence{Kind: kind, Name: name, Namespace: namespace, Key: key}
	if scaffold.ValidateObjectName(name) != nil || (key != "" && scaffold.ValidateKagentSecretKey(key) != nil) {
		return evidence, fmt.Errorf("Kagent dependency reference is invalid")
	}
	// A projection exposes metadata and key presence, never Secret bodies.
	template := "go-template={{.kind}}\n{{.metadata.name}}\n{{.metadata.namespace}}\n{{.metadata.uid}}\n{{.metadata.resourceVersion}}\n{{if .metadata.deletionTimestamp}}terminating{{else}}active{{end}}"
	if key != "" {
		template += "\n{{range $key, $_ := .data}}{{if eq $key " + fmt.Sprintf("%q", key) + "}}present{{end}}{{end}}"
	}
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", strings.ToLower(kind), name, "--ignore-not-found=true", "-o", template)
	if err != nil {
		return evidence, fmt.Errorf("cannot read Kagent dependency identity: %w", err)
	}
	parts := strings.Split(string(raw), "\n")
	want := 6
	if key != "" {
		want = 7
	}
	if len(parts) != want || parts[0] != kind || parts[1] != name || parts[2] != namespace || parts[3] == "" || parts[4] == "" || parts[5] != "active" || key != "" && parts[6] != "present" {
		return evidence, &kagentDependencyDriftError{}
	}
	evidence.UID, evidence.ResourceVersion = parts[3], parts[4]
	return evidence, nil
}

func (a *App) kagentLiftDependencies(ctx context.Context, bundle *scaffold.KagentBundle) ([]agentruntime.ResourceEvidence, map[string]kagentToolSnapshot, error) {
	namespace := kagentObjectNamespace(bundle.Agent)
	model := bundle.ModelConfig["spec"].(map[string]any)
	secret, err := a.kagentReferenceEvidence(ctx, namespace, "Secret", model["apiKeySecret"].(string), model["apiKeySecretKey"].(string))
	if err != nil {
		return nil, nil, err
	}
	evidence := []agentruntime.ResourceEvidence{secret}
	tools, err := a.snapshotKagentTools(ctx, bundle)
	if err != nil {
		return nil, nil, err
	}
	if len(tools) > 0 {
		profile := a.selectedKagentProfile()
		raw, err := a.kagentCapture(ctx, nil, "get", "crd", "remotemcpservers.kagent.dev", "-o", "json")
		if err != nil {
			return nil, nil, err
		}
		if err = validateKagentCRD(raw, kagentCRDIdentity{Name: "remotemcpservers.kagent.dev", Kind: "RemoteMCPServer", Plural: "remotemcpservers", SchemaSHA256: profile.RemoteSchema}); err != nil {
			return nil, nil, fmt.Errorf("RemoteMCPServer exact-version schema: %w", err)
		}
	}
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		snapshot := tools[name]
		raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", "remotemcpservers.kagent.dev", name, "-o", "json")
		if err != nil {
			return nil, nil, err
		}
		var object struct {
			Metadata kagentMetadata `json:"metadata"`
			Spec     struct {
				HeadersFrom []struct {
					Value     string                            `json:"value"`
					ValueFrom *struct{ Type, Name, Key string } `json:"valueFrom"`
				} `json:"headersFrom"`
				TLS *struct{ CaCertSecretRef, CaCertSecretKey string } `json:"tls"`
			} `json:"spec"`
		}
		if json.Unmarshal(raw, &object) != nil || object.Metadata.UID != snapshot.UID || object.Metadata.Generation != snapshot.Generation || object.Metadata.ResourceVersion == "" {
			return nil, nil, fmt.Errorf("RemoteMCPServer dependency changed or has invalid identity")
		}
		fingerprint, err := kagentSpecFingerprint(struct {
			SpecDigest string
			Tools      []string
		}{snapshot.SpecDigest, snapshot.DiscoveredTools})
		if err != nil {
			return nil, nil, err
		}
		evidence = append(evidence, agentruntime.ResourceEvidence{Kind: "RemoteMCPServer", Name: name, Namespace: namespace, UID: snapshot.UID, ResourceVersion: object.Metadata.ResourceVersion, Generation: snapshot.Generation, SpecDigest: fingerprint})
		for _, header := range object.Spec.HeadersFrom {
			if header.Value != "" || header.ValueFrom == nil || (header.ValueFrom.Type != "Secret" && header.ValueFrom.Type != "ConfigMap") {
				return nil, nil, fmt.Errorf("RemoteMCPServer headers require explicit Secret or ConfigMap references; inline values are not qualified for lift")
			}
			ref, err := a.kagentReferenceEvidence(ctx, namespace, header.ValueFrom.Type, header.ValueFrom.Name, header.ValueFrom.Key)
			if err != nil {
				return nil, nil, err
			}
			evidence = append(evidence, ref)
		}
		if tls := object.Spec.TLS; tls != nil && tls.CaCertSecretRef != "" {
			ref, err := a.kagentReferenceEvidence(ctx, namespace, "Secret", tls.CaCertSecretRef, tls.CaCertSecretKey)
			if err != nil {
				return nil, nil, err
			}
			evidence = append(evidence, ref)
		}
	}
	return evidence, tools, nil
}

func (a *App) verifyKagentLiftDependencies(ctx context.Context, bundle *scaffold.KagentBundle, expected []agentruntime.ResourceEvidence, tools map[string]kagentToolSnapshot) error {
	current, _, err := a.kagentLiftDependencies(ctx, bundle)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, expected) {
		return fmt.Errorf("Kagent dependency identity, spec, reference or discovered tools changed")
	}
	return a.verifyKagentToolSnapshots(ctx, bundle, tools)
}

func (a *App) kagentLiftWorkload(ctx context.Context, namespace string, id kagentIdentity) ([]agentruntime.ResourceEvidence, error) {
	workload, err := a.verifyKagentAgentWorkload(ctx, namespace, id, "go", nil)
	if err != nil {
		return nil, err
	}
	evidence := []agentruntime.ResourceEvidence{
		{Kind: "Deployment", Name: id.Name, Namespace: namespace, UID: workload.DeploymentUID, ResourceVersion: workload.DeploymentResourceVersion, Generation: workload.DeploymentGeneration, SpecDigest: workload.DeploymentSpecDigest},
		{Kind: "Service", Name: id.Name, Namespace: namespace, UID: workload.ServiceUID, ResourceVersion: workload.ServiceResourceVersion},
	}
	for _, kind := range []string{"ServiceAccount", "Secret"} {
		// The generated Secret is projected to metadata only, not read as JSON.
		template := "go-template={{.kind}}\n{{.metadata.name}}\n{{.metadata.namespace}}\n{{.metadata.uid}}\n{{.metadata.resourceVersion}}\n{{if .metadata.deletionTimestamp}}terminating{{else}}active{{end}}\n{{range .metadata.ownerReferences}}{{if .controller}}{{.apiVersion}}|{{.kind}}|{{.name}}|{{.uid}}\n{{end}}{{end}}"
		raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", kagentPlural(kind), id.Name, "-o", template)
		if err != nil {
			return nil, err
		}
		parts := strings.Split(string(raw), "\n")
		if len(parts) < 6 || parts[0] != kind || parts[1] == "" || parts[2] == "" || parts[3] == "" || parts[4] == "" || (parts[5] != "active" && parts[5] != "terminating") {
			return nil, kagentFailure("unreadable", fmt.Errorf("generated Kagent %s returned malformed identity projection", kind))
		}
		if len(parts) != 8 || parts[1] != id.Name || parts[2] != namespace || parts[5] != "active" || parts[6] != scaffold.KagentAPIVersion+"|Agent|"+id.Name+"|"+id.UID || parts[7] != "" {
			return nil, kagentFailure("drift", fmt.Errorf("generated Kagent %s has invalid identity or owner", kind))
		}
		evidence = append(evidence, agentruntime.ResourceEvidence{Kind: kind, Name: id.Name, Namespace: namespace, UID: parts[3], ResourceVersion: parts[4]})
	}
	return evidence, nil
}

func (a *App) deployKagentBundle(ctx context.Context, bundle *scaffold.KagentBundle, rendered agentruntime.RenderedBundle, opt kagentLiftOperation) (result agentruntime.DeployResult, err error) {
	namespace, name := kagentObjectNamespace(bundle.Agent), kagentObjectName(bundle.Agent)
	if a.selectedKagentProfile().Version != "v0.10.3" {
		return result, fmt.Errorf("Kagent lift requires the exact v0.10.3 profile")
	}
	runtimeName := bundle.Agent["spec"].(map[string]any)["declarative"].(map[string]any)["runtime"].(string)
	if _, _, _, err = a.selectedKagentProfile().runtimeImage(runtimeName); err != nil {
		return result, err
	}
	operationCtx := ctx
	ctx, cancel := a.waitContext(ctx, "kagent-preflight", kagentPreflightTimeout)
	defer cancel()
	service, err := a.preflightKagentInstallation(ctx, runtimeName, namespace)
	if err != nil {
		return result, err
	}
	clusterUID, err := a.kagentClusterUID(ctx)
	if err != nil {
		return result, err
	}
	if opt.ClusterUID == "" || clusterUID != opt.ClusterUID {
		return result, fmt.Errorf("Kagent selected destination cluster changed before deploy; no dry-run or writes attempted")
	}
	target := agentruntime.DeployTarget{Runtime: agentruntime.Kagent, Context: a.Cfg.KubeContext, Namespace: namespace}
	if opt.Expected != nil {
		status, err := a.kagentBundleStatus(ctx, agentruntime.AgentRef{Runtime: agentruntime.Kagent, Context: target.Context, Namespace: namespace, Name: name}, agentruntime.StatusOptions{Desired: &rendered, Expected: opt.Expected})
		if err != nil {
			return result, err
		}
		if kagentStatusField(status, "state") != "ready" {
			return result, fmt.Errorf("Kagent receipt-bound reuse conflict: %s (%s)", kagentStatusField(status, "state"), kagentStatusField(status, "detail"))
		}
		if opt.Recheck != nil {
			if err = opt.Recheck(); err != nil {
				return result, err
			}
		}
		result.Receipt = *opt.Expected
		result.Receipt.Resources = slices.Clone(result.Receipt.Resources)
		for i := range result.Receipt.Resources {
			result.Receipt.Resources[i].Outcome = agentruntime.ResourceReused
		}
		result.Ref = agentruntime.AgentRef{Runtime: agentruntime.Kagent, Context: target.Context, Namespace: namespace, Kind: "agents.kagent.dev", Name: name, UID: result.Receipt.Resources[1].UID}
		return result, nil
	}
	docs, err := kagentLiftDocuments(bundle, rendered)
	if err != nil {
		return result, err
	}
	absent := func() error {
		for _, doc := range docs {
			object, err := a.readKagentLiftObject(ctx, namespace, doc["kind"].(string), name)
			if err != nil {
				return fmt.Errorf("cannot establish primary absence: %w", err)
			}
			if object != nil {
				return fmt.Errorf("Kagent lift conflict: primary already exists without a complete matching receipt; no adoption or updates")
			}
		}
		return a.kagentGeneratedChildrenAbsent(ctx, namespace, name)
	}
	if err = absent(); err != nil {
		return result, err
	}
	dependencies, tools, err := a.kagentLiftDependencies(ctx, bundle)
	if err != nil {
		return result, err
	}
	admitted := make([]kagentObjectProof, len(docs))
	for i, doc := range docs {
		if err = a.reverifyKagentPlatformAndCluster(ctx, service, clusterUID); err != nil {
			return result, err
		}
		body, err := json.Marshal(doc)
		if err != nil {
			return result, err
		}
		admissionCtx, cancelAdmission := a.waitContext(ctx, "kagent-admission", 0)
		raw, err := a.kagentCapture(admissionCtx, body, "-n", namespace, "create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
		cancelAdmission()
		if err != nil {
			return result, fmt.Errorf("strict Kagent server dry-run failed; no resources changed: %w", err)
		}
		admitted[i], err = validateKagentAdmission(raw, doc, namespace)
		if err != nil {
			return result, fmt.Errorf("strict Kagent server dry-run mutated reviewed object: %w", err)
		}
	}
	if err = absent(); err != nil {
		return result, err
	}
	if err = a.verifyKagentLiftDependencies(ctx, bundle, dependencies, tools); err != nil {
		return result, err
	}
	if err = a.reverifyKagentPlatformAndCluster(ctx, service, clusterUID); err != nil {
		return result, err
	}
	if opt.Recheck != nil {
		if err = opt.Recheck(); err != nil {
			return result, err
		}
	}
	if opt.Plan {
		a.notef("ModelConfig/%s: new; Agent/%s: new (strict dry-run only)", name, name)
		return result, nil
	}
	cancel()
	ctx = operationCtx
	created := []string{}
	defer func() {
		if err != nil {
			a.notef("Stopped; no retry, adoption, update, rollback or receipt. Confirmed creations: %s", strings.Join(created, ", "))
		}
	}()
	identities := make([]kagentIdentity, 0, 2)
	for i, doc := range docs {
		if err = a.reverifyKagentPlatformAndCluster(ctx, service, clusterUID); err != nil {
			return result, err
		}
		if err = a.verifyKagentControllerRBAC(ctx, service, namespace); err != nil {
			return result, err
		}
		if err = a.verifyKagentLiftDependencies(ctx, bundle, dependencies, tools); err != nil {
			return result, err
		}
		if opt.Recheck != nil {
			if err = opt.Recheck(); err != nil {
				return result, err
			}
		}
		if i == 1 {
			model, readErr := a.readKagentObject(ctx, namespace, identities[0])
			if readErr != nil {
				return result, readErr
			}
			if model.Status.ObservedGeneration != identities[0].Generation || !kagentObjectConditionTrue(model.Status.Conditions, "Accepted", identities[0]) {
				return result, fmt.Errorf("Kagent ModelConfig is no longer current-generation Accepted before Agent create")
			}
		}
		if err = a.kagentAbsent(ctx, namespace, doc); err != nil {
			return result, err
		}
		if err = a.kagentGeneratedChildrenAbsent(ctx, namespace, name); err != nil {
			return result, err
		}
		id, createErr := a.createKagentObject(ctx, namespace, doc, admitted[i])
		if createErr != nil {
			return result, createErr
		}
		created = append(created, id.Kind+"/"+id.Name+" UID "+id.UID)
		identities = append(identities, id)
		readyCtx, cancelReady := a.waitContext(ctx, "kagent-readiness-"+id.Kind, kagentReadinessTimeout)
		readyErr := a.waitKagentReady(readyCtx, namespace, id)
		cancelReady()
		if readyErr != nil {
			return result, readyErr
		}
	}
	workload, err := a.kagentLiftWorkload(ctx, namespace, identities[1])
	if err != nil {
		return result, err
	}
	result.Ref = agentruntime.AgentRef{Runtime: agentruntime.Kagent, Context: target.Context, Namespace: namespace, Kind: "agents.kagent.dev", Name: name, UID: identities[1].UID}
	result.Receipt = agentruntime.DeployReceipt{Bundle: name, PortableDigest: rendered.PortableDigest(), RenderedDigest: rendered.RenderedDigest(), Target: target, RuntimeVersion: "v0.10.3", ClusterUID: clusterUID, Ready: true, ObservedAt: time.Now().UTC(), Dependencies: dependencies, Workload: workload}
	for _, id := range identities {
		object, err := a.readKagentObject(ctx, namespace, id)
		if err != nil {
			return agentruntime.DeployResult{}, err
		}
		if _, detail := kagentPrimaryReadiness(object, id); detail != "" {
			return agentruntime.DeployResult{}, fmt.Errorf("Kagent primary is no longer current-generation Accepted and Ready: %s", detail)
		}
		fingerprint, err := kagentSpecFingerprint(id.Proof.Spec)
		if err != nil {
			return agentruntime.DeployResult{}, err
		}
		result.Receipt.Resources = append(result.Receipt.Resources, agentruntime.ResourceResult{Kind: id.Kind, Name: id.Name, Namespace: namespace, UID: id.UID, Generation: id.Generation, Outcome: agentruntime.ResourceCreated})
		result.Receipt.Proofs = append(result.Receipt.Proofs, agentruntime.ResourceEvidence{Kind: id.Kind, Name: id.Name, Namespace: namespace, UID: id.UID, Generation: id.Generation, SpecDigest: fingerprint})
	}
	// Revalidation is mandatory after readiness, not an optimistic success receipt.
	if err = a.verifyKagentLiftDependencies(ctx, bundle, dependencies, tools); err != nil {
		return agentruntime.DeployResult{}, err
	}
	current, err := a.kagentLiftWorkload(ctx, namespace, identities[1])
	if err != nil {
		return agentruntime.DeployResult{}, err
	}
	if !reflect.DeepEqual(current, workload) {
		return agentruntime.DeployResult{}, fmt.Errorf("Kagent generated workload changed before receipt")
	}
	if err = a.reverifyKagentPlatformAndCluster(ctx, service, clusterUID); err != nil {
		return agentruntime.DeployResult{}, err
	}
	if err = a.verifyKagentControllerRBAC(ctx, service, namespace); err != nil {
		return agentruntime.DeployResult{}, err
	}
	for _, id := range identities {
		object, readErr := a.readKagentObject(ctx, namespace, id)
		if readErr != nil {
			return agentruntime.DeployResult{}, readErr
		}
		if _, detail := kagentPrimaryReadiness(object, id); detail != "" {
			return agentruntime.DeployResult{}, fmt.Errorf("Kagent primary lost current-generation readiness before receipt: %s", detail)
		}
	}
	return result, nil
}
