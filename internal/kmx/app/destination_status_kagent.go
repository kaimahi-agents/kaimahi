package app

import (
	"encoding/json"
	"reflect"
)

func statusMap(object map[string]any, key string) map[string]any {
	value, _ := object[key].(map[string]any)
	return value
}
func statusString(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}
func statusControllerOwner(meta map[string]any, version, kind, name, uid string) bool {
	owners, ok := meta["ownerReferences"].([]any)
	if !ok {
		return false
	}
	controllers, matched := 0, false
	for _, value := range owners {
		owner, ok := value.(map[string]any)
		if !ok {
			return false
		}
		if owner["controller"] == true {
			controllers++
			matched = owner["apiVersion"] == version && owner["kind"] == kind && owner["name"] == name && owner["uid"] == uid
		}
	}
	return controllers == 1 && matched
}
func statusLabels(value map[string]any) map[string]string {
	if len(value) == 0 || len(value) > 64 {
		return nil
	}
	out := map[string]string{}
	for key, raw := range value {
		s, ok := raw.(string)
		if !ok || !statusSafeText(key) || !statusSafeText(s) {
			return nil
		}
		out[key] = s
	}
	return out
}
func statusWorkloadRow(doc map[string]any, ready destinationStatusReadiness) destinationStatusWorkload {
	meta := statusMap(doc, "metadata")
	return destinationStatusWorkload{Kind: statusString(doc, "kind"), Name: statusString(meta, "name"), UID: statusString(meta, "uid"), Readiness: ready}
}
func (o *destinationStatusObserver) workloadObject(prefix, plural, kind, version, name string) (map[string]any, bool) {
	var doc map[string]any
	ok := o.get(prefix+"/namespaces/"+o.report.Target.Namespace+"/"+plural+"/"+name, &doc) && o.validObject(doc, kind, version) && statusString(statusMap(doc, "metadata"), "name") == name
	return doc, ok
}
func statusRollout(doc map[string]any, desired int64) destinationStatusReadiness {
	unknown := destinationStatusReadiness{"unknown", "workload rollout counters or identity are malformed"}
	pending := destinationStatusReadiness{"pending", "owned workload rollout is not complete at its current generation"}
	meta, spec, status := statusMap(doc, "metadata"), statusMap(doc, "spec"), statusMap(doc, "status")
	if doc["status"] != nil && status == nil {
		return unknown
	}
	generation, ok := statusInteger(meta["generation"])
	if !ok || generation < 1 {
		return unknown
	}
	if meta["deletionTimestamp"] != nil {
		return pending
	}
	replicas, ok := statusInteger(spec["replicas"])
	if !ok {
		return unknown
	}
	if replicas != desired || desired < 1 {
		return pending
	}
	observed := int64(0)
	if value, present := status["observedGeneration"]; present {
		var valid bool
		observed, valid = statusInteger(value)
		if !valid || observed > generation {
			return unknown
		}
	}
	if observed != generation {
		return pending
	}
	keys := []string{"replicas", "readyReplicas", "availableReplicas"}
	if doc["kind"] == "Deployment" {
		keys = append(keys, "updatedReplicas")
	}
	for _, key := range keys {
		n := int64(0)
		if value, present := status[key]; present {
			var valid bool
			n, valid = statusInteger(value)
			if !valid {
				return unknown
			}
		}
		if n != desired {
			return pending
		}
	}
	if value, present := status["unavailableReplicas"]; present {
		n, valid := statusInteger(value)
		if !valid {
			return unknown
		}
		if n != 0 {
			return pending
		}
	}
	return destinationStatusReadiness{"ready", "owner-UID-scoped current rollout is ready; runtime images are not qualified"}
}
func statusTemplateDigest(doc map[string]any) (string, error) {
	raw, err := json.Marshal(doc["spec"])
	if err != nil {
		return "", err
	}
	return kagentDeploymentTemplateFingerprint(raw)
}

// Observe only workload ownership and readiness. No generated Secret, Node,
// controller proxy, auth probe, image qualification or execution is performed.
func (o *destinationStatusObserver) kagentWorkloads(agent map[string]any) ([]destinationStatusWorkload, destinationStatusReadiness) {
	unknown := destinationStatusReadiness{"unknown", "owned kagent workload observation is incomplete or unsupported"}
	drift := destinationStatusReadiness{"drifted", "generated workload identity, owner or selector differs"}
	pending := destinationStatusReadiness{"pending", "owned kagent workload rollout is not complete"}
	rows := []destinationStatusWorkload{}
	meta := statusMap(agent, "metadata")
	name, uid := statusString(meta, "name"), statusString(meta, "uid")
	deployment, ok := o.workloadObject("/apis/apps/v1", "deployments", "Deployment", "apps/v1", name)
	if !ok {
		return rows, unknown
	}
	dm, ds := statusMap(deployment, "metadata"), statusMap(deployment, "spec")
	if !statusControllerOwner(dm, "kagent.dev/v1alpha2", "Agent", name, uid) {
		rows = append(rows, statusWorkloadRow(deployment, drift))
		return rows, drift
	}
	desired, ok := statusInteger(ds["replicas"])
	if !ok {
		return rows, unknown
	}
	ready := statusRollout(deployment, desired)
	rows = append(rows, statusWorkloadRow(deployment, ready))
	selectorSpec := statusMap(ds, "selector")
	selector := statusLabels(statusMap(selectorSpec, "matchLabels"))
	expressions, present := selectorSpec["matchExpressions"]
	if selector == nil || present && expressions != nil && !reflect.DeepEqual(expressions, []any{}) {
		return rows, unknown
	}
	if !kagentLabelsContain(statusLabels(statusMap(statusMap(statusMap(ds, "template"), "metadata"), "labels")), selector) {
		return rows, drift
	}
	service, ok := o.workloadObject("/api/v1", "services", "Service", "v1", name)
	if !ok {
		return rows, unknown
	}
	serviceReady := destinationStatusReadiness{"ready", "Service is controlled by the observed Agent UID and matches its Deployment"}
	ss := statusMap(service, "spec")
	port := false
	if ports, ok := ss["ports"].([]any); ok {
		for _, value := range ports {
			p, ok := value.(map[string]any)
			if ok {
				n, valid := statusInteger(p["port"])
				port = port || valid && n == 8080 && p["protocol"] == "TCP"
			}
		}
	}
	if !statusControllerOwner(statusMap(service, "metadata"), "kagent.dev/v1alpha2", "Agent", name, uid) || !reflect.DeepEqual(selector, statusLabels(statusMap(ss, "selector"))) || !port || statusMap(service, "metadata")["deletionTimestamp"] != nil {
		serviceReady = drift
	}
	rows = append(rows, statusWorkloadRow(service, serviceReady))
	ready = statusWorseReadiness(ready, serviceReady)
	if serviceReady.State != "ready" {
		return rows, ready
	}
	rsRows, complete := o.list("/apis/apps/v1", "replicasets", "ReplicaSet", "apps/v1", kagentSelectorArgument(selector))
	if !complete {
		return rows, unknown
	}
	revision := statusString(statusMap(dm, "annotations"), "deployment.kubernetes.io/revision")
	if revision == "" {
		return rows, statusWorseReadiness(ready, pending)
	}
	fingerprint, err := statusTemplateDigest(deployment)
	if err != nil {
		return rows, unknown
	}
	var current map[string]any
	for _, rs := range rsRows {
		rm := statusMap(rs, "metadata")
		if !statusControllerOwner(rm, "apps/v1", "Deployment", name, statusString(dm, "uid")) || !kagentLabelsContain(statusLabels(statusMap(rm, "labels")), selector) {
			return rows, drift
		}
		if statusString(statusMap(rm, "annotations"), "deployment.kubernetes.io/revision") != revision {
			continue
		}
		spec := statusMap(rs, "spec")
		rsSelector := statusMap(spec, "selector")
		digest, err := statusTemplateDigest(rs)
		if err != nil {
			return rows, unknown
		}
		if current != nil || digest != fingerprint || !kagentLabelsContain(statusLabels(statusMap(rsSelector, "matchLabels")), selector) || rsSelector["matchExpressions"] != nil && !reflect.DeepEqual(rsSelector["matchExpressions"], []any{}) {
			return rows, drift
		}
		current = rs
	}
	if current == nil {
		return rows, statusWorseReadiness(ready, pending)
	}
	rsReady := statusRollout(current, desired)
	rows = append(rows, statusWorkloadRow(current, rsReady))
	ready = statusWorseReadiness(ready, rsReady)
	pods, complete := o.list("/api/v1", "pods", "Pod", "v1", kagentSelectorArgument(selector))
	if !complete {
		return rows, unknown
	}
	rm := statusMap(current, "metadata")
	for _, pod := range pods {
		pm := statusMap(pod, "metadata")
		podReady := statusPodReadiness(pod, ds)
		if !statusControllerOwner(pm, "apps/v1", "ReplicaSet", statusString(rm, "name"), statusString(rm, "uid")) || !kagentLabelsContain(statusLabels(statusMap(pm, "labels")), selector) {
			podReady = drift
		}
		rows = append(rows, statusWorkloadRow(pod, podReady))
		ready = statusWorseReadiness(ready, podReady)
	}
	if int64(len(pods)) != desired {
		ready = statusWorseReadiness(ready, pending)
	}
	// Anchor the observed owner chain after the child observations. Status-only
	// RV churn is allowed; replacement or changed selection/spec is not.
	checks := []struct {
		prefix, plural string
		doc            map[string]any
	}{{"/apis/apps/v1", "deployments", deployment}, {"/api/v1", "services", service}, {"/apis/apps/v1", "replicasets", current}}
	for _, pod := range pods {
		checks = append(checks, struct {
			prefix, plural string
			doc            map[string]any
		}{"/api/v1", "pods", pod})
	}
	for _, check := range checks {
		before := statusMap(check.doc, "metadata")
		after, valid := o.workloadObject(check.prefix, check.plural, statusString(check.doc, "kind"), statusString(check.doc, "apiVersion"), statusString(before, "name"))
		observed := statusMap(after, "metadata")
		valid = valid && reflect.DeepEqual(before["uid"], observed["uid"]) && reflect.DeepEqual(before["generation"], observed["generation"]) && reflect.DeepEqual(check.doc["spec"], after["spec"]) && reflect.DeepEqual(before["ownerReferences"], observed["ownerReferences"]) && reflect.DeepEqual(before["labels"], observed["labels"]) && reflect.DeepEqual(statusMap(before, "annotations")["deployment.kubernetes.io/revision"], statusMap(observed, "annotations")["deployment.kubernetes.io/revision"]) && observed["deletionTimestamp"] == nil
		latest := unknown
		if valid {
			switch check.doc["kind"] {
			case "Deployment", "ReplicaSet":
				latest = statusRollout(after, desired)
			case "Pod":
				latest = statusPodReadiness(after, ds)
			default:
				latest = serviceReady
			}
		}
		ready = statusWorseReadiness(ready, latest)
		for i := range rows {
			if rows[i].Kind == statusString(check.doc, "kind") && rows[i].Name == statusString(before, "name") {
				rows[i].Readiness = statusWorseReadiness(rows[i].Readiness, latest)
			}
		}
	}
	return rows, ready
}

func statusPodReadiness(pod, desired map[string]any) destinationStatusReadiness {
	pending := destinationStatusReadiness{"pending", "owned Pod is not Running and Ready"}
	unknown := destinationStatusReadiness{"unknown", "owned Pod readiness is malformed or indeterminate"}
	meta, spec, status := statusMap(pod, "metadata"), statusMap(pod, "spec"), statusMap(pod, "status")
	if meta["deletionTimestamp"] != nil || statusString(spec, "nodeName") == "" {
		return pending
	}
	if status["phase"] == "Failed" || status["phase"] == "Succeeded" {
		return destinationStatusReadiness{"unhealthy", "owned serving Pod has terminated"}
	}
	if status["phase"] == "Unknown" {
		return unknown
	}
	if status["phase"] != "Running" {
		return pending
	}
	conditions, ok := status["conditions"].([]any)
	if !ok {
		return unknown
	}
	ready, seen := false, false
	for _, value := range conditions {
		c, ok := value.(map[string]any)
		if !ok {
			return unknown
		}
		if c["type"] == "Ready" {
			if seen || c["status"] == "Unknown" {
				return unknown
			}
			seen = true
			ready = c["status"] == "True"
		}
	}
	if !seen || !ready {
		return pending
	}
	wanted := statusContainerImage(statusMap(statusMap(desired, "template"), "spec"))
	if wanted == "" || statusContainerImage(spec) != wanted {
		return destinationStatusReadiness{"drifted", "owned Pod container differs from current Deployment template"}
	}
	containers, ok := status["containerStatuses"].([]any)
	if !ok {
		return unknown
	}
	found := 0
	for _, value := range containers {
		c, ok := value.(map[string]any)
		if !ok {
			return unknown
		}
		if c["name"] == "kagent" {
			found++
			if c["ready"] != true {
				return pending
			}
		}
	}
	if found != 1 {
		return unknown
	}
	return destinationStatusReadiness{"ready", "Pod is Ready and owned by the observed current ReplicaSet UID"}
}
func statusContainerImage(spec map[string]any) string {
	containers, ok := spec["containers"].([]any)
	if !ok {
		return ""
	}
	image, count := "", 0
	for _, value := range containers {
		c, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		if c["name"] == "kagent" {
			count++
			image = statusString(c, "image")
		}
	}
	if count != 1 {
		return ""
	}
	return image
}
