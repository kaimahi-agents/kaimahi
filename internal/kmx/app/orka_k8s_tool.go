package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

const (
	quickstartK8sTool          = "k8s-get-resources"
	quickstartK8sToolPolicy    = "kmx-k8s-tool-gateway"
	quickstartK8sToolAuthority = "https://1.1.1.1/resources"
)

const quickstartK8sInstructions = "For questions about live Kubernetes resources, call k8s-get-resources and answer only from its output. Never invent resource names. Copy resource names exactly. This tool lists resources read-only; it cannot change the cluster."

// The controller's configured worker account is authoritative; matching chart
// identity and AI trust labels keep an unrelated account from gaining access.
func (a *App) orkaAIWorkerAccount(ctx context.Context) (string, error) {
	deployments, err := a.orkaDeployments(ctx)
	if err != nil {
		return "", err
	}
	controller, legacy, err := selectOrkaController(deployments)
	if err != nil {
		return "", err
	}
	labels := controller.Metadata.Labels
	if legacy || labels["helm.sh/chart"] == "" || labels["app.kubernetes.io/name"] != "orka" ||
		labels["app.kubernetes.io/instance"] == "" ||
		labels["app.kubernetes.io/managed-by"] != "Helm" {
		return "", fmt.Errorf("Orka controller Deployment must have nonempty chart and instance, app name=orka and managed-by=Helm labels")
	}
	const flag = "--ai-worker-service-account-name="
	const flagName = "ai-worker-service-account-name"
	worker := ""
	controllers := 0
	flags := 0
	splitFlag := false
	for _, container := range controller.Spec.Template.Spec.Containers {
		if container.Name != "controller" {
			continue
		}
		controllers++
		for _, arg := range container.Args {
			switch {
			case strings.HasPrefix(arg, flag):
				flags++
				worker = strings.TrimPrefix(arg, flag)
			case arg == "--"+flagName, arg == "-"+flagName:
				flags++
				splitFlag = true
			case strings.HasPrefix(arg, "-"+flagName+"="):
				// Go flag parsing accepts single-dash forms too. Never select a
				// name that might differ from the controller's effective one.
				flags++
			}
		}
	}
	if controllers != 1 {
		return "", fmt.Errorf("Orka controller must have exactly one controller container")
	}
	if flags > 1 {
		return "", fmt.Errorf("Orka controller has multiple %s flags", flag)
	}
	if splitFlag {
		return "", fmt.Errorf("Orka controller has an unsupported split %s flag; use the joined form", flagName)
	}
	if flags != 1 || worker == "" {
		return "", fmt.Errorf("Orka controller must have one nonempty %s flag", flag)
	}
	if err := scaffold.ValidateObjectName(worker); err != nil {
		return "", fmt.Errorf("Orka controller has invalid %s account name", flag)
	}

	data, err := a.orkaCapture(ctx, nil, "-n", OrkaNamespace, "get", "serviceaccounts", "-o", "json")
	if err != nil {
		return "", fmt.Errorf("cannot list Orka ServiceAccounts in %s: %w", OrkaNamespace, err)
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil || list.Items == nil {
		return "", fmt.Errorf("cannot read Orka ServiceAccounts in %s: malformed list", OrkaNamespace)
	}
	type workerMetadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	}
	matches := 0
	var identity workerMetadata
	for _, raw := range list.Items {
		var account struct {
			Metadata workerMetadata `json:"metadata"`
		}
		if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &account) != nil || account.Metadata.Name == "" {
			return "", fmt.Errorf("cannot read Orka ServiceAccounts in %s: malformed item", OrkaNamespace)
		}
		if account.Metadata.Name != worker {
			continue
		}
		matches++
		identity = account.Metadata
	}
	if matches > 1 {
		return "", fmt.Errorf("multiple Orka ServiceAccounts named %s in %s; refusing ambiguous worker identity", worker, OrkaNamespace)
	}
	if matches == 0 {
		return "", fmt.Errorf("Orka worker ServiceAccount %s not found in %s", worker, OrkaNamespace)
	}
	switch {
	case identity.Namespace != OrkaNamespace:
		return "", fmt.Errorf("Orka worker ServiceAccount %s has wrong namespace", worker)
	case identity.Labels["orka.ai/worker"] != "true":
		return "", fmt.Errorf("Orka worker ServiceAccount %s lacks worker=true label", worker)
	case identity.Labels["orka.ai/worker-trust"] != "ai":
		return "", fmt.Errorf("Orka worker ServiceAccount %s lacks worker-trust=ai label", worker)
	case identity.Labels["helm.sh/chart"] != labels["helm.sh/chart"]:
		return "", fmt.Errorf("Orka worker ServiceAccount %s has wrong chart label", worker)
	case identity.Labels["app.kubernetes.io/instance"] != labels["app.kubernetes.io/instance"]:
		return "", fmt.Errorf("Orka worker ServiceAccount %s has wrong instance label", worker)
	case identity.Labels["app.kubernetes.io/name"] != labels["app.kubernetes.io/name"]:
		return "", fmt.Errorf("Orka worker ServiceAccount %s has wrong app name label", worker)
	case identity.Labels["app.kubernetes.io/managed-by"] != labels["app.kubernetes.io/managed-by"]:
		return "", fmt.Errorf("Orka worker ServiceAccount %s has wrong managed-by label", worker)
	}
	return worker, nil
}

const quickstartWorkerPlaceholder = "kmx-ai-worker-service-account-placeholder"

func renderQuickstartK8sTool(resources []byte, worker string) ([]byte, error) {
	if err := scaffold.ValidateObjectName(worker); err != nil || worker == quickstartWorkerPlaceholder {
		return nil, fmt.Errorf("invalid Orka AI worker ServiceAccount name")
	}
	placeholder := []byte(quickstartWorkerPlaceholder)
	if bytes.Count(resources, placeholder) != 1 {
		return nil, fmt.Errorf("Orka Kubernetes Tool manifest must have exactly one worker ServiceAccount placeholder")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(resources))
	bindings := 0
	for {
		var document map[string]any
		if err := decoder.Decode(&document); err == io.EOF {
			break
		} else if err != nil || document == nil {
			return nil, fmt.Errorf("Orka Kubernetes Tool manifest has invalid YAML")
		}
		metadata, _ := document["metadata"].(map[string]any)
		if document["kind"] != "RoleBinding" || metadata["name"] != "kmx-k8s-tool-policy-reader" {
			continue
		}
		bindings++
		subjects, _ := document["subjects"].([]any)
		if len(subjects) != 1 {
			return nil, fmt.Errorf("Orka Kubernetes Tool RoleBinding must have one worker subject")
		}
		subject, _ := subjects[0].(map[string]any)
		if subject["kind"] != "ServiceAccount" || subject["name"] != quickstartWorkerPlaceholder || subject["namespace"] != OrkaNamespace {
			return nil, fmt.Errorf("Orka Kubernetes Tool RoleBinding has no valid worker placeholder subject")
		}
	}
	if bindings != 1 {
		return nil, fmt.Errorf("Orka Kubernetes Tool manifest must have exactly one policy reader RoleBinding")
	}
	return bytes.Replace(resources, placeholder, []byte(worker), 1), nil
}

// policyPermissionDenied is emitted when SAR does not allow the named permission.
// Tool is set by the lift preflight, which knows the referring Tool identity.
type policyPermissionDenied struct {
	Tool, Worker, Namespace, Policy string
}

func (e *policyPermissionDenied) Error() string {
	return fmt.Sprintf("denied ServiceAccount %s/%s get outboundaccesspolicies.core.orka.ai/%s in namespace %s", e.Namespace, e.Worker, e.Policy, e.Namespace)
}

// Check effective access for the one named policy, not a broader list grant.
// The Agent's worker and its referenced Tool policy share namespace here.
// SAR responses and kubectl stderr can contain sensitive admission details;
// only the requested identity and permission are included in errors.
func (a *App) orkaWorkerCanGetPolicy(ctx context.Context, worker, namespace, policy string) error {
	permission := fmt.Sprintf("ServiceAccount %s/%s get outboundaccesspolicies.core.orka.ai/%s in namespace %s", namespace, worker, policy, namespace)
	if scaffold.ValidateObjectName(worker) != nil || scaffold.ValidateNamespace(namespace) != nil || scaffold.ValidateObjectName(policy) != nil {
		return fmt.Errorf("invalid worker or named policy permission: %s", permission)
	}
	request, err := json.Marshal(map[string]any{
		"apiVersion": "authorization.k8s.io/v1", "kind": "SubjectAccessReview",
		"spec": map[string]any{
			"user":   "system:serviceaccount:" + namespace + ":" + worker,
			"groups": []string{"system:serviceaccounts", "system:serviceaccounts:" + namespace, "system:authenticated"},
			"resourceAttributes": map[string]string{
				"namespace": namespace, "group": "core.orka.ai", "resource": "outboundaccesspolicies",
				"name": policy, "verb": "get",
			},
		},
	})
	if err != nil {
		return fmt.Errorf("cannot prepare named policy permission: %s", permission)
	}
	response, err := a.orkaCapture(ctx, request, "create", "--raw", "/apis/authorization.k8s.io/v1/subjectaccessreviews", "-f", "-")
	if err != nil {
		return fmt.Errorf("cannot evaluate %s: %w", permission, err)
	}
	var review struct {
		Status *struct {
			Allowed         *bool  `json:"allowed"`
			Denied          bool   `json:"denied"`
			EvaluationError string `json:"evaluationError"`
		} `json:"status"`
	}
	if json.Unmarshal(response, &review) != nil || review.Status == nil {
		return fmt.Errorf("invalid authorization review for %s", permission)
	}
	if review.Status.EvaluationError != "" {
		return fmt.Errorf("indeterminate authorization review for %s", permission)
	}
	if review.Status.Allowed == nil {
		return fmt.Errorf("invalid authorization review for %s", permission)
	}
	if *review.Status.Allowed && review.Status.Denied {
		return fmt.Errorf("indeterminate authorization review for %s", permission)
	}
	if !*review.Status.Allowed {
		return &policyPermissionDenied{Worker: worker, Namespace: namespace, Policy: policy}
	}
	return nil
}

func quickstartAgentTools(opt *CreateOptions) {
	if strings.TrimSpace(opt.Tools) == "" {
		opt.Tools = quickstartK8sTool
	}
}

func (a *App) installQuickstartK8sTool() error {
	worker, err := a.orkaAIWorkerAccount(a.operationContext())
	if err != nil {
		return fmt.Errorf("cannot install Orka Kubernetes Tool without a verified AI worker: %w", err)
	}
	script, err := kaimahi.Manifests.ReadFile("scripts/orka-k8s-tool.py")
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]string{"name": "kmx-k8s-tool", "namespace": OrkaNamespace},
		"data":     map[string]string{"server.py": string(script)},
	})
	if err != nil {
		return err
	}
	resources, err := manifest("orka-k8s-tool.yaml")
	if err != nil {
		return err
	}
	resources, err = renderQuickstartK8sTool(resources, worker)
	if err != nil {
		return err
	}
	if err := a.applyBytes("Orka Kubernetes tool server", body); err != nil {
		return err
	}
	resources = []byte(strings.Replace(string(resources), "kmx-tool-code-checksum", fmt.Sprintf("%x", sha256.Sum256(script)), 1))
	if err := a.applyBytes("Orka Kubernetes tool resources", resources); err != nil {
		return err
	}
	if err := a.kubectlRun("-n", OrkaNamespace, "rollout", "status", "deploy/kmx-k8s-tool", "--timeout=180s"); err != nil {
		return err
	}
	if err := a.waitOrkaResourceCondition("outboundaccesspolicies.core.orka.ai",
		quickstartK8sToolPolicy, "Accepted"); err != nil {
		return err
	}
	if err := a.orkaWorkerCanGetPolicy(a.operationContext(), worker, OrkaNamespace, quickstartK8sToolPolicy); err != nil {
		return fmt.Errorf("Orka Kubernetes Tool policy reader is not authorized: %w", err)
	}
	return a.waitOrkaResourceCondition("tools.core.orka.ai", quickstartK8sTool, "Available")
}

func (a *App) waitOrkaResourceCondition(resource, name, condition string) error {
	ctx, cancel := context.WithTimeout(a.operationContext(), time.Minute)
	defer cancel()
	for {
		raw, err := a.orkaCapture(ctx, nil, "-n", OrkaNamespace, "get", resource, name, "-o", "json")
		if err != nil {
			return fmt.Errorf("read %s/%s status: %w", resource, name, err)
		}
		var object struct {
			Metadata struct {
				Generation int64 `json:"generation"`
			} `json:"metadata"`
			Status struct {
				Conditions []serverCondition `json:"conditions"`
			} `json:"status"`
		}
		if err := json.Unmarshal(raw, &object); err != nil {
			return fmt.Errorf("read %s/%s status: invalid JSON: %w", resource, name, err)
		}
		if object.Metadata.Generation < 1 {
			return fmt.Errorf("%s/%s has no generation", resource, name)
		}
		for _, current := range object.Status.Conditions {
			if current.Type == condition && current.Status == "True" &&
				current.ObservedGeneration == object.Metadata.Generation {
				return nil
			}
		}
		if err := a.pause(ctx, time.Second); err != nil {
			return fmt.Errorf("waiting for %s/%s current-generation %s: %w",
				resource, name, condition, err)
		}
	}
}

// Preserve existing references and instructions. A resourceVersion test prevents
// overwriting concurrent edits when upgrading a pre-tool quickstart agent.
func (a *App) attachQuickstartK8sTool(agent, namespace string) error {
	ctx, cancel := context.WithTimeout(a.operationContext(), time.Minute)
	defer cancel()
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", "agents.core.orka.ai", agent, "-o", "json")
	if err != nil {
		return err
	}
	patch, err := quickstartK8sToolPatch(raw)
	if err != nil || patch == nil {
		return err
	}
	raw, err = a.orkaCapture(ctx, nil, "-n", namespace, "patch", "agents.core.orka.ai", agent, "--type=json", "-p", string(patch), "-o", "json")
	if err != nil {
		return err
	}
	var object orkaObject
	if err := json.Unmarshal(raw, &object); err != nil || object.Metadata.UID == "" || object.Metadata.Generation < 1 {
		return fmt.Errorf("updated Agent returned no valid identity")
	}
	return a.waitOrkaReady(ctx, namespace, orkaIdentity{Kind: "Agent", Name: agent, UID: object.Metadata.UID, Generation: object.Metadata.Generation})
}

func quickstartK8sToolPatch(raw []byte) ([]byte, error) {
	var agent struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Spec struct {
			Tools []map[string]any `json:"tools"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &agent); err != nil {
		return nil, err
	}
	if agent.Metadata.ResourceVersion == "" {
		return nil, fmt.Errorf("Agent has no resourceVersion; cannot attach Kubernetes tool")
	}
	for _, tool := range agent.Spec.Tools {
		if tool["name"] == quickstartK8sTool {
			return nil, nil // Also respect an explicitly disabled reference.
		}
	}
	tools := append(agent.Spec.Tools, map[string]any{"name": quickstartK8sTool})
	return json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": agent.Metadata.ResourceVersion},
		{"op": "add", "path": "/spec/tools", "value": tools},
	})
}
