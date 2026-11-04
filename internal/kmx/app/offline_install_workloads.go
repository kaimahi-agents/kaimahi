package app

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
)

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
func (a *App) verifyOfflineWorkloads(b *offlineInstallBundle, rollout bool) error {
	if rollout {
		names := []string{}
		for name := range b.workloads {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			if _, err := a.offlineCapture("kubectl", a.kubectl("-n", OrkaNamespace, "rollout", "status", "deployment/"+name, "--timeout=180s")...); err != nil {
				return fmt.Errorf("offline workload rollout failed; installed effects retained: %w", err)
			}
		}
	}
	raw, err := a.offlineCapture("kubectl", a.kubectl("-n", OrkaNamespace, "get", "deploy", "-o", "json")...)
	if err != nil {
		return err
	}
	var list struct {
		Items []struct {
			Kind     string
			Metadata struct {
				Name, Namespace, UID string
				Generation           int64
				DeletionTimestamp    *string
			}
			Spec struct {
				Replicas int
				Template struct{ Spec offlineWorkload }
			}
			Status struct {
				ObservedGeneration                                                     int64
				UpdatedReplicas, AvailableReplicas, ReadyReplicas, UnavailableReplicas int
			}
		}
	}
	if offlineJSON(raw, &list, false) != nil || len(list.Items) != len(b.workloads) {
		return fmt.Errorf("offline live workload set differs; refusing reinstall")
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
			return fmt.Errorf("offline live workload execution settings differ; refusing reinstall")
		}
		seen[d.Metadata.Name] = true
		if rollout && (d.Spec.Replicas <= 0 || d.Status.ObservedGeneration < d.Metadata.Generation || d.Status.UpdatedReplicas != d.Spec.Replicas || d.Status.AvailableReplicas != d.Spec.Replicas || d.Status.ReadyReplicas != d.Spec.Replicas || d.Status.UnavailableReplicas != 0) {
			return fmt.Errorf("offline live workload is not healthy; effects retained, no reinstall attempted")
		}
	}
	return nil
}
