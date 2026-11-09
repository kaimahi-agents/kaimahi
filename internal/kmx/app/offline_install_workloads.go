package app

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"
)

// offlineLiveDeployment is the projection of a live Orka Deployment that
// install and reuse compare and wait on.
type offlineLiveDeployment struct {
	Kind     string
	Metadata struct {
		Name, Namespace, UID string
		Generation           int64
		DeletionTimestamp    *string
	}
	Spec struct {
		Replicas int
		Selector struct {
			MatchLabels      map[string]string
			MatchExpressions []any
		}
		Template struct{ Spec offlineWorkload }
	}
	Status struct {
		ObservedGeneration                                                     int64
		UpdatedReplicas, AvailableReplicas, ReadyReplicas, UnavailableReplicas int
	}
}

// Only normal empty-list and readOnly=false omission are normalized. Execution
// settings, storage sources, mounts and service accounts remain bidirectional.
func offlineNormalizeWorkload(w offlineWorkload) offlineWorkload {
	normalize := func(c *offlineContainer) {
		if len(c.Command) == 0 {
			c.Command = nil
		}
		if len(c.Args) == 0 {
			c.Args = nil
		}
		if len(c.Env) == 0 {
			c.Env = nil
		}
		if len(c.EnvFrom) == 0 {
			c.EnvFrom = nil
		}
		if len(c.VolumeMounts) == 0 {
			c.VolumeMounts = nil
		} else {
			c.VolumeMounts = slices.Clone(c.VolumeMounts)
			for i, raw := range c.VolumeMounts {
				if mount, ok := raw.(map[string]any); ok && mount["readOnly"] == false {
					mount = maps.Clone(mount)
					delete(mount, "readOnly")
					c.VolumeMounts[i] = mount
				}
			}
		}
	}
	for i := range w.Containers {
		normalize(&w.Containers[i])
	}
	for i := range w.InitContainers {
		normalize(&w.InitContainers[i])
	}
	if len(w.InitContainers) == 0 {
		w.InitContainers = nil
	}
	if len(w.Volumes) == 0 {
		w.Volumes = nil
	}
	return w
}

// verifyOfflineWorkloads checks the live Deployments' execution identity. With
// rollout it also waits until every workload's pods report readiness no older
// than their kind node container's current start and Orka's API answers its
// typed /readyz through its Service. Deployment status alone can be the
// controller manager's record from before a node restart.
func (a *App) verifyOfflineWorkloads(b *offlineInstallBundle, rollout bool) error {
	if !rollout {
		_, err := a.observeOfflineWorkloads(a.operationContext(), b, false)
		return err
	}
	return a.awaitOrkaServing("offline Orka workloads", "effects retained, no reinstall attempted", func(ctx context.Context) (string, error) {
		return a.observeOfflineWorkloads(ctx, b, true)
	})
}

// observeOfflineWorkloads compares execution identity on every look. While
// waiting, reads are bounded by ctx, a failed read is a readiness gap, and the
// pods' current readiness and the API Service's answer are required too.
func (a *App) observeOfflineWorkloads(ctx context.Context, b *offlineInstallBundle, waiting bool) (string, error) {
	args := a.kubectl("-n", OrkaNamespace, "get", "deploy", "-o", "json")
	var raw []byte
	var err error
	if !waiting {
		raw, err = a.offlineCapture("kubectl", args...)
	} else if raw, err = a.offlineCaptureWithin(ctx, "kubectl", args...); err != nil {
		return fmt.Sprintf("Orka Deployments in %s could not be read", OrkaNamespace), nil
	}
	if err != nil {
		return "", err
	}
	var list struct{ Items []offlineLiveDeployment }
	if offlineJSON(raw, &list, false) != nil || len(list.Items) != len(b.workloads) {
		return "", fmt.Errorf("offline live workload set differs; refusing reinstall")
	}
	seen := map[string]bool{}
	for _, d := range list.Items {
		expected, ok := b.workloads[d.Metadata.Name]
		gotBytes, _ := json.Marshal(offlineNormalizeWorkload(d.Spec.Template.Spec))
		wantBytes, _ := json.Marshal(offlineNormalizeWorkload(expected))
		// Encoding both projections makes YAML/JSON env maps share number semantics.
		var got, want any
		json.Unmarshal(gotBytes, &got)
		json.Unmarshal(wantBytes, &want)
		if !ok || seen[d.Metadata.Name] || d.Kind != "Deployment" || d.Metadata.Namespace != OrkaNamespace || d.Metadata.UID == "" || d.Metadata.Generation < 1 || d.Metadata.DeletionTimestamp != nil || !reflect.DeepEqual(got, want) {
			return "", fmt.Errorf("offline live workload execution settings differ; refusing reinstall")
		}
		seen[d.Metadata.Name] = true
	}
	if !waiting {
		return "", nil
	}
	slices.SortFunc(list.Items, func(x, y offlineLiveDeployment) int {
		return strings.Compare(x.Metadata.Name, y.Metadata.Name)
	})
	for _, d := range list.Items {
		if d.Spec.Replicas <= 0 {
			return "", fmt.Errorf("offline live workload %s is scaled to %d; effects retained, no reinstall attempted", d.Metadata.Name, d.Spec.Replicas)
		}
		if d.Status.ObservedGeneration < d.Metadata.Generation || d.Status.UpdatedReplicas != d.Spec.Replicas || d.Status.AvailableReplicas != d.Spec.Replicas || d.Status.ReadyReplicas != d.Spec.Replicas || d.Status.UnavailableReplicas != 0 {
			return fmt.Sprintf("Deployment %s status is not a completed rollout", d.Metadata.Name), nil
		}
	}
	read := func(what string, proxied bool, args ...string) ([]byte, error) {
		raw, err := a.offlineCaptureWithin(ctx, "kubectl", a.kubectl(args...)...)
		if err != nil {
			return nil, orkaServingDenial(what, err, proxied)
		}
		return raw, nil
	}
	reader := orkaServingReader{
		pods: func(selector string) ([]byte, error) {
			return read("Orka pods", false, "-n", OrkaNamespace, "get", "pods", "--selector="+selector, "-o", "json")
		},
		replicaSets: func(selector string) ([]byte, error) {
			return read("Orka ReplicaSets", false, "-n", OrkaNamespace, "get", "replicasets", "--selector="+selector, "-o", "json")
		},
		services: func() ([]byte, error) {
			return read("Orka Services", false, "-n", OrkaNamespace, "get", "services", "-o", "json")
		},
		get: func(path string) ([]byte, error) {
			return read("Orka's API readiness through its Service", true, "get", "--raw", path)
		},
		decode:    func(raw []byte, out any) error { return offlineJSON(raw, out, false) },
		nodeStart: func(node string) (time.Time, error) { return a.kindNodeStartedAt(ctx, node) },
	}
	for _, d := range list.Items {
		images := map[string]string{}
		for _, c := range b.workloads[d.Metadata.Name].Containers {
			images[c.Name] = c.Image
		}
		pending, err := reader.check(orkaServingWorkload{
			name: d.Metadata.Name, uid: d.Metadata.UID, matchLabels: d.Spec.Selector.MatchLabels,
			matchExpressions: len(d.Spec.Selector.MatchExpressions), replicas: d.Spec.Replicas, images: images,
		})
		if pending != "" || err != nil {
			return pending, err
		}
	}
	return reader.apiServing()
}
