package app

import (
	"context"
	"math"
	"net/url"
	"sort"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/conditionfacts"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

func discoveryProviderCondition(m map[string]any, generation float64) (string, bool) {
	if m["status"] == nil {
		return "Unknown", true
	}
	status, ok := m["status"].(map[string]any)
	if !ok {
		return "", false
	}
	ready, rOK := status["ready"].(bool)
	if _, present := status["ready"]; present && !rOK {
		return "", false
	}
	facts := conditionfacts.ReadMap(status)
	if facts.Shape() == conditionfacts.Invalid || facts.HasDuplicateTypes() {
		return "", false
	}
	answer := "Unknown"
	for _, c := range facts.Entries() {
		if !c.Object || !c.TypeValid || !c.StateValid || !c.ObservedValid {
			return "", false
		}
		if c.Type == "Ready" && c.ObservedPresent && float64(c.ObservedGeneration) == generation && rOK {
			if c.State == "True" && ready {
				answer = "True"
			}
			if c.State == "False" && !ready {
				answer = "False"
			}
		}
	}
	return answer, true
}
func (a *App) discoveryTargetProviders(ctx context.Context, s *discoveryReadSession, namespace string) ([]DiscoveryProvider, error) {
	rows := []DiscoveryProvider{}
	defer func() { sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name }) }()
	bad := error(nil)
	api, e := a.targetGet(ctx, s, "/apis/core.orka.ai/v1alpha1")
	if e != nil {
		return rows, e
	}
	if !discoveryAPIHas(api, "core.orka.ai/v1alpha1", "providers", "Provider", true) {
		return rows, &discoveryReadFailure{reason: "read_unknown"}
	}
	cursor, version := "", ""
	seen, ids, cursors := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for page := 0; page < 32; page++ {
		path := "/apis/core.orka.ai/v1alpha1/namespaces/" + namespace + "/providers?limit=64"
		if cursor != "" {
			path += "&continue=" + url.QueryEscape(cursor)
		}
		m, e := a.targetGet(ctx, s, path)
		if e != nil {
			return rows, e
		}
		if m["apiVersion"] != "core.orka.ai/v1alpha1" || m["kind"] != "ProviderList" {
			return rows, &discoveryReadFailure{reason: "malformed"}
		}
		meta, ok := m["metadata"].(map[string]any)
		if !ok {
			return rows, &discoveryReadFailure{reason: "malformed"}
		}
		for _, key := range []string{"resourceVersion", "continue"} {
			if v, present := meta[key]; present {
				if _, ok := v.(string); !ok {
					return rows, &discoveryReadFailure{reason: "malformed"}
				}
			}
		}
		rv, next := statusString(meta, "resourceVersion"), statusString(meta, "continue")
		if page > 0 && (version == "" || version != rv) {
			return rows, &discoveryReadFailure{reason: "read_unknown"}
		}
		version = rv
		entries, ok := m["items"].([]any)
		if !ok {
			return rows, &discoveryReadFailure{reason: "malformed"}
		}
		for _, v := range entries {
			obj, ok := v.(map[string]any)
			meta := statusMap(obj, "metadata")
			spec := statusMap(obj, "spec")
			name, uid := statusString(meta, "name"), statusString(meta, "uid")
			typ, model := statusString(spec, "type"), statusString(spec, "defaultModel")
			gen, gOK := meta["generation"].(float64)
			condition, cOK := discoveryProviderCondition(obj, gen)
			if v, present := spec["defaultModel"]; present {
				if _, ok := v.(string); !ok {
					cOK = false
				}
			}
			if !ok || scaffold.ValidateObjectName(name) != nil || !statusSafeText(name) || meta["namespace"] != namespace || !statusOpaque(uid) || !statusSafeText(typ) || !gOK || gen < 1 || gen > 9007199254740991 || math.Trunc(gen) != gen || !cOK || seen[name] || ids[uid] || (model != "" && !statusSafeText(model)) || (obj["kind"] != nil && obj["kind"] != "Provider") || (obj["apiVersion"] != nil && obj["apiVersion"] != "core.orka.ai/v1alpha1") {
				bad = &discoveryReadFailure{reason: "malformed"}
				continue
			}
			seen[name], ids[uid] = true, true
			if meta["deletionTimestamp"] != nil {
				condition = "Unknown"
			}
			if len(rows) >= 128 {
				return rows, &discoveryReadFailure{reason: "read_unknown"}
			}
			rows = append(rows, DiscoveryProvider{Name: name, Namespace: namespace, UID: uid, Type: typ, Model: model, ConditionState: condition})
		}
		if next == "" {
			return rows, bad
		}
		if len(next) > 4096 || rv == "" || cursors[next] {
			return rows, &discoveryReadFailure{reason: "read_unknown"}
		}
		cursors[next] = true
		cursor = next
	}
	return rows, &discoveryReadFailure{reason: "read_unknown"}
}
