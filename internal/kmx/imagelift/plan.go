package imagelift

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const OwnerLabel = "kaimahi.dev/image-deployment"
const PlanAnnotation = "kaimahi.dev/image-plan-digest"

type Object map[string]any

type Plan struct {
	Name        string                    `json:"name"`
	Context     string                    `json:"context"`
	ClusterUID  string                    `json:"clusterUID"`
	Namespace   string                    `json:"namespace"`
	Image       string                    `json:"image"`
	SuiteDigest string                    `json:"suiteDigest"`
	Digest      string                    `json:"digest"`
	Objects     []Object                  `json:"objects"`
	Secrets     []agentsuite.SecretKeyRef `json:"secretReferences"`
	Platform    agentsuite.Platform       `json:"platform"`
}

// Render builds an exact, protocol-specific deployment without cluster access.
// All supplied bindings must be consumed; secret slots accept references only.
func Render(image string, record agentsuite.ImageDeployment, env Environment) (Plan, error) {
	if err := env.Validate(); err != nil {
		return Plan{}, err
	}
	if _, err := agentsuite.EncodeImageDeployment(record); err != nil {
		return Plan{}, err
	}
	if record.Platform != env.Platform || record.Execution.Kind != env.Adapter {
		return Plan{}, errors.New("image and environment execution contract/platform differ")
	}
	if !strings.Contains(image, "@sha256:") {
		return Plan{}, errors.New("deployment image must be pinned by digest")
	}
	if len(env.Inputs) != len(record.Execution.Inputs) {
		return Plan{}, errors.New("environment inputs must exactly match image execution slots")
	}
	var variables []any
	var secrets []agentsuite.SecretKeyRef
	var config []byte
	if record.Execution.Configuration == agentsuite.AgentKitMountedConfig {
		var err error
		config, err = runtimeConfig(record, env)
		if err != nil {
			return Plan{}, err
		}
		if env.Inference.Credential != nil {
			variables = append(variables, Object{"name": "KMX_INFERENCE_KEY", "valueFrom": Object{"secretKeyRef": env.Inference.Credential}})
			secrets = append(secrets, *env.Inference.Credential)
		}
	} else if env.Inference != nil {
		return Plan{}, errors.New("image does not support deployment-time inference configuration")
	}
	inputs := append([]agentsuite.ExecutionInput(nil), record.Execution.Inputs...)
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Name < inputs[j].Name })
	for _, input := range inputs {
		if input.Environment == "KMX_INFERENCE_KEY" {
			return Plan{}, errors.New("execution input collides with inference credential")
		}
		binding, ok := env.Inputs[input.Name]
		if !ok {
			return Plan{}, fmt.Errorf("missing binding for input %q", input.Name)
		}
		entry := Object{"name": input.Environment}
		if input.Secret {
			if binding.SecretRef == nil {
				return Plan{}, fmt.Errorf("input %q requires a Secret key reference", input.Name)
			}
			entry["valueFrom"] = Object{"secretKeyRef": binding.SecretRef}
			secrets = append(secrets, *binding.SecretRef)
		} else {
			if binding.Value == nil {
				return Plan{}, fmt.Errorf("input %q requires a non-secret value", input.Name)
			}
			if secretshapes.Match(*binding.Value) != nil {
				return Plan{}, fmt.Errorf("input %q contains a credential-shaped literal", input.Name)
			}
			entry["value"] = *binding.Value
		}
		variables = append(variables, entry)
	}
	labels := Object{OwnerLabel: env.Name}
	metadata := func() Object { return Object{"name": env.Name, "namespace": env.Namespace, "labels": labels} }
	var pulls []any
	for _, name := range env.ImagePullSecrets {
		pulls = append(pulls, Object{"name": name})
		secrets = append(secrets, agentsuite.SecretKeyRef{Name: name})
	}
	pod := Object{
		"automountServiceAccountToken": false,
		"nodeSelector":                 Object{"kubernetes.io/os": env.Platform.OS, "kubernetes.io/arch": env.Platform.Architecture},
		"securityContext":              Object{"runAsNonRoot": true, "seccompProfile": Object{"type": "RuntimeDefault"}},
		"volumes":                      []any{Object{"name": "tmp", "emptyDir": Object{"sizeLimit": "128Mi"}}},
		"containers": []any{Object{
			"name": "agent", "image": image, "imagePullPolicy": "IfNotPresent", "env": variables,
			"ports":           []any{Object{"name": "http", "containerPort": record.Execution.Port}},
			"securityContext": Object{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": Object{"drop": []string{"ALL"}}},
			"volumeMounts":    []any{Object{"name": "tmp", "mountPath": "/tmp"}},
			"readinessProbe":  Object{"httpGet": Object{"path": record.Execution.HealthPath, "port": "http"}, "periodSeconds": 5},
			"resources":       Object{"requests": Object{"cpu": "100m", "memory": "128Mi"}, "limits": Object{"cpu": "1", "memory": "1Gi"}},
		}},
	}
	if config != nil {
		pod["volumes"] = append(pod["volumes"].([]any), Object{"name": "agent-config", "configMap": Object{"name": env.Name}})
		container := pod["containers"].([]any)[0].(Object)
		container["volumeMounts"] = append(container["volumeMounts"].([]any), Object{"name": "agent-config", "mountPath": "/run/agentsuite", "readOnly": true})
	}
	if len(pulls) > 0 {
		pod["imagePullSecrets"] = pulls
	}
	deployment := Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": metadata(), "spec": Object{
		"replicas": 1, "selector": Object{"matchLabels": labels},
		"template": Object{"metadata": Object{"labels": labels}, "spec": pod},
	}}
	service := Object{"apiVersion": "v1", "kind": "Service", "metadata": metadata(), "spec": Object{
		"type": "ClusterIP", "selector": labels, "ports": []any{Object{"name": "http", "port": 80, "targetPort": "http"}},
	}}
	plan := Plan{Name: env.Name, Context: env.Context, ClusterUID: env.ClusterUID, Namespace: env.Namespace, Image: image, SuiteDigest: record.SuiteDigest, Objects: []Object{deployment, service}, Secrets: secrets, Platform: env.Platform}
	if config != nil {
		// Configuration changes belong on the pod template, not immutable selectors.
		sum := sha256.Sum256(config)
		deployment["spec"].(Object)["template"].(Object)["metadata"].(Object)["annotations"] = Object{"kaimahi.dev/config": fmt.Sprintf("%x", sum)}
		plan.Objects = append([]Object{{"apiVersion": "v1", "kind": "ConfigMap", "metadata": metadata(), "data": Object{"agent.json": string(config)}}}, plan.Objects...)
	}
	// Include the complete declaration, even when fields do not render as resources.
	data, err := json.Marshal(struct {
		Plan      Plan
		Record    agentsuite.ImageDeployment
		Inference *InferenceBinding
	}{plan, record, env.Inference})
	if err != nil {
		return Plan{}, err
	}
	plan.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	for _, obj := range plan.Objects {
		obj["metadata"].(Object)["annotations"] = Object{PlanAnnotation: plan.Digest, "kaimahi.dev/suite-digest": record.SuiteDigest}
	}
	return plan, nil
}
