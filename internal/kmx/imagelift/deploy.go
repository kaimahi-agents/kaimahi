package imagelift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

// Cluster binds all calls to a context outside this package. Calls accept only
// read/create/replace/rollout arguments; the deployer never deletes resources.
type Cluster interface {
	Call(context.Context, []byte, ...string) ([]byte, error)
}

type ResourceResult struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	UID     string `json:"uid"`
	Outcome string `json:"outcome"`
}

type Receipt struct {
	PlanDigest  string           `json:"planDigest"`
	Image       string           `json:"image"`
	SuiteDigest string           `json:"suiteDigest"`
	ClusterUID  string           `json:"clusterUID"`
	Namespace   string           `json:"namespace"`
	Ready       bool             `json:"ready"`
	Resources   []ResourceResult `json:"resources"`
}

type observed struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		UID               string            `json:"uid"`
		ResourceVersion   string            `json:"resourceVersion"`
		DeletionTimestamp *string           `json:"deletionTimestamp"`
		Labels            map[string]string `json:"labels"`
		Annotations       map[string]string `json:"annotations"`
		Generation        int64             `json:"generation"`
	} `json:"metadata"`
	Spec   map[string]any `json:"spec"`
	Status struct {
		ObservedGeneration int64 `json:"observedGeneration"`
		UpdatedReplicas    int   `json:"updatedReplicas"`
		AvailableReplicas  int   `json:"availableReplicas"`
	} `json:"status"`
}

// CheckTarget distinguishes an unreadable target from an absent prerequisite.
func CheckTarget(ctx context.Context, cluster Cluster, plan Plan) error {
	raw, err := cluster.Call(ctx, nil, "get", "namespace", "kube-system", "-o", "json")
	if err != nil {
		return fmt.Errorf("read cluster identity: %w", err)
	}
	var identity observed
	if json.Unmarshal(raw, &identity) != nil || identity.Kind != "Namespace" || identity.Metadata.Name != "kube-system" || identity.Metadata.UID != plan.ClusterUID {
		return errors.New("destination clusterUID differs from environment")
	}
	raw, err = cluster.Call(ctx, nil, "get", "namespace", plan.Namespace, "-o", "json")
	if err != nil {
		return fmt.Errorf("read destination namespace: %w", err)
	}
	var ns observed
	if json.Unmarshal(raw, &ns) != nil || ns.Kind != "Namespace" || ns.Metadata.Name != plan.Namespace || ns.Metadata.DeletionTimestamp != nil {
		return errors.New("destination namespace is missing or terminating")
	}
	raw, err = cluster.Call(ctx, nil, "get", "nodes", "-l", "kubernetes.io/os="+plan.Platform.OS+",kubernetes.io/arch="+plan.Platform.Architecture, "-o", "json")
	if err != nil {
		return fmt.Errorf("read target platform: %w", err)
	}
	var nodes struct {
		Items []struct {
			Spec struct {
				Unschedulable bool `json:"unschedulable"`
			} `json:"spec"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &nodes) != nil {
		return errors.New("invalid node inventory")
	}
	ready := false
	for _, node := range nodes.Items {
		for _, condition := range node.Status.Conditions {
			if !node.Spec.Unschedulable && condition.Type == "Ready" && condition.Status == "True" {
				ready = true
			}
		}
	}
	if !ready {
		return errors.New("no Ready schedulable node matches image platform")
	}
	for _, secret := range plan.Secrets {
		if err := checkSecret(ctx, cluster, plan.Namespace, secret); err != nil {
			return err
		}
	}
	return nil
}

func checkSecret(ctx context.Context, cluster Cluster, namespace string, secret agentsuite.SecretKeyRef) error {
	// kubectl formats presence only; no Secret values enter KMX output or receipts.
	template := `{{.metadata.name}}`
	if secret.Key != "" {
		template = fmt.Sprintf(`{{if index .data %q}}present{{end}}`, secret.Key)
	}
	raw, err := cluster.Call(ctx, nil, "-n", namespace, "get", "secret", secret.Name, "-o", "go-template="+template)
	if err != nil {
		return fmt.Errorf("read required Secret/%s: %w", secret.Name, err)
	}
	want := secret.Name
	if secret.Key != "" {
		want = "present"
	}
	if string(raw) != want {
		return fmt.Errorf("required Secret/%s key is missing or empty", secret.Name)
	}
	return nil
}

func readResource(ctx context.Context, cluster Cluster, plan Plan, object Object) (*observed, error) {
	kind := object["kind"].(string)
	raw, err := cluster.Call(ctx, nil, "-n", plan.Namespace, "get", kind, plan.Name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("inspect %s/%s: %w", kind, plan.Name, err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var current observed
	if json.Unmarshal(raw, &current) != nil || current.Kind != kind || current.Metadata.Name != plan.Name || current.Metadata.Namespace != plan.Namespace || current.Metadata.UID == "" || current.Metadata.ResourceVersion == "" || current.Metadata.DeletionTimestamp != nil {
		return nil, errors.New("invalid or terminating destination resource")
	}
	if current.Metadata.Labels[OwnerLabel] != plan.Name {
		return nil, fmt.Errorf("%s/%s belongs to another deployment", kind, plan.Name)
	}
	return &current, nil
}

// Inspect checks every resource before the first mutation, including server
// admission in dry-run mode. It never adopts a pre-existing unowned resource.
func Inspect(ctx context.Context, cluster Cluster, plan Plan) error {
	if err := CheckTarget(ctx, cluster, plan); err != nil {
		return err
	}
	for _, object := range plan.Objects {
		current, err := readResource(ctx, cluster, plan, object)
		if err != nil {
			return err
		}
		payload, verb, err := writePayload(object, current)
		if err != nil {
			return err
		}
		if _, err := cluster.Call(ctx, payload, "-n", plan.Namespace, verb, "--dry-run=server", "-f", "-", "-o", "json"); err != nil {
			return fmt.Errorf("server admission: %w", err)
		}
	}
	return nil
}

func writePayload(object Object, current *observed) ([]byte, string, error) {
	raw, err := json.Marshal(object)
	if err != nil {
		return nil, "", err
	}
	var candidate map[string]any
	if err := json.Unmarshal(raw, &candidate); err != nil {
		return nil, "", err
	}
	verb := "create"
	if current != nil {
		verb = "replace"
		meta := candidate["metadata"].(map[string]any)
		meta["uid"], meta["resourceVersion"] = current.Metadata.UID, current.Metadata.ResourceVersion
		if current.Kind == "Service" {
			spec := candidate["spec"].(map[string]any)
			for _, key := range []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy"} {
				if value, ok := current.Spec[key]; ok {
					spec[key] = value
				}
			}
		}
	}
	raw, err = json.Marshal(candidate)
	return raw, verb, err
}

// Deploy rechecks target and UID snapshots after confirmation. A failed mutation
// returns partial evidence and never retries an ambiguous write.
func Deploy(ctx context.Context, cluster Cluster, plan Plan, confirm func() error) (Receipt, error) {
	receipt := Receipt{PlanDigest: plan.Digest, Image: plan.Image, SuiteDigest: plan.SuiteDigest, ClusterUID: plan.ClusterUID, Namespace: plan.Namespace}
	if err := Inspect(ctx, cluster, plan); err != nil {
		return receipt, err
	}
	snapshots := make([]*observed, len(plan.Objects))
	for i, obj := range plan.Objects {
		var err error
		snapshots[i], err = readResource(ctx, cluster, plan, obj)
		if err != nil {
			return receipt, err
		}
	}
	if err := confirm(); err != nil {
		return receipt, err
	}
	if err := CheckTarget(ctx, cluster, plan); err != nil {
		return receipt, err
	}
	for i, obj := range plan.Objects {
		current, err := readResource(ctx, cluster, plan, obj)
		if err != nil {
			return receipt, err
		}
		old := snapshots[i]
		if (old == nil) != (current == nil) || old != nil && (old.Metadata.UID != current.Metadata.UID || old.Metadata.ResourceVersion != current.Metadata.ResourceVersion) {
			return receipt, errors.New("deployment resource changed after planning; inspect and plan again")
		}
		payload, verb, err := writePayload(obj, current)
		if err != nil {
			return receipt, err
		}
		raw, err := cluster.Call(ctx, payload, "-n", plan.Namespace, verb, "-f", "-", "-o", "json")
		if err != nil {
			return receipt, fmt.Errorf("%s %s/%s outcome unknown; inspect target before retrying: %w", verb, obj["kind"], plan.Name, err)
		}
		var result observed
		if json.Unmarshal(raw, &result) != nil || result.Metadata.UID == "" || result.Kind != obj["kind"] || result.Metadata.Name != plan.Name || result.Metadata.Namespace != plan.Namespace {
			return receipt, errors.New("write returned invalid resource identity; outcome unknown")
		}
		receipt.Resources = append(receipt.Resources, ResourceResult{Kind: result.Kind, Name: plan.Name, UID: result.Metadata.UID, Outcome: verb})
	}
	if _, err := cluster.Call(ctx, nil, "-n", plan.Namespace, "rollout", "status", "deployment/"+plan.Name, "--timeout=180s"); err != nil {
		return receipt, fmt.Errorf("resources written but readiness not established: %w", err)
	}
	// A rollout of a replacement object must not be attributed to this deployment.
	for i, obj := range plan.Objects {
		current, err := readResource(ctx, cluster, plan, obj)
		if err != nil {
			return receipt, err
		}
		if current == nil || current.Metadata.UID != receipt.Resources[i].UID || current.Metadata.Annotations[PlanAnnotation] != plan.Digest {
			return receipt, errors.New("resource replaced during rollout")
		}
		if current.Kind == "Deployment" && (current.Metadata.Generation < 1 || current.Status.ObservedGeneration < current.Metadata.Generation || current.Status.UpdatedReplicas != 1 || current.Status.AvailableReplicas < 1) {
			return receipt, errors.New("deployment is not Ready for its current generation")
		}
	}
	receipt.Ready = true
	return receipt, nil
}
