package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

type DiscoveryTarget struct {
	Context      string              `json:"context"`
	Namespace    string              `json:"namespace"`
	ClusterUID   string              `json:"clusterUid,omitempty"`
	NamespaceUID string              `json:"namespaceUid,omitempty"`
	Runtime      *DiscoveryRuntime   `json:"runtime,omitempty"`
	Providers    []DiscoveryProvider `json:"providers"`
	Tools        *OrkaToolInventory  `json:"tools,omitempty"`
}
type DiscoveryRuntime struct {
	Version string `json:"version,omitempty"`
	State   string `json:"state"`
	Reason  string `json:"reason"`
}
type DiscoveryProvider struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	UID            string `json:"uid"`
	Type           string `json:"type"`
	Model          string `json:"model,omitempty"`
	ConditionState string `json:"conditionState"`
}

func (r *DiscoveryReport) targetProblem(scope, state, reason string) {
	r.Complete = false
	for _, c := range r.Checks {
		if c.Scope == scope && c.Reason == reason {
			return
		}
	}
	r.Checks = append(r.Checks, DiscoveryCheck{scope, state, reason})
}
func (a *App) targetGet(ctx context.Context, s *discoveryReadSession, path string) (map[string]any, error) {
	raw, f := s.capture(ctx, a, "get", "--raw", path)
	if f != nil {
		return nil, f
	}
	var m map[string]any
	if !statusDecode(raw, &m) || m == nil {
		return nil, &discoveryReadFailure{reason: "malformed"}
	}
	return m, nil
}
func (a *App) targetNamespace(ctx context.Context, s *discoveryReadSession, name string) (string, error) {
	m, e := a.targetGet(ctx, s, "/api/v1/namespaces/"+name)
	if e != nil {
		return "", e
	}
	meta := statusMap(m, "metadata")
	uid := statusString(meta, "uid")
	if m["apiVersion"] != "v1" || m["kind"] != "Namespace" || meta["name"] != name || !statusOpaque(uid) || meta["deletionTimestamp"] != nil {
		return "", &discoveryReadFailure{reason: "malformed"}
	}
	return uid, nil
}
func targetReadReason(err error) string {
	var f *discoveryReadFailure
	if errors.As(err, &f) {
		if f.reason == "resource_missing" || f.reason == "inventory_limit" || f.reason == "cancelled" {
			return "read_unknown"
		}
		if f.reason == "authentication_required" {
			return "unauthorized"
		}
		return string(f.reason)
	}
	return "read_unknown"
}
func (a *App) discoverTarget(ctx context.Context, opt DiscoveryOptions) (DiscoveryReport, error) {
	r := DiscoveryReport{Scope: "target", Complete: true, Checks: []DiscoveryCheck{}, Rows: []DiscoveryRow{}, Target: &DiscoveryTarget{Context: opt.Context, Namespace: opt.Namespace, Providers: []DiscoveryProvider{}}}
	t := r.Target
	if opt.Context == "" || !statusSafeText(opt.Context) || len(opt.Namespace) > 63 || !discoveryNamespaceName.MatchString(opt.Namespace) || !statusSafeText(opt.Namespace) {
		r.targetProblem("target", "unknown", "malformed")
		return r, NewMachineError("invalid_arguments", "preflight")
	}
	worker, runner := *a, *a.Run
	worker.Cfg = &config.Config{KubeContext: opt.Context}
	worker.Run = &runner
	worker.orkaForwardContext = nil
	s := &discoveryReadSession{context: opt.Context}
	worker.discoveryRead = s
	var e error
	t.ClusterUID, e = worker.targetNamespace(ctx, s, "kube-system")
	if e != nil {
		r.targetProblem("cluster", "unknown", targetReadReason(e))
	}
	t.NamespaceUID, e = worker.targetNamespace(ctx, s, opt.Namespace)
	if e != nil {
		state, reason := "unknown", targetReadReason(e)
		var f *discoveryReadFailure
		if errors.As(e, &f) && f.reason == "resource_missing" {
			api, ae := worker.targetGet(ctx, s, "/api/v1")
			if ae == nil && discoveryAPIHas(api, "v1", "namespaces", "Namespace", false) {
				state, reason = "missing", "resource_missing"
			}
		}
		r.targetProblem("namespace", state, reason)
	}
	if t.ClusterUID != "" && t.NamespaceUID != "" {
		t.Runtime, e = worker.discoveryTargetRuntime(ctx, s, opt.Namespace)
		if e != nil || t.Runtime.State != "available" {
			if e != nil && t.Runtime.State == "unknown" {
				t.Runtime.Reason = targetReadReason(e)
			}
			r.targetProblem("runtime", t.Runtime.State, t.Runtime.Reason)
		}
		t.Providers, e = worker.discoveryTargetProviders(ctx, s, opt.Namespace)
		if e != nil {
			r.targetProblem("providers", "unknown", targetReadReason(e))
		}
		inv, te := worker.ReadOrkaTools(ctx, OrkaToolOptions{Namespace: opt.Namespace})
		t.Tools = &inv
		if te != nil || !inv.Complete {
			reason := inv.Reason
			if reason == "authentication_required" {
				reason = "unauthorized"
			}
			if reason == "inventory_limit" || reason == "cancelled" {
				reason = "read_unknown"
			}
			r.targetProblem("tools", "unknown", reason)
		}
		if s.verifyRuntime != nil {
			if e := s.verifyRuntime(); e != nil {
				t.Runtime.State, t.Runtime.Reason = "unknown", targetReadReason(e)
				r.targetProblem("runtime", "unknown", t.Runtime.Reason)
			}
		}
		cu, ce := worker.targetNamespace(ctx, s, "kube-system")
		nu, ne := worker.targetNamespace(ctx, s, opt.Namespace)
		if ce != nil || ne != nil {
			r.targetProblem("target", "unknown", "read_unknown")
		}
		if (ce == nil && cu != t.ClusterUID) || (ne == nil && nu != t.NamespaceUID) || (inv.Target.ClusterUID != "" && inv.Target.ClusterUID != t.ClusterUID) || (inv.Target.NamespaceUID != "" && inv.Target.NamespaceUID != t.NamespaceUID) {
			t.ClusterUID, t.NamespaceUID = "", ""
			t.Runtime = nil
			t.Providers = []DiscoveryProvider{}
			t.Tools = nil
			r.targetProblem("target", "unknown", "stale_input")
		}
	}
	// Admission covers the composed object, not independently bounded subreports.
	for {
		n := len(t.Providers)
		if t.Tools != nil {
			n += len(t.Tools.Tools)
		}
		candidate := r
		candidate.Checks = append(append([]DiscoveryCheck(nil), r.Checks...), DiscoveryCheck{"target", "unknown", "network_unavailable"})
		raw, _ := json.Marshal(candidate)
		if n <= 128 && len(raw) <= 60<<10 {
			break
		}
		r.targetProblem("target", "unknown", "read_unknown")
		if t.Tools != nil && len(t.Tools.Tools) > 0 {
			t.Tools.Tools = t.Tools.Tools[:len(t.Tools.Tools)-1]
			t.Tools.Complete = false
			t.Tools.Reason = "inventory_limit"
		} else if len(t.Providers) > 0 {
			t.Providers = t.Providers[:len(t.Providers)-1]
		} else {
			break
		}
	}
	if e := ctx.Err(); e != nil {
		r.targetProblem("target", "unknown", "read_unknown")
		return r, &discoveryReadFailure{reason: "read_unknown", cause: e}
	}
	if !r.Complete {
		if s.failure != nil && s.failure.reason != "resource_missing" {
			return r, s.failure
		}
		return r, &discoveryReadFailure{reason: discoveryReadReason(r.Checks[0].Reason)}
	}
	return r, nil
}
func discoveryAPIHas(m map[string]any, group, name, kind string, namespaced bool) bool {
	if m["apiVersion"] != "v1" || m["kind"] != "APIResourceList" || m["groupVersion"] != group {
		return false
	}
	rows, ok := m["resources"].([]any)
	if !ok {
		return false
	}
	count := 0
	for _, v := range rows {
		r, ok := v.(map[string]any)
		if !ok {
			return false
		}
		if r["name"] == name && r["kind"] == kind && r["namespaced"] == namespaced {
			b, _ := json.Marshal(r["verbs"])
			var verbs []string
			if json.Unmarshal(b, &verbs) != nil {
				return false
			}
			v := " " + strings.Join(verbs, " ") + " "
			if strings.Contains(v, " get ") && (!namespaced || strings.Contains(v, " list ")) {
				count++
			}
		}
	}
	return count == 1
}
