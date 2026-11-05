package app

import "encoding/json"

// Keep only literal API keys before typed projection. Unknown fields remain
// forward-compatible, but encoding/json's case folding cannot shadow identity.
func discoveryFields(raw json.RawMessage, keys ...string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return raw
	}
	out := map[string]json.RawMessage{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	b, _ := json.Marshal(out)
	return b
}
func discoveryDecode(raw []byte, out any, shape string) bool {
	var m map[string]json.RawMessage
	if !statusDecode(raw, &m) || m == nil {
		return false
	}
	keep := map[string]json.RawMessage{}
	for _, k := range []string{"apiVersion", "kind", "groupVersion", "metadata", "spec", "status", "items", "resources"} {
		if v, ok := m[k]; ok {
			keep[k] = v
		}
	}
	if v, ok := keep["metadata"]; ok {
		keep["metadata"] = discoveryFields(v, "name", "namespace", "uid", "resourceVersion", "generation", "deletionTimestamp", "labels", "continue")
	}
	if v, ok := keep["status"]; ok {
		keep["status"] = discoveryFields(v, "available")
	}
	if shape == "api" {
		var rows []json.RawMessage
		if json.Unmarshal(m["resources"], &rows) != nil || rows == nil {
			return false
		}
		for i := range rows {
			rows[i] = discoveryFields(rows[i], "name", "kind", "namespaced", "verbs")
		}
		keep["resources"], _ = json.Marshal(rows)
	}
	b, _ := json.Marshal(keep)
	return json.Unmarshal(b, out) == nil
}
func discoveryDeployments(raw []byte) (items []orkaDeployment, cursor, resourceVersion string, ok bool) {
	var m map[string]json.RawMessage
	if !statusDecode(raw, &m) || string(m["apiVersion"]) != `"apps/v1"` || string(m["kind"]) != `"DeploymentList"` {
		return nil, "", "", false
	}
	var meta struct{ Continue, ResourceVersion string }
	if v, present := m["metadata"]; present && json.Unmarshal(discoveryFields(v, "continue", "resourceVersion"), &meta) != nil {
		return nil, "", "", false
	}
	var rows []json.RawMessage
	if json.Unmarshal(m["items"], &rows) != nil || rows == nil {
		return nil, "", "", false
	}
	for _, raw := range rows {
		var d map[string]json.RawMessage
		if json.Unmarshal(raw, &d) != nil || d == nil {
			return nil, "", "", false
		}
		d = map[string]json.RawMessage{"kind": d["kind"], "metadata": discoveryFields(d["metadata"], "name", "namespace", "uid", "generation", "deletionTimestamp", "labels"), "spec": d["spec"], "status": discoveryFields(d["status"], "observedGeneration", "replicas", "updatedReplicas", "readyReplicas", "availableReplicas", "unavailableReplicas")}
		var spec, template, pod map[string]json.RawMessage
		if json.Unmarshal(d["spec"], &spec) != nil || json.Unmarshal(spec["template"], &template) != nil || json.Unmarshal(template["spec"], &pod) != nil {
			return nil, "", "", false
		}
		var containers []json.RawMessage
		if json.Unmarshal(pod["containers"], &containers) != nil {
			return nil, "", "", false
		}
		for i := range containers {
			containers[i] = discoveryFields(containers[i], "name", "image", "command", "args")
		}
		b, _ := json.Marshal(containers)
		pod = map[string]json.RawMessage{"containers": b, "serviceAccountName": pod["serviceAccountName"]}
		b, _ = json.Marshal(pod)
		template = map[string]json.RawMessage{"spec": b}
		b, _ = json.Marshal(template)
		spec = map[string]json.RawMessage{"replicas": spec["replicas"], "template": b}
		d["spec"], _ = json.Marshal(spec)
		b, _ = json.Marshal(d)
		var item orkaDeployment
		if json.Unmarshal(b, &item) != nil {
			return nil, "", "", false
		}
		if item.Kind == "" {
			item.Kind = "Deployment"
		}
		items = append(items, item)
	}
	return items, meta.Continue, meta.ResourceVersion, true
}
