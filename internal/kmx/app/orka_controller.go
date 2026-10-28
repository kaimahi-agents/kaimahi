package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Both manifests identify the controller by role, not by Deployment name.
// The v0.1.3 kustomize manifest uses control-plane=controller-manager and
// app.kubernetes.io/name=orka; v0.2.0's chart uses component=controller.
type orkaDeployment struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		UID               string            `json:"uid"`
		DeletionTimestamp *string           `json:"deletionTimestamp"`
		Labels            map[string]string `json:"labels"`
		Generation        int64             `json:"generation"`
	} `json:"metadata"`
	Spec struct {
		Replicas int32 `json:"replicas"`
		Template struct {
			Spec struct {
				ServiceAccountName string `json:"serviceAccountName"`
				Containers         []struct {
					Name    string   `json:"name"`
					Image   string   `json:"image"`
					Command []string `json:"command"`
					Args    []string `json:"args"`
				} `json:"containers"`
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

func (a *App) orkaDeployments(ctx context.Context) ([]orkaDeployment, error) {
	data, err := a.orkaCapture(ctx, nil, "-n", OrkaNamespace, "get", "deploy", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("cannot list Orka controller Deployments in %s: %w", OrkaNamespace, err)
	}
	var list struct {
		Items []orkaDeployment `json:"items"`
	}
	if len(data) != 0 {
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("cannot read Orka Deployments in %s: malformed response", OrkaNamespace)
		}
	}
	return list.Items, nil
}

// selectOrkaController fails closed on zero or ambiguous role matches. A
// matching image or familiar Deployment name is not evidence of ownership.
func selectOrkaController(deployments []orkaDeployment) (orkaDeployment, bool, error) {
	var selected orkaDeployment
	legacy := false
	matches := 0
	for _, d := range deployments {
		labels := d.Metadata.Labels
		old := labels["control-plane"] == "controller-manager" && labels["app.kubernetes.io/name"] == "orka"
		chart := labels["app.kubernetes.io/component"] == "controller" && labels["app.kubernetes.io/name"] == "orka"
		if !old && !chart {
			continue
		}
		matches++
		selected, legacy = d, old && !chart
	}
	switch matches {
	case 0:
		return orkaDeployment{}, false, fmt.Errorf("no Orka controller Deployment in %s (expected Orka identity plus chart component=controller or legacy control-plane=controller-manager labels)", OrkaNamespace)
	case 1:
		if selected.Metadata.Name == "" {
			return orkaDeployment{}, false, fmt.Errorf("Orka controller Deployment in %s has no name", OrkaNamespace)
		}
		return selected, legacy, nil
	default:
		return orkaDeployment{}, false, fmt.Errorf("multiple Orka controller Deployments in %s; refusing ambiguous controller selection", OrkaNamespace)
	}
}

func orkaControllerWatches(controller orkaDeployment, namespace string) error {
	containers, claims := 0, 0
	watched := ""
	for _, container := range controller.Spec.Template.Spec.Containers {
		if container.Name != "controller" {
			continue
		}
		containers++
		for _, arg := range container.Args {
			key, value, joined := strings.Cut(arg, "=")
			if !joined || !strings.HasPrefix(key, "--") {
				return fmt.Errorf("management Orka controller requires joined --name=value arguments for namespace verification")
			}
			if key == "--watch-namespace" {
				claims++
				watched = value
			}
		}
	}
	if containers != 1 || claims != 1 || watched != namespace {
		return fmt.Errorf("management Orka controller does not unambiguously watch Task namespace %s", namespace)
	}
	return nil
}

func (a *App) orkaControllerForLift(ctx context.Context) (string, error) {
	deployments, err := a.orkaDeployments(ctx)
	if err != nil {
		return "", err
	}
	controller, _, err := selectOrkaController(deployments)
	if err != nil {
		return "", err
	}
	return controller.Metadata.Name, nil
}

func (a *App) orkaRunningVersion() (string, error) {
	deployments, err := a.orkaDeployments(a.operationContext())
	if err != nil {
		return "", err
	}
	controller, _, err := selectOrkaController(deployments)
	if err != nil {
		return "", err
	}
	if len(controller.Spec.Template.Spec.Containers) == 0 {
		return "unknown (controller image absent)", nil
	}
	image := strings.TrimSpace(controller.Spec.Template.Spec.Containers[0].Image)
	if image == "ghcr.io/orka-agents/orka@"+orkaControllerDigest {
		return "0.2.0 (release image digest)", nil
	}
	// A foreign digest cannot be inferred from a release tag.
	if strings.Contains(image, "@sha256:") {
		return "unknown (unrecognized controller image digest)", nil
	}
	// A registry port is not an image tag; inspect only the last path component.
	_, tag, found := strings.Cut(image[strings.LastIndex(image, "/")+1:], ":")
	if !found || tag == "" {
		return "unknown (controller image has no tag)", nil
	}
	if strings.TrimPrefix(tag, "v") == strings.TrimPrefix(OrkaVersion, "v") {
		return tag, nil
	}
	return tag + " (kmx pins " + OrkaVersion + " — this cluster was installed another way)", nil
}
