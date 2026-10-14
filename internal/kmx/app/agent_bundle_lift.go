package app

import (
	"context"
	"encoding/json"
	"fmt"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Read the cluster identity independently of kubeconfig context naming.
func (a *App) readLiftClusterUID(ctx context.Context) (string, bool, error) {
	raw, err := a.orkaCapture(ctx, nil, "get", "namespace", "kube-system", "-o", "json")
	if err != nil {
		return "", false, fmt.Errorf("cannot establish destination cluster identity (kube-system UID): %w", err)
	}
	var namespace struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name              string  `json:"name"`
			UID               string  `json:"uid"`
			DeletionTimestamp *string `json:"deletionTimestamp"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &namespace) != nil || namespace.Kind != "Namespace" || namespace.Metadata.Name != "kube-system" || namespace.Metadata.UID == "" {
		return "", false, fmt.Errorf("destination kube-system namespace returned no valid cluster UID")
	}
	return namespace.Metadata.UID, namespace.Metadata.DeletionTimestamp != nil, nil
}

type liftProviderIdentity struct {
	UID, ResourceVersion string
	Generation           int64
}

func (a *App) readLiftProviderBindings(ctx context.Context, namespace, provider string) (agentruntime.OrkaBindings, liftProviderIdentity, error) {
	var identity liftProviderIdentity
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", "providers.core.orka.ai", provider, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return agentruntime.OrkaBindings{}, identity, fmt.Errorf("cannot inspect destination Provider/%s: %w", provider, err)
	}
	var selected struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name              string  `json:"name"`
			Namespace         string  `json:"namespace"`
			UID               string  `json:"uid"`
			Generation        int64   `json:"generation"`
			ResourceVersion   string  `json:"resourceVersion"`
			DeletionTimestamp *string `json:"deletionTimestamp"`
		} `json:"metadata"`
		Spec struct {
			Type    string `json:"type"`
			BaseURL string `json:"baseURL"`
			Azure   struct {
				DeploymentName string `json:"deploymentName"`
				APIVersion     string `json:"apiVersion"`
			} `json:"azure"`
			SecretRef struct {
				Name string `json:"name"`
				Key  string `json:"key"`
			} `json:"secretRef"`
		} `json:"spec"`
		Status struct {
			Ready      bool              `json:"ready"`
			Conditions []serverCondition `json:"conditions"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &selected) != nil || selected.Kind != "Provider" || selected.Metadata.Name != provider || selected.Metadata.Namespace != namespace || selected.Metadata.UID == "" || selected.Metadata.Generation < 1 || selected.Metadata.DeletionTimestamp != nil {
		return agentruntime.OrkaBindings{}, identity, fmt.Errorf("destination Provider/%s is missing or has invalid identity; create a Ready Provider before lifting", provider)
	}
	ready := false
	for _, condition := range selected.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == "True" && condition.ObservedGeneration == selected.Metadata.Generation {
			ready = true
		}
	}
	if !selected.Status.Ready || !ready {
		return agentruntime.OrkaBindings{}, identity, fmt.Errorf("destination Provider/%s is not Ready for its current generation", provider)
	}
	// Render defaults an absent key to api-key for shorthand create. A lift
	// must not silently replace the selected Provider's actual reference.
	if selected.Spec.SecretRef.Key == "" {
		return agentruntime.OrkaBindings{}, identity, fmt.Errorf("destination Provider/%s has no explicit Secret reference key", provider)
	}
	bindings := agentruntime.OrkaBindings{Namespace: namespace, Provider: agentruntime.OrkaProviderBindings{Type: selected.Spec.Type, BaseURL: selected.Spec.BaseURL, Azure: agentruntime.OrkaAzureBindings{DeploymentName: selected.Spec.Azure.DeploymentName, APIVersion: selected.Spec.Azure.APIVersion}, SecretRef: agentruntime.OrkaSecretRefBindings{Name: selected.Spec.SecretRef.Name, Key: selected.Spec.SecretRef.Key}}}
	if err := agentruntime.ValidateOrkaBindings(bindings); err != nil {
		return agentruntime.OrkaBindings{}, identity, fmt.Errorf("destination Provider/%s has invalid bindings (type, endpoint or Secret reference): %w", provider, err)
	}
	identity = liftProviderIdentity{UID: selected.Metadata.UID, ResourceVersion: selected.Metadata.ResourceVersion, Generation: selected.Metadata.Generation}
	return bindings, identity, nil
}

func (a *App) liftClusterUID(ctx context.Context) (string, error) {
	uid, _, err := a.readLiftClusterUID(ctx)
	return uid, err
}
