package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// Categories are assigned where external evidence is read or validated, not
// inferred later from sanitized transport/error strings.
type kagentEvidenceError struct {
	State string
	Err   error
}

func kagentFailure(state string, err error) error {
	return &kagentEvidenceError{State: state, Err: err}
}

type kagentChildCollisionError struct{ message string }

type kagentDependencyDriftError struct{}

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

func (a *App) observeKagentLiftWorkload(ctx context.Context, namespace string, id kagentIdentity, images *[]kagentEvaluationImage) ([]agentruntime.ResourceEvidence, error) {
	workload, err := a.observeKagentAgentWorkload(ctx, namespace, id, "go", nil, images)
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

func (e *kagentEvidenceError) Error() string { return e.Err.Error() }

func (e *kagentEvidenceError) Unwrap() error { return e.Err }

func (e *kagentChildCollisionError) Error() string { return e.message }

func (*kagentDependencyDriftError) Error() string {
	return "Kagent dependency is absent, terminating or missing its referenced key"
}

func kagentObjectName(doc map[string]any) string {
	metadata, _ := doc["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	return name
}
