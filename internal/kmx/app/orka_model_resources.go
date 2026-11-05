package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"
)

const modelOperationAnnotation = "kaimahi.dev/orka-model-operation"

type modelAPI struct {
	version, plural, prefix string
	generation              bool
}

func modelAPIFor(kind string) (modelAPI, bool) {
	table := map[string]modelAPI{
		"Deployment": {"apps/v1", "deployments", "/apis/apps/v1", true},
		"Service":    {"v1", "services", "/api/v1", false}, "Secret": {"v1", "secrets", "/api/v1", false},
		"Provider": {"core.orka.ai/v1alpha1", "providers", "/apis/core.orka.ai/v1alpha1", true},
		"Agent":    {"core.orka.ai/v1alpha1", "agents", "/apis/core.orka.ai/v1alpha1", true},
		"Task":     {"core.orka.ai/v1alpha1", "tasks", "/apis/core.orka.ai/v1alpha1", true},
	}
	v, ok := table[kind]
	return v, ok
}
func modelPath(ns string, r modelOwnedResource) (string, error) {
	api, ok := modelAPIFor(r.Kind)
	if !ok || api.version != r.APIVersion || ns != OrkaNamespace || !modelResourceName(r.Name) {
		return "", fmt.Errorf("invalid model resource")
	}
	return api.prefix + "/namespaces/" + ns + "/" + api.plural + "/" + r.Name, nil
}
func modelResourceName(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for i, c := range value {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if c != '-' || i == 0 || i == len(value)-1 {
			return false
		}
	}
	return true
}
func modelJSON(raw []byte, out any) error { return decodeModelState(bytes.NewReader(raw), out) }
func modelMap(value any) map[string]any   { m, _ := value.(map[string]any); return m }
func modelText(value any) string          { s, _ := value.(string); return s }
func modelExecutionSpec(kind string, obj map[string]any) map[string]any {
	if kind == "Secret" {
		m := map[string]any{"type": obj["type"], "data": obj["data"]}
		if v, ok := obj["immutable"]; ok {
			m["immutable"] = v
		}
		return m
	}
	return modelMap(obj["spec"])
}

// Only declared execution fields and known API defaults may enter a receipt.
// This rejects injected containers, env, storage, service accounts and policy.
func modelSpecMatches(want, got any, path string, kind string) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range w {
			if !modelSpecMatches(v, g[k], path+"."+k, kind) {
				return false
			}
		}
		for k, v := range g {
			if _, ok := w[k]; !ok && !modelDefault(path+"."+k, v, kind) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(w) != len(g) {
			return false
		}
		for i := range w {
			if !modelSpecMatches(w[i], g[i], path+"[]", kind) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(w, got)
	}
}
func modelDefault(path string, value any, kind string) bool {
	defaults := map[string]any{
		".template.metadata.creationTimestamp": nil,
		".template.spec.serviceAccount":        "default",
		".progressDeadlineSeconds":             float64(600), ".revisionHistoryLimit": float64(10),
		".template.spec.restartPolicy": "Always", ".template.spec.dnsPolicy": "ClusterFirst", ".template.spec.schedulerName": "default-scheduler", ".template.spec.terminationGracePeriodSeconds": float64(30),
		".sessionAffinity": "None", ".type": "ClusterIP", ".internalTrafficPolicy": "Cluster", ".ipFamilyPolicy": "SingleStack",
	}
	if v, ok := defaults[path]; ok {
		return reflect.DeepEqual(v, value)
	}
	for _, prefix := range []string{".template.spec.containers[]", ".template.spec.initContainers[]"} {
		if path == prefix+".resources" {
			return reflect.DeepEqual(value, map[string]any{})
		}
		if path == prefix+".terminationMessagePath" {
			return value == "/dev/termination-log"
		}
		if path == prefix+".terminationMessagePolicy" {
			return value == "File"
		}
	}
	if kind == "Service" {
		if path == ".clusterIP" {
			return net.ParseIP(modelText(value)) != nil
		}
		if path == ".clusterIPs" {
			v, ok := value.([]any)
			return ok && len(v) == 1 && net.ParseIP(modelText(v[0])) != nil
		}
		if path == ".ipFamilies" {
			v, ok := value.([]any)
			return ok && len(v) == 1 && (v[0] == "IPv4" || v[0] == "IPv6")
		}
	}
	return false
}
func modelObserved(ns string, want modelOwnedResource, obj map[string]any) (modelOwnedResource, error) {
	fail := fmt.Errorf("model resource acknowledgment is invalid")
	api, ok := modelAPIFor(want.Kind)
	meta := modelMap(obj["metadata"])
	if !ok || obj["apiVersion"] != api.version || obj["kind"] != want.Kind || meta["name"] != want.Name || meta["namespace"] != ns {
		return modelOwnedResource{}, fail
	}
	uid, rv := modelText(meta["uid"]), modelText(meta["resourceVersion"])
	if !verificationIdentity(uid) || !verificationIdentity(rv) || want.UID != "" && want.UID != uid || modelMap(meta["annotations"])[modelOperationAnnotation] != want.Marker {
		return modelOwnedResource{}, fail
	}
	generation, _ := meta["generation"].(float64)
	if api.generation && (generation < 1 || generation != float64(int64(generation))) {
		return modelOwnedResource{}, fail
	}
	spec := modelExecutionSpec(want.Kind, obj)
	if spec == nil || modelStateHasCredentialShape(spec) || !modelSpecMatches(want.Spec, spec, "", want.Kind) {
		return modelOwnedResource{}, fail
	}
	if want.UID != "" && !reflect.DeepEqual(want.Spec, spec) {
		return modelOwnedResource{}, fail
	}
	if want.UID != "" && want.Generation > 0 && want.Generation != int64(generation) {
		return modelOwnedResource{}, fail
	}
	want.Terminating = meta["deletionTimestamp"] != nil
	want.UID, want.ResourceVersion, want.Generation, want.Spec = uid, rv, int64(generation), spec
	return want, nil
}
func modelDesired(doc map[string]any) (modelOwnedResource, error) {
	kind := modelText(doc["kind"])
	api, ok := modelAPIFor(kind)
	meta := modelMap(doc["metadata"])
	r := modelOwnedResource{APIVersion: api.version, Kind: kind, Name: modelText(meta["name"]), Marker: modelText(modelMap(meta["annotations"])[modelOperationAnnotation]), Purpose: "connection", Spec: modelExecutionSpec(kind, doc)}
	if !ok || doc["apiVersion"] != api.version || meta["namespace"] != OrkaNamespace || !modelResourceName(r.Name) || !modelUUID(r.Marker) || r.Spec == nil || modelStateHasCredentialShape(r.Spec) {
		return r, fmt.Errorf("invalid desired model resource")
	}
	if kind == "Agent" || kind == "Task" {
		r.Purpose = "proof"
	}
	if kind == "Secret" {
		// The unchanged document is sent, so reject write fields omitted by the
		// retained projection: stringData would override even a checked data value.
		for key := range doc {
			switch key {
			case "apiVersion", "kind", "metadata", "type", "data", "immutable":
			default:
				return r, fmt.Errorf("model Secret contains an unsupported field")
			}
		}
		data := modelMap(r.Spec["data"])
		if r.Spec["type"] != "Opaque" || len(data) != 1 || data["api-key"] != "bm90LXVzZWQtYnktdGhpcy1lbmRwb2ludA==" {
			return r, fmt.Errorf("model Secret must be the fixed noncredential placeholder")
		}
	}
	return r, nil
}
func (a *App) createModelObjectObserved(ctx context.Context, ns string, doc map[string]any) (modelOwnedResource, error) {
	// Round trip once so comparisons use the same JSON numeric representation.
	raw, err := json.Marshal(doc)
	if err != nil {
		return modelOwnedResource{}, fmt.Errorf("invalid desired model resource")
	}
	var normalized map[string]any
	if modelJSON(raw, &normalized) != nil {
		return modelOwnedResource{}, fmt.Errorf("invalid desired model resource")
	}
	want, err := modelDesired(normalized)
	if err != nil {
		return want, err
	}
	if _, err = modelPath(ns, want); err != nil {
		return want, err
	}
	if err = a.annotateCreatedByVersion(normalized); err != nil {
		return want, fmt.Errorf("model version unavailable")
	}
	raw, _ = json.Marshal(normalized)
	reply, err := a.orkaCapture(ctx, raw, "-n", ns, "create", "-f", "-", "-o", "json")
	if err != nil {
		return want, fmt.Errorf("model CREATE acknowledgment unknown")
	}
	var observed map[string]any
	if modelJSON(reply, &observed) != nil {
		return want, fmt.Errorf("model CREATE acknowledgment invalid")
	}
	if modelMap(observed["metadata"])["deletionTimestamp"] != nil {
		return want, fmt.Errorf("model CREATE acknowledgment terminating")
	}
	return modelObserved(ns, want, observed)
}
func (a *App) readModelObject(ctx context.Context, ns string, want modelOwnedResource) (modelOwnedResource, bool, error) {
	// Observation cannot promote a pending CREATE into acknowledged ownership.
	if !verificationIdentity(want.UID) || !verificationIdentity(want.ResourceVersion) {
		return want, false, fmt.Errorf("model read requires retained acknowledgment")
	}
	path, err := modelPath(ns, want)
	if err != nil {
		return want, false, err
	}
	// A successful explicit presence query distinguishes absence from read failure.
	api, _ := modelAPIFor(want.Kind)
	resource := api.plural
	if group, _, grouped := strings.Cut(api.version, "/"); grouped {
		resource += "." + group
	}
	raw, err := a.orkaCapture(ctx, nil, "-n", ns, "get", resource, want.Name, "--ignore-not-found=true", "-o", "jsonpath={.metadata.uid}")
	if err != nil {
		return want, false, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return want, false, nil
	}
	if !verificationIdentity(string(raw)) || want.UID != "" && string(raw) != want.UID {
		return want, false, fmt.Errorf("model presence identity changed")
	}
	raw, err = a.orkaCapture(ctx, nil, "get", "--raw", path)
	if err != nil {
		return want, false, err
	}
	var observed map[string]any
	if modelJSON(raw, &observed) != nil {
		return want, false, fmt.Errorf("invalid model read")
	}
	r, err := modelObserved(ns, want, observed)
	return r, true, err
}
