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
	uid := statusString(meta, "uid")
	if !statusOpaque(uid) {
		uid = ""
	}
	return destinationStatusWorkload{Kind: statusString(doc, "kind"), Name: statusString(meta, "name"), UID: uid, Readiness: ready}
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
	terminating, valid := statusTerminating(meta)
	if !valid {
		return unknown
	}
	if terminating {
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
	// Conditions describe the acknowledged rollout, not an earlier generation.
	// Available=False alone is normal during initial progress.
	if raw, present := status["conditions"]; present {
		conditions, valid := raw.([]any)
		if !valid || len(conditions) > 64 {
			return unknown
		}
		seen := map[string]bool{}
		failed := false
		for _, value := range conditions {
			c, valid := value.(map[string]any)
			if !valid {
				return unknown
			}
			typ, typeOK := c["type"].(string)
			state, stateOK := c["status"].(string)
			if !typeOK || typ == "" || seen[typ] || !stateOK || state != "True" && state != "False" && state != "Unknown" {
				return unknown
			}
			seen[typ] = true
			reason, reasonOK := c["reason"].(string)
			if _, present := c["reason"]; present && !reasonOK {
				return unknown
			}
			if (typ == "Progressing" || typ == "ReplicaFailure") && state == "Unknown" {
				return unknown
			}
			failed = failed || typ == "ReplicaFailure" && state == "True" || doc["kind"] == "Deployment" && typ == "Progressing" && state == "False" && reason == "ProgressDeadlineExceeded"
		}
		if failed {
			return destinationStatusReadiness{"unhealthy", "owned workload reports an explicit current-generation rollout failure"}
		}
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
	targetPort := statusServiceTargetPort(ss, statusMap(statusMap(ds, "template"), "spec"))
	terminating, valid := statusTerminating(statusMap(service, "metadata"))
	if !valid {
		serviceReady = unknown
	} else if !statusControllerOwner(statusMap(service, "metadata"), "kagent.dev/v1alpha2", "Agent", name, uid) || !reflect.DeepEqual(selector, statusLabels(statusMap(ss, "selector"))) || targetPort == 0 || terminating {
		serviceReady = drift
	}
	rows = append(rows, statusWorkloadRow(service, serviceReady))
	ready = statusWorseReadiness(ready, serviceReady)
	if serviceReady.State != "ready" {
		return rows, ready
	}
	rsRows, complete := o.list("/apis/apps/v1", "replicasets", "ReplicaSet", "apps/v1", kagentSelectorArgument(selector))
	if !complete {
		for _, rs := range rsRows {
			rows = append(rows, statusWorkloadRow(rs, destinationStatusReadiness{"unknown", "partial ReplicaSet inventory; current owner-chain completeness is unknown"}))
		}
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
		for _, pod := range pods {
			rows = append(rows, statusWorkloadRow(pod, destinationStatusReadiness{"unknown", "partial Pod inventory; current owner-chain completeness is unknown"}))
		}
		return rows, unknown
	}
	rm := statusMap(current, "metadata")
	for _, pod := range pods {
		pm := statusMap(pod, "metadata")
		podReady := statusPodReadiness(pod, ds)
		if podReady.State == "ready" && statusServiceTargetPort(ss, statusMap(pod, "spec")) != targetPort {
			podReady = drift
		}
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

// Resolve Service TCP/8080 against exactly one observed kagent container.
// An omitted target defaults to the Service port; names resolve through TCP
// container declarations. This observes routing only, not a listening endpoint.
func statusServiceTargetPort(service, spec map[string]any) int64 {
	ports, ok := service["ports"].([]any)
	if !ok {
		return 0
	}
	var target any
	found := 0
	for _, value := range ports {
		p, ok := value.(map[string]any)
		if !ok {
			return 0
		}
		n, valid := statusInteger(p["port"])
		if valid && n == 8080 && (p["protocol"] == "TCP" || p["protocol"] == nil) {
			found++
			target = p["targetPort"]
			if _, present := p["targetPort"]; !present {
				target = p["port"]
			}
		}
	}
	if found != 1 {
		return 0
	}
	numeric, numberOK := statusInteger(target)
	name, nameOK := target.(string)
	if !numberOK && (!nameOK || name == "") || numberOK && (numeric < 1 || numeric > 65535) {
		return 0
	}
	containers, ok := spec["containers"].([]any)
	if !ok {
		return 0
	}
	containerCount, matches := 0, 0
	resolved := int64(0)
	for _, value := range containers {
		c, ok := value.(map[string]any)
		if !ok {
			return 0
		}
		if c["name"] != "kagent" {
			continue
		}
		containerCount++
		declared, ok := c["ports"].([]any)
		if !ok {
			return 0
		}
		for _, value := range declared {
			p, ok := value.(map[string]any)
			if !ok {
				return 0
			}
			n, valid := statusInteger(p["containerPort"])
			if !valid || n < 1 || n > 65535 {
				return 0
			}
			if p["protocol"] != nil && p["protocol"] != "TCP" {
				continue
			}
			if numberOK && n == numeric || nameOK && p["name"] == name {
				matches++
				resolved = n
			}
		}
	}
	if containerCount != 1 || matches != 1 {
		return 0
	}
	return resolved
}

func statusPodReadiness(pod, desired map[string]any) destinationStatusReadiness {
	pending := destinationStatusReadiness{"pending", "owned Pod is not Running and Ready"}
	unknown := destinationStatusReadiness{"unknown", "owned Pod readiness is malformed or indeterminate"}
	meta, spec, status := statusMap(pod, "metadata"), statusMap(pod, "spec"), statusMap(pod, "status")
	terminating, valid := statusTerminating(meta)
	if !valid || spec == nil || pod["status"] != nil && status == nil {
		return unknown
	}
	if raw, present := spec["nodeName"]; present {
		if _, ok := raw.(string); !ok {
			return unknown
		}
	}
	if raw, present := status["phase"]; present {
		if _, ok := raw.(string); !ok {
			return unknown
		}
	}
	phase := statusString(status, "phase")
	switch phase {
	case "", "Pending", "Running", "Succeeded", "Failed", "Unknown":
	default:
		return unknown
	}
	if terminating || statusString(spec, "nodeName") == "" {
		return pending
	}
	if phase == "Failed" || phase == "Succeeded" {
		return destinationStatusReadiness{"unhealthy", "owned serving Pod has terminated"}
	}
	if phase == "Unknown" {
		return unknown
	}
	if phase != "Running" {
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
		typ, typeOK := c["type"].(string)
		state, stateOK := c["status"].(string)
		if !typeOK || typ == "" || !stateOK || state != "True" && state != "False" && state != "Unknown" {
			return unknown
		}
		if typ == "Ready" {
			if seen || state == "Unknown" {
				return unknown
			}
			seen = true
			ready = state == "True"
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
			containerReady, valid := c["ready"].(bool)
			if !valid {
				return unknown
			}
			if !containerReady {
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
