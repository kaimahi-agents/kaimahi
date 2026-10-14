package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const (
	kagentControllerImageRepo = "ghcr.io/kagent-dev/kagent/controller"
	kagentGoImageRepo         = "ghcr.io/kagent-dev/kagent/golang-adk"
)

const (
	kagentAgentSchemaSHA256 = "b6048ddf43a7fff6ea11bb168b09d737c78ee6e36afd57df2f3a050f480e80c6"
	kagentModelSchemaSHA256 = "1742024902c0987df9f9131ece110aa2746e9a5a91055d96f91ba3e5b2ca48a3"
)

var (
	kagentReadinessTimeout = 10 * time.Minute
)

type kagentControllerService struct {
	Name, Namespace, UID, ResourceVersion string
	Selector                              map[string]string
	DeploymentName                        string
	DeploymentUID                         string
	DeploymentResourceVersion             string
	DeploymentGeneration                  int64
	ControllerImages                      []kagentEvaluationImage
	ControllerServiceAccount              string
	ConfigMapName                         string
	ConfigMapUID                          string
	ConfigMapResourceVersion              string
	Runtime, AuthMode, TargetNamespace    string
}

type kagentMetadata struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resourceVersion"`
	Generation      int64             `json:"generation"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
	OwnerReferences []struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		UID        string `json:"uid"`
		Controller *bool  `json:"controller"`
	} `json:"ownerReferences"`
	DeletionTimestamp *time.Time `json:"deletionTimestamp"`
}

type kagentContainer struct {
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	Command []string `json:"command"`
	Args    []string `json:"args"`
	Env     []struct {
		Name      string          `json:"name"`
		Value     string          `json:"value"`
		ValueFrom json.RawMessage `json:"valueFrom"`
	} `json:"env"`
	EnvFrom []struct {
		Prefix       string `json:"prefix"`
		ConfigMapRef *struct {
			Name     string `json:"name"`
			Optional *bool  `json:"optional"`
		} `json:"configMapRef"`
		SecretRef json.RawMessage `json:"secretRef"`
	} `json:"envFrom"`
}

type kagentDeployment struct {
	APIVersion string          `json:"apiVersion"`
	SpecRaw    json.RawMessage `json:"-"`
	Kind       string          `json:"kind"`
	Metadata   kagentMetadata  `json:"metadata"`
	Spec       struct {
		Replicas *int32 `json:"replicas"`
		Selector struct {
			MatchLabels      map[string]string `json:"matchLabels"`
			MatchExpressions []json.RawMessage `json:"matchExpressions"`
		} `json:"selector"`
		Template struct {
			Metadata kagentMetadata `json:"metadata"`
			Spec     struct {
				ServiceAccountName string            `json:"serviceAccountName"`
				Containers         []kagentContainer `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration  int64 `json:"observedGeneration"`
		Replicas            int32 `json:"replicas"`
		UpdatedReplicas     int32 `json:"updatedReplicas"`
		ReadyReplicas       int32 `json:"readyReplicas"`
		AvailableReplicas   int32 `json:"availableReplicas"`
		UnavailableReplicas int32 `json:"unavailableReplicas"`
	} `json:"status"`
}

// Retain the complete admitted spec, including fields not needed by the image
// verifier, so a stable UID cannot hide pod-template or security drift.
func (d *kagentDeployment) UnmarshalJSON(raw []byte) error {
	type plain kagentDeployment
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	var spec struct {
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return err
	}
	*d = kagentDeployment(decoded)
	d.SpecRaw = spec.Spec
	return nil
}

type kagentWorkloadIdentity struct {
	DeploymentUID, DeploymentResourceVersion string
	DeploymentGeneration                     int64
	DeploymentSpecDigest                     string
	ServiceUID, ServiceResourceVersion       string
}

// kagentCapture is the only Kagent kubectl boundary. It pins context, bounds
// process and response size, accepts stdin for strict creates/raw A2A, and
// never propagates kubectl stderr because admission and transport errors may
// echo a prompt or manifest field.
func (a *App) kagentCapture(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	raw, err := a.kagentCaptureWithTimeout(ctx, 15*time.Second, "10s", stdin, args...)
	if err != nil {
		return nil, kagentFailure("unreadable", err)
	}
	return raw, nil
}

func (a *App) kagentCaptureWithTimeout(ctx context.Context, timeout time.Duration, requestTimeout string, stdin []byte, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	prepared := a.Command(append([]string{"--request-timeout=" + requestTimeout}, args...)...)
	cmd := exec.CommandContext(callCtx, prepared.Path, prepared.Args[1:]...)
	cmd.Env = prepared.Env
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(stdin)
	out := &orkaBoundedBuffer{remaining: 4 << 20}
	stderr := &orkaBoundedBuffer{remaining: 4 << 10}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := a.executeCapture(callCtx, cmd); err != nil {
		if callCtx.Err() != nil {
			return nil, fmt.Errorf("kubectl request cancelled or timed out")
		}
		reason := strings.ToLower(stderr.buffer.String())
		switch {
		case strings.Contains(reason, "notfound"), strings.Contains(reason, "not found"):
			return nil, fmt.Errorf("kubectl request failed: requested Kubernetes resource not found")
		case strings.Contains(reason, "forbidden"), strings.Contains(reason, "unauthorized"):
			return nil, fmt.Errorf("kubectl request failed: access forbidden")
		default:
			return nil, fmt.Errorf("kubectl request failed; check permissions, prerequisites and the selected context")
		}
	}
	return append([]byte(nil), out.buffer.Bytes()...), nil
}

func (a *App) guardKagentOperation(ctx context.Context, namespace, action, command string) error {
	if a.guarded {
		return nil
	}
	raw, err := a.kagentCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot read context metadata for the Kagent mutation guard: %w", err)
	}
	cfg, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return fmt.Errorf("cannot decode context metadata for the Kagent mutation guard")
	}
	if err := guard.CheckContext(ctx, cfg, guard.Request{
		Action: action, Context: a.Cfg.KubeContext, Source: a.Cfg.ContextSource,
		Namespaces: namespace, Confirm: a.Cfg.Confirm, Command: command,
	}, a.Err, a.Stdin); err != nil {
		return err
	}
	a.guarded = true
	return nil
}

func (a *App) preflightKagentInstallation(ctx context.Context, runtimeName, targetNamespace string) (kagentControllerService, error) {
	service, err := a.inspectKagentInstallation(ctx, runtimeName, targetNamespace)
	if err != nil {
		return kagentControllerService{}, fmt.Errorf("%w; no Kubernetes resources were changed", err)
	}
	if err := a.verifyKagentControllerRBAC(ctx, service, targetNamespace); err != nil {
		return kagentControllerService{}, fmt.Errorf("%w; no Kubernetes resources were changed", err)
	}
	return service, nil
}

func (a *App) inspectKagentInstallation(ctx context.Context, runtimeName, targetNamespace string) (kagentControllerService, error) {
	raw, err := a.kagentCapture(ctx, nil, "get", "services", "--all-namespaces", "-l", "app.kubernetes.io/part-of=kagent,app.kubernetes.io/component=controller", "-o", "json")
	if err != nil {
		return kagentControllerService{}, fmt.Errorf("cannot detect the Kagent controller Service: %w", err)
	}
	var list struct {
		Items []struct {
			Metadata kagentMetadata `json:"metadata"`
			Spec     struct {
				Ports []struct {
					Name, Protocol string
					Port           int32 `json:"port"`
				} `json:"ports"`
				Selector map[string]string `json:"selector"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Service discovery returned invalid JSON")
	}
	var candidates []int
	for i, item := range list.Items {
		if slices.ContainsFunc(item.Spec.Ports, func(port struct {
			Name, Protocol string
			Port           int32 `json:"port"`
		}) bool {
			return port.Name == "controller" && port.Protocol == "TCP" && port.Port == 8083
		}) {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) != 1 {
		return kagentControllerService{}, fmt.Errorf("expected exactly one labeled Kagent Service with named controller TCP port 8083, found %d", len(candidates))
	}
	item := list.Items[candidates[0]]
	if item.Metadata.Name == "" || item.Metadata.Namespace == "" ||
		item.Metadata.UID == "" || item.Metadata.ResourceVersion == "" || item.Metadata.DeletionTimestamp != nil ||
		item.Metadata.Labels["app.kubernetes.io/part-of"] != "kagent" ||
		item.Metadata.Labels["app.kubernetes.io/component"] != "controller" {
		return kagentControllerService{}, fmt.Errorf("the discovered Kagent controller Service has an invalid identity or labels")
	}
	if err := a.selectedKagentProfile().validateLabels(item.Metadata.Labels); err != nil {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Service labels: %w", err)
	}
	if len(item.Spec.Selector) == 0 {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Service has no selector")
	}
	service := kagentControllerService{
		Name: item.Metadata.Name, Namespace: item.Metadata.Namespace,
		UID: item.Metadata.UID, ResourceVersion: item.Metadata.ResourceVersion,
		Selector: copyKagentLabels(item.Spec.Selector), Runtime: runtimeName, TargetNamespace: targetNamespace,
	}
	deployment, err := a.readKagentControllerDeployment(ctx, service)
	if err != nil {
		return kagentControllerService{}, err
	}
	service.DeploymentName = deployment.Metadata.Name
	service.DeploymentUID = deployment.Metadata.UID
	service.DeploymentResourceVersion = deployment.Metadata.ResourceVersion
	service.DeploymentGeneration = deployment.Metadata.Generation
	service.ControllerServiceAccount = deployment.Spec.Template.Spec.ServiceAccountName
	if service.ControllerServiceAccount == "" {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Deployment has no explicit serviceAccountName")
	}
	controller, err := exactKagentContainer(deployment.Spec.Template.Spec.Containers, "controller")
	if err != nil {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Deployment: %w", err)
	}
	profile := a.selectedKagentProfile()
	if !profile.imageMatches(controller.Image, profile.ControllerImage, kagentControllerImageRepo, profile.ControllerDigests[:]) {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Deployment does not use official image %s", profile.ControllerImage)
	}
	if len(controller.Command) != 0 || len(controller.Args) != 0 {
		return kagentControllerService{}, fmt.Errorf("Kagent controller Deployment overrides the official image command or arguments")
	}
	service.AuthMode, err = kagentControllerAuthMode(controller)
	if err != nil {
		return kagentControllerService{}, err
	}
	configMapName, err := kagentControllerConfigMapName(controller)
	if err != nil {
		return kagentControllerService{}, err
	}
	if configMapName != service.Name {
		return kagentControllerService{}, fmt.Errorf("Kagent controller image ConfigMap is not bound to the selected controller Service and Deployment")
	}
	configMap, err := a.readKagentControllerConfigMap(ctx, service.Namespace, configMapName, runtimeName)
	if err != nil {
		return kagentControllerService{}, err
	}
	service.ConfigMapName = configMap.Metadata.Name
	service.ConfigMapUID = configMap.Metadata.UID
	service.ConfigMapResourceVersion = configMap.Metadata.ResourceVersion
	if err := validateKagentWatchNamespaces(configMap.Data["WATCH_NAMESPACES"], targetNamespace); err != nil {
		return kagentControllerService{}, err
	}
	if err := a.observeKagentDeploymentPods(ctx, service.Namespace, deployment, "controller", controller.Image, kagentControllerImageRepo,
		profile.ControllerDigests[:], &service.ControllerImages); err != nil {
		return kagentControllerService{}, fmt.Errorf("Kagent controller workload: %w", err)
	}
	versionPath := kagentServiceProxyPath(service, "/version")
	raw, err = a.kagentCapture(ctx, nil, "get", "--raw", versionPath)
	if err != nil {
		return kagentControllerService{}, fmt.Errorf("cannot verify the live Kagent controller version: %w", err)
	}
	var version struct {
		KagentVersion string `json:"kagent_version"`
		GitCommit     string `json:"git_commit"`
	}
	if err := json.Unmarshal(raw, &version); err != nil {
		return kagentControllerService{}, fmt.Errorf("Kagent controller /version returned invalid JSON")
	}
	if !profile.validVersion(version.KagentVersion, version.GitCommit) {
		return kagentControllerService{}, fmt.Errorf("live Kagent controller is not exact %s commit %s", profile.Version, profile.Commit)
	}
	for _, identity := range []kagentCRDIdentity{
		{Name: "modelconfigs.kagent.dev", Kind: "ModelConfig", Plural: "modelconfigs", SchemaSHA256: profile.ModelSchema},
		{Name: "agents.kagent.dev", Kind: "Agent", Plural: "agents", SchemaSHA256: profile.AgentSchema},
	} {
		raw, err := a.kagentCapture(ctx, nil, "get", "crd", identity.Name, "-o", "json")
		if err != nil {
			return kagentControllerService{}, fmt.Errorf("cannot read installed %s CRD: %w", identity.Name, err)
		}
		if err := validateKagentCRD(raw, identity); err != nil {
			return kagentControllerService{}, fmt.Errorf("installed %s CRD is incompatible with exact Kagent %s: %w", identity.Name, profile.Version, err)
		}
	}
	return service, nil
}

func validateKagentWatchNamespaces(value, targetNamespace string) error {
	if value == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, namespace := range strings.Split(value, ",") {
		if namespace == "" || namespace != strings.TrimSpace(namespace) || scaffold.ValidateNamespace(namespace) != nil || seen[namespace] {
			return fmt.Errorf("Kagent controller WATCH_NAMESPACES is not an exact comma-separated namespace list")
		}
		seen[namespace] = true
	}
	if !seen[targetNamespace] {
		return fmt.Errorf("Kagent controller does not watch target namespace %s", targetNamespace)
	}
	return nil
}

func (a *App) verifyKagentControllerRBAC(ctx context.Context, service kagentControllerService, targetNamespace string) error {
	as := "system:serviceaccount:" + service.Namespace + ":" + service.ControllerServiceAccount
	checks := []struct {
		resource string
		verbs    []string
	}{
		{"agents.kagent.dev", []string{"get", "list", "watch"}},
		{"agents.kagent.dev/status", []string{"get", "update", "patch"}},
		{"modelconfigs.kagent.dev", []string{"get", "list", "watch"}},
		{"modelconfigs.kagent.dev/status", []string{"get", "update", "patch"}},
		{"remotemcpservers.kagent.dev", []string{"get", "list", "watch"}},
		{"secrets", []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{"configmaps", []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{"serviceaccounts", []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{"deployments.apps", []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{"services", []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
	}
	for _, check := range checks {
		for _, verb := range check.verbs {
			raw, err := a.kagentCapture(ctx, nil, "auth", "can-i", verb, check.resource, "--as="+as, "-n", targetNamespace)
			if err != nil {
				return fmt.Errorf("cannot prove Kagent controller RBAC for %s %s in namespace %s: %w", verb, check.resource, targetNamespace, err)
			}
			if string(bytes.TrimSpace(raw)) != "yes" {
				return fmt.Errorf("Kagent controller ServiceAccount %s is not allowed to %s %s in target namespace %s", service.ControllerServiceAccount, verb, check.resource, targetNamespace)
			}
		}
	}
	return nil
}

func copyKagentLabels(source map[string]string) map[string]string {
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func equalKagentLabels(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func kagentSelectorArgument(selector map[string]string) string {
	keys := make([]string, 0, len(selector))
	for key := range selector {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+selector[key])
	}
	return strings.Join(parts, ",")
}

func kagentLabelsContain(labels, selector map[string]string) bool {
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func (a *App) readKagentControllerDeployment(ctx context.Context, service kagentControllerService) (kagentDeployment, error) {
	raw, err := a.kagentCapture(ctx, nil, "-n", service.Namespace, "get", "deployments", "-l", kagentSelectorArgument(service.Selector), "-o", "json")
	if err != nil {
		return kagentDeployment{}, fmt.Errorf("cannot bind the Kagent controller Service to its Deployment: %w", err)
	}
	var list struct {
		Items []kagentDeployment `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil || len(list.Items) != 1 {
		return kagentDeployment{}, fmt.Errorf("expected exactly one Kagent controller Deployment selected by the controller Service, found %d", len(list.Items))
	}
	deployment := list.Items[0]
	if deployment.Kind != "Deployment" || deployment.Metadata.Name == "" || deployment.Metadata.Name != service.Name || deployment.Metadata.Namespace != service.Namespace ||
		deployment.Metadata.UID == "" || deployment.Metadata.ResourceVersion == "" || deployment.Metadata.Generation < 1 ||
		deployment.Metadata.DeletionTimestamp != nil || !kagentLabelsContain(deployment.Metadata.Labels, service.Selector) ||
		!kagentLabelsContain(deployment.Spec.Template.Metadata.Labels, service.Selector) ||
		len(deployment.Spec.Selector.MatchExpressions) != 0 || !equalKagentLabels(deployment.Spec.Selector.MatchLabels, service.Selector) ||
		deployment.Metadata.Labels["app.kubernetes.io/component"] != "controller" {
		return kagentDeployment{}, fmt.Errorf("the selected Kagent controller Deployment has an invalid identity, selector, or labels")
	}
	if err := a.selectedKagentProfile().validateLabels(deployment.Metadata.Labels); err != nil {
		return kagentDeployment{}, fmt.Errorf("Kagent controller Deployment labels: %w", err)
	}
	return deployment, nil
}

func exactKagentContainer(containers []kagentContainer, name string) (kagentContainer, error) {
	var found []kagentContainer
	for _, container := range containers {
		if container.Name == name {
			found = append(found, container)
		}
	}
	if len(found) != 1 {
		return kagentContainer{}, fmt.Errorf("expected exactly one %s container, found %d", name, len(found))
	}
	return found[0], nil
}

func kagentControllerAuthMode(controller kagentContainer) (string, error) {
	var values []string
	for _, env := range controller.Env {
		if env.Name == "" {
			return "", fmt.Errorf("Kagent controller has an invalid unnamed environment entry")
		}
		if env.Name == "AUTH_MODE" {
			if len(bytes.TrimSpace(env.ValueFrom)) != 0 && string(bytes.TrimSpace(env.ValueFrom)) != "null" {
				return "", fmt.Errorf("Kagent controller AUTH_MODE must be a literal value")
			}
			values = append(values, env.Value)
		}
	}
	if len(values) != 1 || values[0] != "unsecure" && values[0] != "trusted-proxy" {
		return "", fmt.Errorf("Kagent controller AUTH_MODE must be exactly one literal unsecure or trusted-proxy value")
	}
	return values[0], nil
}

func kagentControllerConfigMapName(controller kagentContainer) (string, error) {
	if len(controller.EnvFrom) != 1 {
		return "", fmt.Errorf("Kagent controller must have exactly one ConfigMap envFrom source, found %d sources", len(controller.EnvFrom))
	}
	var names []string
	for _, source := range controller.EnvFrom {
		if len(bytes.TrimSpace(source.SecretRef)) != 0 && string(bytes.TrimSpace(source.SecretRef)) != "null" {
			return "", fmt.Errorf("Kagent controller image envFrom must not include a Secret source")
		}
		if source.ConfigMapRef != nil {
			if source.Prefix != "" || source.ConfigMapRef.Name == "" || source.ConfigMapRef.Optional != nil && *source.ConfigMapRef.Optional {
				return "", fmt.Errorf("Kagent controller image ConfigMap envFrom reference is not exact")
			}
			names = append(names, source.ConfigMapRef.Name)
		}
	}
	if len(names) != 1 {
		return "", fmt.Errorf("Kagent controller must have exactly one ConfigMap envFrom source, found %d", len(names))
	}
	for _, env := range controller.Env {
		if strings.HasPrefix(env.Name, "IMAGE_") || strings.HasPrefix(env.Name, "GO_IMAGE_") {
			return "", fmt.Errorf("Kagent controller image config must come only from its ConfigMap envFrom source")
		}
	}
	return names[0], nil
}

type kagentConfigMap struct {
	Kind     string            `json:"kind"`
	Metadata kagentMetadata    `json:"metadata"`
	Data     map[string]string `json:"data"`
}

func (a *App) readKagentControllerConfigMap(ctx context.Context, namespace, name, runtimeName string) (kagentConfigMap, error) {
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", "configmap", name, "-o", "json")
	if err != nil {
		return kagentConfigMap{}, fmt.Errorf("cannot read Kagent controller image ConfigMap: %w", err)
	}
	var configMap kagentConfigMap
	if json.Unmarshal(raw, &configMap) != nil || configMap.Kind != "ConfigMap" || configMap.Metadata.Name != name ||
		configMap.Metadata.Namespace != namespace || configMap.Metadata.UID == "" || configMap.Metadata.ResourceVersion == "" ||
		configMap.Metadata.DeletionTimestamp != nil {
		return kagentConfigMap{}, fmt.Errorf("Kagent controller image ConfigMap returned an invalid identity")
	}
	if err := a.selectedKagentProfile().validateLabels(configMap.Metadata.Labels); err != nil {
		return kagentConfigMap{}, fmt.Errorf("Kagent controller image ConfigMap labels: %w", err)
	}
	prefix, repository := "IMAGE_", "kagent-dev/kagent/app"
	if runtimeName == "go" {
		prefix, repository = "GO_IMAGE_", "kagent-dev/kagent/golang-adk"
	} else if runtimeName != "python" {
		return kagentConfigMap{}, fmt.Errorf("unsupported Kagent declarative runtime")
	}
	if configMap.Data[prefix+"REGISTRY"] != "ghcr.io" || configMap.Data[prefix+"REPOSITORY"] != repository || !a.selectedKagentProfile().runtimeTagMatches(runtimeName, configMap.Data[prefix+"TAG"]) {
		return kagentConfigMap{}, fmt.Errorf("Kagent controller %s image config does not exactly select ghcr.io/%s:%s", runtimeName, repository, a.selectedKagentProfile().tag())
	}
	return configMap, nil
}

func (a *App) observeKagentDeploymentPods(ctx context.Context, namespace string, deployment kagentDeployment, containerName, image, imageRepo string, digests []string, observed *[]kagentEvaluationImage) error {
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if desired < 1 || deployment.Status.ObservedGeneration != deployment.Metadata.Generation ||
		deployment.Status.Replicas != desired || deployment.Status.UpdatedReplicas != desired || deployment.Status.ReadyReplicas != desired ||
		deployment.Status.AvailableReplicas != desired || deployment.Status.UnavailableReplicas != 0 {
		return kagentFailure("failed", fmt.Errorf("Deployment is not a complete current-generation rollout"))
	}
	selector := deployment.Spec.Selector.MatchLabels
	if len(selector) == 0 || len(deployment.Spec.Selector.MatchExpressions) != 0 || !kagentLabelsContain(deployment.Spec.Template.Metadata.Labels, selector) {
		return kagentFailure("drift", fmt.Errorf("Deployment has no exact matchLabels pod selector"))
	}
	var currentRS *kagentDeployment
	if a.selectedKagentProfile().Version == "v0.10.3" {
		var err error
		currentRS, err = a.currentKagentReplicaSet(ctx, namespace, deployment)
		if err != nil {
			return err
		}
	}
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", "pods", "-l", kagentSelectorArgument(selector), "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot read Deployment pods: %w", err)
	}
	var list struct {
		Items []struct {
			Metadata kagentMetadata `json:"metadata"`
			Spec     struct {
				NodeName   string            `json:"nodeName"`
				Containers []kagentContainer `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase      string `json:"phase"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
				ContainerStatuses []struct {
					Name    string `json:"name"`
					Image   string `json:"image"`
					ImageID string `json:"imageID"`
					Ready   bool   `json:"ready"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return kagentFailure("unreadable", fmt.Errorf("Deployment pod list returned invalid JSON"))
	}
	if a.selectedKagentProfile().Version == "v0.10.3" && list.Items == nil {
		return kagentFailure("unreadable", fmt.Errorf("Deployment pod list is missing a non-null items array"))
	}
	readyPods := int32(0)
	seen := map[string]bool{}
	for _, pod := range list.Items {
		if pod.Metadata.Name == "" || pod.Metadata.UID == "" || pod.Metadata.ResourceVersion == "" ||
			pod.Metadata.Namespace != namespace || !kagentLabelsContain(pod.Metadata.Labels, selector) {
			return kagentFailure("unreadable", fmt.Errorf("Deployment selected a pod with invalid identity"))
		}
		if currentRS != nil && (seen[pod.Metadata.UID] || !kagentWorkloadControllerOwner(pod.Metadata, "ReplicaSet", currentRS.Metadata.Name, currentRS.Metadata.UID)) {
			return kagentFailure("drift", fmt.Errorf("Deployment selected a pod without its current ReplicaSet controller owner"))
		}
		seen[pod.Metadata.UID] = true
		if pod.Metadata.DeletionTimestamp != nil || pod.Spec.NodeName == "" || pod.Status.Phase != "Running" {
			return kagentFailure("failed", fmt.Errorf("Deployment selected a terminating, unscheduled or non-running pod"))
		}
		podReady := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				podReady = true
			}
		}
		container, err := exactKagentContainer(pod.Spec.Containers, containerName)
		if err != nil || container.Image != image {
			return kagentFailure("drift", fmt.Errorf("pod does not have exactly one %s container using %s", containerName, image))
		}
		var statuses []struct {
			Name    string `json:"name"`
			Image   string `json:"image"`
			ImageID string `json:"imageID"`
			Ready   bool   `json:"ready"`
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == containerName {
				statuses = append(statuses, status)
			}
		}
		profile := a.selectedKagentProfile()
		if !podReady || len(statuses) == 1 && !statuses[0].Ready {
			return kagentFailure("failed", fmt.Errorf("Deployment pod is not Ready"))
		}
		if len(statuses) != 1 || (!profile.StrictPlatform && statuses[0].Image != image) ||
			!validKagentImageID(statuses[0].ImageID, imageRepo, digests) {
			return kagentFailure("drift", fmt.Errorf("ready pod %s container image identity does not match the published %s digest", containerName, a.selectedKagentProfile().Version))
		}
		if a.selectedKagentProfile().StrictPlatform {
			var platform kagentEvaluationImage
			if err := a.observeKagentPodPlatform(ctx, pod.Spec.NodeName, image, statuses[0].Image, statuses[0].ImageID, imageRepo, digests, &platform); err != nil {
				return err
			}
			if observed != nil {
				*observed = append(*observed, platform)
			}
		}
		readyPods++
	}
	if readyPods != desired {
		return kagentFailure("failed", fmt.Errorf("Deployment has %d proven ready pods, want %d", readyPods, desired))
	}
	return nil
}

func kagentWorkloadControllerOwner(metadata kagentMetadata, kind, name, uid string) bool {
	controllers, matched := 0, false
	for _, owner := range metadata.OwnerReferences {
		if owner.Controller != nil && *owner.Controller {
			controllers++
			matched = owner.APIVersion == "apps/v1" && owner.Kind == kind && owner.Name == name && owner.UID == uid
		}
	}
	return controllers == 1 && matched
}

func kagentDeploymentTemplateFingerprint(spec json.RawMessage) (string, error) {
	value, err := canonicalKagentJSONValue(spec)
	if err != nil {
		return "", err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", fmt.Errorf("missing Deployment spec object")
	}
	template, ok := object["template"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("missing pod template")
	}
	if meta, ok := template["metadata"].(map[string]any); ok {
		if labels, ok := meta["labels"].(map[string]any); ok {
			delete(labels, "pod-template-hash")
		}
	}
	return kagentSpecFingerprint(template)
}

// Labels alone cannot establish rollout provenance. Every selected pod must
// have the exact current ReplicaSet owner, itself owned by this Deployment UID.
func (a *App) currentKagentReplicaSet(ctx context.Context, namespace string, deployment kagentDeployment) (*kagentDeployment, error) {
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", "replicasets.apps", "-l", kagentSelectorArgument(deployment.Spec.Selector.MatchLabels), "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("cannot read Deployment ReplicaSets: %w", err)
	}
	var list struct {
		Items []kagentDeployment `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil || list.Items == nil {
		return nil, kagentFailure("unreadable", fmt.Errorf("Deployment ReplicaSet list is invalid"))
	}
	revision := deployment.Metadata.Annotations["deployment.kubernetes.io/revision"]
	if revision == "" {
		return nil, kagentFailure("drift", fmt.Errorf("Deployment has no current ReplicaSet revision"))
	}
	fingerprint, err := kagentDeploymentTemplateFingerprint(deployment.SpecRaw)
	if err != nil {
		return nil, kagentFailure("unreadable", fmt.Errorf("Deployment pod template is invalid"))
	}
	var current *kagentDeployment
	for i := range list.Items {
		rs := &list.Items[i]
		if rs.APIVersion != "apps/v1" || rs.Kind != "ReplicaSet" || rs.Metadata.Name == "" || rs.Metadata.Namespace != namespace || rs.Metadata.UID == "" || rs.Metadata.ResourceVersion == "" || rs.Metadata.Generation < 1 {
			return nil, kagentFailure("unreadable", fmt.Errorf("ReplicaSet returned incomplete identity"))
		}
		if !kagentWorkloadControllerOwner(rs.Metadata, "Deployment", deployment.Metadata.Name, deployment.Metadata.UID) || !kagentLabelsContain(rs.Metadata.Labels, deployment.Spec.Selector.MatchLabels) {
			return nil, kagentFailure("drift", fmt.Errorf("ReplicaSet does not retain the selected Deployment controller owner"))
		}
		if rs.Metadata.Annotations["deployment.kubernetes.io/revision"] != revision {
			continue
		}
		rsFingerprint, err := kagentDeploymentTemplateFingerprint(rs.SpecRaw)
		if err != nil {
			return nil, kagentFailure("unreadable", fmt.Errorf("ReplicaSet pod template is invalid"))
		}
		if current != nil || rsFingerprint != fingerprint || !kagentLabelsContain(rs.Spec.Selector.MatchLabels, deployment.Spec.Selector.MatchLabels) || len(rs.Spec.Selector.MatchExpressions) != 0 {
			return nil, kagentFailure("drift", fmt.Errorf("current ReplicaSet template or selector differs from Deployment, or revision is ambiguous"))
		}
		if rs.Metadata.DeletionTimestamp != nil || rs.Spec.Replicas == nil || deployment.Spec.Replicas == nil || *rs.Spec.Replicas != *deployment.Spec.Replicas || rs.Status.ObservedGeneration != rs.Metadata.Generation || rs.Status.Replicas != *rs.Spec.Replicas || rs.Status.ReadyReplicas != *rs.Spec.Replicas || rs.Status.AvailableReplicas != *rs.Spec.Replicas {
			return nil, kagentFailure("failed", fmt.Errorf("current ReplicaSet is not a complete current-generation rollout"))
		}
		current = rs
	}
	if current == nil {
		return nil, kagentFailure("drift", fmt.Errorf("Deployment has no proven current ReplicaSet"))
	}
	return current, nil
}

func validKagentImageID(imageID, repository string, digests []string) bool {
	if _, suffix, ok := strings.Cut(imageID, "://"); ok {
		imageID = suffix
	}
	if strings.HasPrefix(imageID, repository+"@") {
		imageID = strings.TrimPrefix(imageID, repository+"@")
	} else if strings.HasPrefix(imageID, "ghcr.io/"+repository+"@") {
		imageID = strings.TrimPrefix(imageID, "ghcr.io/"+repository+"@")
	} else if !strings.HasPrefix(imageID, "sha256:") {
		return false
	}
	parts := strings.Split(imageID, "@")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !strings.HasPrefix(part, "sha256:") || !slices.Contains(digests, strings.TrimPrefix(part, "sha256:")) {
			return false
		}
	}
	return true
}

type kagentCRDIdentity struct{ Name, Kind, Plural, SchemaSHA256 string }

func validateKagentCRD(raw []byte, want kagentCRDIdentity) error {
	var crd struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Group string `json:"group"`
			Scope string `json:"scope"`
			Names struct {
				Kind, Plural string
			} `json:"names"`
			Versions []struct {
				Name            string `json:"name"`
				Served, Storage bool
				Schema          struct {
					OpenAPIV3Schema json.RawMessage `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &crd); err != nil {
		return fmt.Errorf("invalid JSON")
	}
	if crd.Metadata.Name != want.Name || crd.Spec.Group != "kagent.dev" || crd.Spec.Scope != "Namespaced" ||
		crd.Spec.Names.Kind != want.Kind || crd.Spec.Names.Plural != want.Plural {
		return fmt.Errorf("group, kind, plural, scope, or metadata name differs")
	}
	var selected *json.RawMessage
	for i := range crd.Spec.Versions {
		version := &crd.Spec.Versions[i]
		if version.Name == "v1alpha2" {
			if selected != nil {
				return fmt.Errorf("v1alpha2 appears more than once")
			}
			if !version.Served || !version.Storage {
				return fmt.Errorf("v1alpha2 is not both served and storage")
			}
			selected = &version.Schema.OpenAPIV3Schema
			continue
		}
		if version.Storage {
			return fmt.Errorf("a version other than v1alpha2 is marked storage")
		}
	}
	if selected == nil {
		return fmt.Errorf("v1alpha2 is not both served and storage")
	}
	if len(*selected) == 0 {
		return fmt.Errorf("v1alpha2 has no openAPIV3Schema")
	}
	var schema any
	decoder := json.NewDecoder(bytes.NewReader(*selected))
	decoder.UseNumber()
	if decoder.Decode(&schema) != nil {
		return fmt.Errorf("v1alpha2 openAPIV3Schema is invalid JSON")
	}
	canonical, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("v1alpha2 openAPIV3Schema cannot be canonicalized")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(canonical))
	if digest != want.SchemaSHA256 {
		return fmt.Errorf("v1alpha2 openAPIV3Schema SHA-256 differs from the published v0.10.2 schema")
	}
	return nil
}

func kagentServiceProxyPath(service kagentControllerService, suffix string) string {
	return "/api/v1/namespaces/" + url.PathEscape(service.Namespace) + "/services/http:" +
		url.PathEscape(service.Name) + ":8083/proxy" + suffix
}

func kagentPlural(kind string) string {
	switch kind {
	case "Secret":
		return "secrets"
	case "ServiceAccount":
		return "serviceaccounts"
	case "Deployment":
		return "deployments.apps"
	case "Service":
		return "services"
	case "ModelConfig":
		return "modelconfigs.kagent.dev"
	case "Agent":
		return "agents.kagent.dev"
	default:
		return strings.ToLower(kind) + "s.kagent.dev"
	}
}

func (a *App) kagentGeneratedChildrenAbsent(ctx context.Context, namespace, name string) error {
	for _, kind := range []string{"Secret", "ServiceAccount", "Deployment", "Service"} {
		if err := a.kagentNamedResourceAbsent(ctx, namespace, kind, name); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) kagentNamedResourceAbsent(ctx context.Context, namespace, kind, name string) error {
	template := "go-template={{if .metadata.uid}}present{{else}}invalid{{end}}"
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", kagentPlural(kind), name, "--ignore-not-found=true", "-o", template)
	if err != nil {
		return fmt.Errorf("cannot establish %s/%s absence; no Kubernetes resources were changed: %w", kind, name, err)
	}
	switch string(bytes.TrimSpace(raw)) {
	case "":
		return nil
	case "present":
		return &kagentChildCollisionError{message: fmt.Sprintf("%s/%s already exists; Kagent Agent creation would overwrite this same-name generated child, so create is refused", kind, name)}
	default:
		return fmt.Errorf("cannot establish %s/%s absence because its sanitized read returned an invalid marker; no Kubernetes resources were changed", kind, name)
	}
}

func (a *App) kagentAbsent(ctx context.Context, namespace string, doc map[string]any) error {
	kind, name := doc["kind"].(string), kagentObjectName(doc)
	template := "go-template={{if and .metadata.uid .metadata.generation}}present{{else}}invalid{{end}}"
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", kagentPlural(kind), name, "--ignore-not-found=true", "-o", template)
	if err != nil {
		return fmt.Errorf("cannot establish %s/%s absence; no Kubernetes resources were changed: %w", kind, name, err)
	}
	switch string(bytes.TrimSpace(raw)) {
	case "":
		return nil
	case "present":
		return fmt.Errorf("%s/%s already exists; Kagent create is create-only and refuses overwrite, adoption, or reconciliation", kind, name)
	default:
		return fmt.Errorf("cannot establish %s/%s absence because its sanitized read returned an invalid marker; no Kubernetes resources were changed", kind, name)
	}
}

type kagentIdentity struct {
	Kind, Name, UID string
	Generation      int64
	Proof           kagentObjectProof
}

type kagentObjectProof struct {
	APIVersion, Kind, Name, Namespace string
	Spec                              any
	Markers                           map[string]string
	suite                             *suiteKagentOwnership
}

type kagentObject struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Metadata   kagentMetadata  `json:"metadata"`
	Spec       json.RawMessage `json:"spec"`
	Status     struct {
		ObservedGeneration int64             `json:"observedGeneration"`
		Conditions         []serverCondition `json:"conditions"`
		SecretHash         string            `json:"secretHash"`
	} `json:"status"`
}

func validateKagentAdmissionProof(raw []byte, desired map[string]any, proof kagentObjectProof) (kagentObjectProof, error) {
	var admitted struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name, Namespace, GenerateName string
			Annotations                   map[string]string `json:"annotations"`
			Labels                        map[string]string `json:"labels"`
			Finalizers                    []string          `json:"finalizers"`
			OwnerReferences               []json.RawMessage `json:"ownerReferences"`
			DeletionTimestamp             *time.Time        `json:"deletionTimestamp"`
		} `json:"metadata"`
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(raw, &admitted); err != nil {
		return proof, fmt.Errorf("server returned invalid JSON")
	}
	metadata := desired["metadata"].(map[string]any)
	wantAnnotations, err := kagentStringMap(metadata["annotations"])
	if err != nil {
		return proof, err
	}
	wantLabels, err := kagentStringMap(metadata["labels"])
	if err != nil {
		return proof, err
	}
	if admitted.APIVersion != proof.APIVersion || admitted.Kind != proof.Kind || admitted.Metadata.Name != proof.Name ||
		admitted.Metadata.Namespace != proof.Namespace || admitted.Metadata.GenerateName != "" || admitted.Metadata.DeletionTimestamp != nil ||
		len(admitted.Metadata.Finalizers) != 0 || len(admitted.Metadata.OwnerReferences) != 0 ||
		!reflect.DeepEqual(admitted.Metadata.Annotations, wantAnnotations) || !reflect.DeepEqual(admitted.Metadata.Labels, wantLabels) {
		return proof, fmt.Errorf("server returned a different identity or mutated reviewed metadata")
	}
	if err := validateKagentProofSpecAndMarkers(admitted.Spec, admitted.Metadata.Annotations, proof); err != nil {
		return proof, err
	}
	return proof, nil
}

func commonKagentObjectProof(desired map[string]any, namespace string) (kagentObjectProof, error) {
	proof := kagentObjectProof{
		APIVersion: fmt.Sprint(desired["apiVersion"]), Kind: fmt.Sprint(desired["kind"]),
		Name: kagentObjectName(desired), Namespace: namespace,
	}
	metadata, ok := desired["metadata"].(map[string]any)
	if !ok || metadata["namespace"] != namespace || proof.APIVersion != scaffold.KagentAPIVersion ||
		(proof.Kind != "ModelConfig" && proof.Kind != "Agent") || proof.Name == "" {
		return proof, fmt.Errorf("reviewed object has an invalid identity")
	}
	spec, ok := desired["spec"].(map[string]any)
	if !ok {
		return proof, fmt.Errorf("reviewed object has no spec")
	}
	normalized, err := cloneOrkaDoc(spec)
	if err != nil {
		return proof, err
	}
	if proof.Kind == "ModelConfig" && normalized["provider"] == "OpenAI" {
		openAI, ok := normalized["openAI"].(map[string]any)
		if !ok {
			return proof, fmt.Errorf("reviewed OpenAI ModelConfig has no openAI configuration")
		}
		if _, present := openAI["apiFormat"]; present {
			return proof, fmt.Errorf("reviewed OpenAI ModelConfig unexpectedly sets apiFormat")
		}
		openAI["apiFormat"] = "chatCompletions"
	}
	proof.Spec, err = canonicalKagentJSONValue(normalized)
	return proof, err
}

func kagentStringMap(value any) (map[string]string, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]string
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("reviewed metadata contains a non-string map")
	}
	return result, nil
}

func canonicalKagentJSONValue(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil || requireKagentJSONEOF(decoder) != nil {
		return nil, fmt.Errorf("invalid JSON value")
	}
	return result, nil
}

func validateKagentProofSpecAndMarkers(rawSpec json.RawMessage, annotations map[string]string, proof kagentObjectProof) error {
	if proof.suite != nil {
		if err := proof.suite.check(annotations); err != nil {
			return err
		}
	} else if hasOrkaSuiteReservation(annotations) {
		return fmt.Errorf("suite reservation blocks legacy Kagent proof")
	}
	actual, err := canonicalKagentJSONValue(json.RawMessage(rawSpec))
	if err != nil || !reflect.DeepEqual(actual, proof.Spec) {
		version := "v0.10.2"
		if marker := proof.Markers[kagentVersionMarker]; marker != "" {
			version = marker
		}
		return fmt.Errorf("server-mutated spec differs from exact %s normalization", version)
	}
	for key, value := range proof.Markers {
		if annotations[key] != value {
			return fmt.Errorf("KMX marker annotations changed")
		}
	}
	return nil
}

func (a *App) createKagentObject(ctx context.Context, namespace string, doc map[string]any, proof kagentObjectProof) (kagentIdentity, error) {
	id := kagentIdentity{Kind: doc["kind"].(string), Name: kagentObjectName(doc), Proof: proof}
	body, err := json.Marshal(doc)
	if err != nil {
		return id, err
	}
	raw, err := a.kagentCapture(ctx, body, "-n", namespace, "create", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return id, fmt.Errorf("create %s/%s failed or was ambiguous; it may exist and was not retried, adopted, or deleted: %w", id.Kind, id.Name, err)
	}
	var object kagentObject
	if err := json.Unmarshal(raw, &object); err != nil || object.Kind != id.Kind || object.Metadata.Name != id.Name ||
		object.APIVersion != proof.APIVersion || object.Metadata.Namespace != namespace || object.Metadata.UID == "" ||
		object.Metadata.Generation < 1 || object.Metadata.DeletionTimestamp != nil {
		return id, fmt.Errorf("create %s/%s returned no valid identity; it may exist and was not retried", id.Kind, id.Name)
	}
	// A valid server identity survives a later proof failure; this does not
	// authorize reuse or claim readiness of the created object.
	id.UID, id.Generation = object.Metadata.UID, object.Metadata.Generation
	if err := validateKagentProofSpecAndMarkers(object.Spec, object.Metadata.Annotations, proof); err != nil {
		return id, fmt.Errorf("create %s/%s returned a mutated admitted object; it may exist and no task or receipt was attempted: %w", id.Kind, id.Name, err)
	}
	if (proof.Markers[kagentVersionMarker] != "" || proof.suite != nil) && object.Metadata.Labels["app.kubernetes.io/managed-by"] != "kmx" {
		return id, fmt.Errorf("Kagent create response changed ownership; no retry or receipt")
	}
	return id, nil
}

func (a *App) readKagentObject(ctx context.Context, namespace string, id kagentIdentity) (*kagentObject, error) {
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", kagentPlural(id.Kind), id.Name, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("cannot read %s/%s UID %s; it may have disappeared: %w", id.Kind, id.Name, id.UID, err)
	}
	var object kagentObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, kagentFailure("unreadable", fmt.Errorf("invalid %s/%s status response", id.Kind, id.Name))
	}
	if object.Kind != id.Kind || object.Metadata.Name != id.Name || object.Metadata.Namespace != namespace ||
		object.APIVersion != id.Proof.APIVersion || object.Metadata.UID != id.UID || object.Metadata.Generation != id.Generation {
		return nil, kagentFailure("drift", fmt.Errorf("%s/%s UID %s was replaced or its spec generation changed; refusing stale state", id.Kind, id.Name, id.UID))
	}
	if object.Metadata.DeletionTimestamp != nil {
		return nil, kagentFailure("drift", fmt.Errorf("%s/%s UID %s is terminating; refusing stale state", id.Kind, id.Name, id.UID))
	}
	if (id.Proof.Markers[kagentVersionMarker] != "" || id.Proof.suite != nil) && object.Metadata.Labels["app.kubernetes.io/managed-by"] != "kmx" {
		return nil, kagentFailure("drift", fmt.Errorf("Kagent primary managed-by ownership label changed"))
	}
	if err := validateKagentProofSpecAndMarkers(object.Spec, object.Metadata.Annotations, id.Proof); err != nil {
		return nil, kagentFailure("drift", fmt.Errorf("%s/%s UID %s no longer retains its admitted spec and KMX markers: %w", id.Kind, id.Name, id.UID, err))
	}
	return &object, nil
}

func (a *App) waitKagentReady(ctx context.Context, namespace string, id kagentIdentity) error {
	for {
		object, err := a.readKagentObject(ctx, namespace, id)
		if err != nil {
			return err
		}
		accepted := object.Status.ObservedGeneration == id.Generation && kagentObjectConditionTrue(object.Status.Conditions, "Accepted", id)
		ready := id.Kind != "Agent" || object.Status.ObservedGeneration == id.Generation && kagentReadyConditionTrue(object.Status.Conditions, id.Generation)
		if accepted && ready {
			return nil
		}
		for _, condition := range object.Status.Conditions {
			generationMatches := condition.ObservedGeneration == id.Generation || id.Kind == "ModelConfig" && condition.ObservedGeneration == 0
			if generationMatches && condition.Status == "False" && condition.Type == "Accepted" {
				return fmt.Errorf("%s/%s UID %s was refused at its current generation: %s=False", id.Kind, id.Name, id.UID, condition.Type)
			}
		}
		if err := a.pause(ctx, time.Second); err != nil {
			return fmt.Errorf("waiting for %s/%s UID %s current-generation readiness: %w", id.Kind, id.Name, id.UID, err)
		}
	}
}

func kagentControllerOwner(metadata kagentMetadata, id kagentIdentity) bool {
	controllers := 0
	matched := false
	for _, owner := range metadata.OwnerReferences {
		if owner.Controller != nil && *owner.Controller {
			controllers++
			matched = owner.APIVersion == scaffold.KagentAPIVersion && owner.Kind == "Agent" && owner.Name == id.Name && owner.UID == id.UID
		}
	}
	return controllers == 1 && matched
}

func (a *App) observeKagentAgentWorkload(ctx context.Context, namespace string, id kagentIdentity, runtimeName string, previous *kagentWorkloadIdentity, observed *[]kagentEvaluationImage) (kagentWorkloadIdentity, error) {
	identity := kagentWorkloadIdentity{}
	object, err := a.readKagentObject(ctx, namespace, id)
	if err != nil {
		return identity, err
	}
	if object.Status.ObservedGeneration != id.Generation || !kagentObjectConditionTrue(object.Status.Conditions, "Accepted", id) ||
		!kagentReadyConditionTrue(object.Status.Conditions, id.Generation) {
		return identity, kagentFailure("failed", fmt.Errorf("Agent/%s UID %s is no longer current-generation Accepted and Ready", id.Name, id.UID))
	}
	raw, err := a.kagentCapture(ctx, nil, "-n", namespace, "get", "deployment", id.Name, "-o", "json")
	if err != nil {
		return identity, fmt.Errorf("cannot read generated Kagent Agent Deployment: %w", err)
	}
	var deployment kagentDeployment
	if json.Unmarshal(raw, &deployment) != nil {
		return identity, kagentFailure("unreadable", fmt.Errorf("generated Kagent Agent Deployment returned invalid JSON"))
	}
	if deployment.Kind != "Deployment" || deployment.Metadata.Name == "" || deployment.Metadata.Namespace == "" || deployment.Metadata.UID == "" || deployment.Metadata.ResourceVersion == "" || deployment.Metadata.Generation < 1 {
		return identity, kagentFailure("unreadable", fmt.Errorf("generated Kagent Agent Deployment returned incomplete identity"))
	}
	if deployment.Metadata.Name != id.Name || deployment.Metadata.Namespace != namespace || deployment.Metadata.DeletionTimestamp != nil || !kagentControllerOwner(deployment.Metadata, id) {
		return identity, kagentFailure("drift", fmt.Errorf("generated Kagent Agent Deployment has an invalid identity or is not controller-owned by Agent UID %s", id.UID))
	}
	image, repository, digests, err := a.selectedKagentProfile().runtimeImage(runtimeName)
	if err != nil {
		return identity, err
	}
	container, err := exactKagentContainer(deployment.Spec.Template.Spec.Containers, "kagent")
	if err != nil || !a.selectedKagentProfile().imageMatches(container.Image, image, repository, digests) {
		return identity, kagentFailure("drift", fmt.Errorf("generated Kagent Agent Deployment does not use exactly one kagent container with image %s", image))
	}
	if err := a.observeKagentDeploymentPods(ctx, namespace, deployment, "kagent", container.Image, repository, digests, observed); err != nil {
		return identity, fmt.Errorf("generated Kagent Agent Deployment is not a proven current rollout: %w", err)
	}
	raw, err = a.kagentCapture(ctx, nil, "-n", namespace, "get", "service", id.Name, "-o", "json")
	if err != nil {
		return identity, fmt.Errorf("cannot read generated Kagent Agent Service: %w", err)
	}
	var service struct {
		Kind     string         `json:"kind"`
		Metadata kagentMetadata `json:"metadata"`
		Spec     struct {
			Selector map[string]string `json:"selector"`
			Ports    []struct {
				Name, Protocol string
				Port           int32 `json:"port"`
			} `json:"ports"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &service) != nil {
		return identity, kagentFailure("unreadable", fmt.Errorf("generated Kagent Agent Service returned invalid JSON"))
	}
	if service.Kind != "Service" || service.Metadata.Name == "" || service.Metadata.Namespace == "" || service.Metadata.UID == "" || service.Metadata.ResourceVersion == "" {
		return identity, kagentFailure("unreadable", fmt.Errorf("generated Kagent Agent Service returned incomplete identity"))
	}
	if service.Metadata.Name != id.Name || service.Metadata.Namespace != namespace || service.Metadata.DeletionTimestamp != nil || !kagentControllerOwner(service.Metadata, id) ||
		len(service.Spec.Selector) == 0 || !equalKagentLabels(service.Spec.Selector, deployment.Spec.Selector.MatchLabels) ||
		!slices.ContainsFunc(service.Spec.Ports, func(port struct {
			Name, Protocol string
			Port           int32 `json:"port"`
		}) bool {
			return port.Protocol == "TCP" && port.Port == 8080
		}) {
		return identity, kagentFailure("drift", fmt.Errorf("generated Kagent Agent Service has an invalid identity, owner, selector, or port 8080"))
	}
	fingerprint := ""
	if a.selectedKagentProfile().Version == "v0.10.3" {
		fingerprint, err = kagentSpecFingerprint(deployment.SpecRaw)
		if err != nil {
			return identity, kagentFailure("unreadable", fmt.Errorf("generated Kagent Deployment spec is invalid"))
		}
	}
	identity = kagentWorkloadIdentity{
		DeploymentUID: deployment.Metadata.UID, DeploymentResourceVersion: deployment.Metadata.ResourceVersion,
		DeploymentGeneration: deployment.Metadata.Generation, DeploymentSpecDigest: fingerprint,
		ServiceUID: service.Metadata.UID, ServiceResourceVersion: service.Metadata.ResourceVersion,
	}
	if previous != nil && identity != *previous {
		return identity, kagentFailure("drift", fmt.Errorf("generated Kagent Agent Deployment or Service was replaced or changed"))
	}
	return identity, nil
}

// v0.10.2 stamps Agent conditions individually. Its ModelConfig controller
// instead stamps status.observedGeneration and writes Accepted in the same
// status update without condition.observedGeneration, so zero is valid only
// for that kind and only after the top-level generation check above succeeds.
func kagentObjectConditionTrue(conditions []serverCondition, conditionType string, id kagentIdentity) bool {
	for _, condition := range conditions {
		generationMatches := condition.ObservedGeneration == id.Generation || id.Kind == "ModelConfig" && condition.ObservedGeneration == 0
		if condition.Type == conditionType && condition.Status == "True" && generationMatches {
			return true
		}
	}
	return false
}

func kagentReadyConditionTrue(conditions []serverCondition, generation int64) bool {
	for _, condition := range conditions {
		if condition.Type == "Ready" && condition.Status == "True" &&
			condition.ObservedGeneration == generation && condition.Reason == "DeploymentReady" {
			return true
		}
	}
	return false
}

func requireKagentJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("extra JSON value")
	}
	return nil
}

func (a *App) kagentClusterUID(ctx context.Context) (string, error) {
	raw, err := a.kagentCapture(ctx, nil, "get", "namespace", "kube-system", "-o", "json")
	if err != nil {
		return "", fmt.Errorf("cannot establish Kagent create cluster identity (kube-system UID): %w", err)
	}
	var namespace struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name, UID string
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &namespace) != nil || namespace.Kind != "Namespace" || namespace.Metadata.Name != "kube-system" || namespace.Metadata.UID == "" {
		return "", fmt.Errorf("kube-system namespace returned no valid Kagent create cluster UID")
	}
	return namespace.Metadata.UID, nil
}
