package app

// `kmx ax` is an evaluation view over a platform Kaimahi does not install.
//
// AX v0.3.0's release has no binary or deployment assets. Its documented
// install builds ax-controller and ax-server from source with ko, pushes them
// to an operator-owned registry, deploys Redis, and expects Agent Substrate to
// be present already. That is not a reproducible installer kmx can wrap without
// becoming the owner of AX's build and supply chain. The honest integration is
// therefore read-only: identify what an operator installed, expose the image
// templates it declares, and check Kubernetes Services its controller names.

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
)

const (
	// AXEvaluationVersion and AXEvaluationRevision identify the upstream
	// source read for this integration. They are an evaluation baseline, NOT
	// an install pin: kmx has no AX bytes to apply and status never restates
	// either value as the version running.
	AXEvaluationVersion  = "v0.3.0"
	AXEvaluationRevision = "d8ed0fe38bceb7842d3c47817d53d16ccdfcb601"

	AXNamespace          = "ax-system"
	axSubstrateNamespace = "ate-system"
)

var axDeploymentNames = []string{"ax-controller", "ax-server", "ax-redis"}
var axServiceNames = []string{"ax-server", "ax-redis"}

type axDeployment struct {
	Metadata struct {
		Name       string `json:"name"`
		Generation int64  `json:"generation"`
	} `json:"metadata"`
	Spec struct {
		Replicas int32 `json:"replicas"`
		Template struct {
			Spec struct {
				Containers []struct {
					Image string   `json:"image"`
					Args  []string `json:"args"`
					Env   []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"env"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration  int64 `json:"observedGeneration"`
		UpdatedReplicas     int32 `json:"updatedReplicas"`
		ReadyReplicas       int32 `json:"readyReplicas"`
		AvailableReplicas   int32 `json:"availableReplicas"`
		UnavailableReplicas int32 `json:"unavailableReplicas"`
	} `json:"status"`
}

type axDeploymentList struct {
	Kind  string         `json:"kind"`
	Items []axDeployment `json:"items"`
}

type axServiceList struct {
	Kind  string `json:"kind"`
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	} `json:"items"`
}

// AXStatus reports an externally installed AX control plane and the default
// Agent Substrate services its evaluated manifest references.
//
// It deliberately does not call this proof of sandboxing. Ready control-plane
// Deployments prove that AX, Redis and their probes are running; only a real AX
// Task can prove which backend executed it, which egress policy applied, and
// whether suspend/resume preserves the state the workload depends on.
func (a *App) AXStatus(namespace string) error {
	if !validAXDNSLabel(namespace) {
		return fmt.Errorf("AX namespace %q is not a Kubernetes DNS label", namespace)
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}

	fmt.Fprintf(a.Out, "%-24s %s\n", "support", "evaluation only — kmx does not install AX")
	fmt.Fprintf(a.Out, "%-24s %s (%s)\n", "source baseline", AXEvaluationVersion, AXEvaluationRevision[:12])

	deployments, absent, err := a.axDeployments(namespace)
	if err != nil {
		return err
	}
	if absent || !hasAnyAXDeployment(deployments) {
		fmt.Fprintf(a.Out, "%-24s %s\n", "AX deployments", "not detected in namespace "+namespace)
	} else {
		fmt.Fprintf(a.Out, "%-24s %s\n", "AX deployments", axDeploymentSummary(deployments))
		fmt.Fprintf(a.Out, "%-24s %s\n", "image templates declared", axImageSummary(deployments))
		endpoint, router := axControllerConnections(deployments["ax-controller"])
		fmt.Fprintf(a.Out, "%-24s %s\n", "Substrate endpoint", dashIfEmpty(endpoint))
		fmt.Fprintf(a.Out, "%-24s %s\n", "Substrate router", dashIfEmpty(router))
		for _, ref := range []struct {
			label string
			value string
		}{
			{"Substrate API Service", endpoint},
			{"Substrate router Service", router},
		} {
			fmt.Fprintf(a.Out, "%-24s %s\n", ref.label, a.axReferencedService(ref.value))
		}
	}

	axServices, axServicesState, err := a.axServices(namespace)
	if err != nil {
		return fmt.Errorf("cannot read AX services, so their presence is unknown: %w", err)
	}
	fmt.Fprintf(a.Out, "%-24s %s\n", "AX services",
		serviceSummary(axServices, axServiceNames, axServicesState))

	fmt.Fprintln(a.Out, "\nReady components do not prove a sandboxed Task, egress enforcement, suspend/resume,")
	fmt.Fprintln(a.Out, "or AX/Substrate version compatibility. Run a representative AX Task to prove those.")
	fmt.Fprintln(a.Out, "AX must be installed from its reviewed upstream source; there is no `kmx ax install`.")
	return nil
}

func (a *App) axDeployments(namespace string) (map[string]axDeployment, bool, error) {
	raw, err := a.kubectlCapture("-n", namespace, "get", "deploy", "-o", "json", statusRequestTimeout)
	switch {
	case unreachable(err):
		return nil, false, fmt.Errorf("cannot read AX: the cluster did not answer.\n  This is not the same as AX being absent — kmx does not know either way.\n  %w", err)
	case isNotFound(err):
		return map[string]axDeployment{}, true, nil
	case err != nil:
		return nil, false, fmt.Errorf("cannot read AX deployments in namespace %s, so their state is unknown: %w", namespace, err)
	}

	var list axDeploymentList
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, false, fmt.Errorf("cannot decode AX deployments in namespace %s, so their state is unknown: %w", namespace, err)
	}
	if list.Kind != "DeploymentList" {
		return nil, false, fmt.Errorf("cannot decode AX deployments in namespace %s, so their state is unknown: expected Kubernetes DeploymentList, got kind %q", namespace, list.Kind)
	}
	got := make(map[string]axDeployment, len(list.Items))
	for _, deployment := range list.Items {
		got[deployment.Metadata.Name] = deployment
	}
	return got, false, nil
}

func (a *App) axServices(namespace string) (map[string]bool, string, error) {
	raw, err := a.kubectlCapture("-n", namespace, "get", "svc", "-o", "json", statusRequestTimeout)
	switch {
	case unreachable(err):
		return nil, "", err
	case isNotFound(err):
		return map[string]bool{}, "not detected in namespace " + namespace, nil
	case err != nil:
		return map[string]bool{}, "unknown — " + firstLine(err.Error()), nil
	}

	var list axServiceList
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, "", fmt.Errorf("cannot decode Services in namespace %s: %w", namespace, err)
	}
	if list.Kind != "ServiceList" {
		return nil, "", fmt.Errorf("cannot decode Services in namespace %s: expected Kubernetes ServiceList, got kind %q", namespace, list.Kind)
	}
	got := make(map[string]bool, len(list.Items))
	for _, service := range list.Items {
		got[service.Metadata.Name] = true
	}
	return got, "", nil
}

func hasAnyAXDeployment(deployments map[string]axDeployment) bool {
	for _, name := range axDeploymentNames {
		if _, ok := deployments[name]; ok {
			return true
		}
	}
	return false
}

func axDeploymentSummary(deployments map[string]axDeployment) string {
	parts := make([]string, 0, len(axDeploymentNames))
	for _, name := range axDeploymentNames {
		deployment, ok := deployments[name]
		parts = append(parts, name+"="+axDeploymentState(deployment, ok))
	}
	return strings.Join(parts, " ")
}

func axDeploymentState(deployment axDeployment, present bool) string {
	if !present {
		return "missing"
	}
	desired := deployment.Spec.Replicas
	if desired <= 0 {
		return "scaled-to-zero"
	}
	if deployment.Status.ObservedGeneration < deployment.Metadata.Generation {
		return fmt.Sprintf("%d/%d ready (generation %d not observed)", deployment.Status.ReadyReplicas,
			desired, deployment.Metadata.Generation)
	}
	if deployment.Status.UpdatedReplicas == desired &&
		deployment.Status.ReadyReplicas == desired &&
		deployment.Status.AvailableReplicas == desired &&
		deployment.Status.UnavailableReplicas == 0 {
		return fmt.Sprintf("%d/%d ready", deployment.Status.ReadyReplicas, desired)
	}
	return fmt.Sprintf("%d/%d ready (%d updated, %d available, %d unavailable)",
		deployment.Status.ReadyReplicas, desired, deployment.Status.UpdatedReplicas,
		deployment.Status.AvailableReplicas, deployment.Status.UnavailableReplicas)
}

func axImageSummary(deployments map[string]axDeployment) string {
	parts := make([]string, 0, len(axDeploymentNames))
	for _, name := range axDeploymentNames {
		deployment, ok := deployments[name]
		if !ok {
			parts = append(parts, name+"=missing")
			continue
		}
		image := "unknown"
		if len(deployment.Spec.Template.Spec.Containers) > 0 &&
			strings.TrimSpace(deployment.Spec.Template.Spec.Containers[0].Image) != "" {
			image = deployment.Spec.Template.Spec.Containers[0].Image
		}
		parts = append(parts, name+"="+image)
	}
	return strings.Join(parts, " ")
}

// axReferencedService checks a Service only when the AX controller names one
// by Kubernetes DNS. An external endpoint is a legitimate AX configuration,
// but kubectl cannot prove it reachable, so status names that limit rather than
// checking an irrelevant default namespace.
func (a *App) axReferencedService(address string) string {
	host := strings.TrimSpace(address)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	parts := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(parts) < 3 || parts[2] != "svc" {
		if host == "" {
			return "unknown — controller does not declare it"
		}
		return "external or non-Service endpoint — not checkable through Kubernetes"
	}
	service, namespace := parts[0], parts[1]
	// These strings come from a Deployment controlled by someone other than
	// kmx. Never pass them to kubectl until they are proven to be values, not
	// flags: a service named "--context=other" would otherwise retarget the
	// read despite kmx having put its intended --context first.
	if !validAXDNSLabel(service) || !validAXDNSLabel(namespace) {
		return "invalid Kubernetes Service reference — not queried"
	}
	_, err := a.kubectlCapture("-n", namespace, "get", "svc", service, "-o", "name", statusRequestTimeout)
	switch {
	case err == nil:
		return namespace + "/" + service + " present"
	case unreachable(err):
		return "unknown — cluster did not answer: " + firstLine(err.Error())
	case isNotFound(err):
		return namespace + "/" + service + " missing"
	default:
		return "unknown — " + firstLine(err.Error())
	}
}

func axControllerConnections(controller axDeployment) (string, string) {
	if len(controller.Spec.Template.Spec.Containers) == 0 {
		return "", ""
	}
	container := controller.Spec.Template.Spec.Containers[0]
	var endpoint, router string
	for i, arg := range container.Args {
		if value, ok := strings.CutPrefix(arg, "--substrate-endpoint="); ok {
			endpoint = value
			continue
		}
		// Go's flag package accepts both `--flag=value` and `--flag value`.
		if arg == "--substrate-endpoint" && i+1 < len(container.Args) {
			endpoint = container.Args[i+1]
		}
	}
	for _, env := range container.Env {
		if env.Name == "ATENET_ROUTER_ADDR" {
			router = env.Value
		}
	}
	return endpoint, router
}

var axDNSLabel = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

func validAXDNSLabel(value string) bool {
	return len(value) > 0 && len(value) <= 63 && axDNSLabel.MatchString(value)
}

func serviceSummary(got map[string]bool, expected []string, state string) string {
	if state != "" {
		return state
	}
	parts := make([]string, 0, len(expected))
	for _, name := range expected {
		status := "missing"
		if got[name] {
			status = "present"
		}
		parts = append(parts, name+"="+status)
	}
	return strings.Join(parts, " ")
}

func dashIfEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown — not declared on the controller Deployment"
	}
	return value
}
