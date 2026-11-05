package app

import (
	"context"
	"encoding/json"
)

func (a *App) discoveryTargetRuntime(ctx context.Context, s *discoveryReadSession, namespace string) (*DiscoveryRuntime, error) {
	r := &DiscoveryRuntime{State: "unknown", Reason: "read_unknown"}
	o := toolObserver{a: a, ctx: ctx}
	items, ok := o.deployments()
	if !ok {
		return r, &discoveryReadFailure{reason: discoveryReadReason(o.reason)}
	}
	if len(items) == 0 {
		r.State, r.Reason = "missing", "resource_missing"
		return r, nil
	}
	controller, _, e := selectOrkaController(items)
	if e != nil {
		return r, e
	}
	profile, e := suiteNativeControllerProfile(controller, namespace)
	if e != nil {
		r.State, r.Reason = "unsupported", "unsupported_runtime"
		return r, e
	}
	r.Version = OrkaVersion
	if _, e = suiteNativeControllerRuntime(controller, namespace); e != nil {
		return r, e
	}
	crds := map[string][]byte{}
	for _, kind := range []string{"Agent", "Provider", "Task"} {
		raw, f := s.capture(ctx, a, "get", "--raw", "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/"+orkaPlural(kind))
		if f != nil {
			return r, f
		}
		var m map[string]any
		if !statusDecode(raw, &m) {
			return r, &discoveryReadFailure{reason: "malformed"}
		}
		crds[kind] = raw
	}
	if _, e = a.installedOrkaSchema(crds); e != nil {
		return r, e
	}
	readWorker := func() (string, error) {
		m, e := a.targetGet(ctx, s, "/api/v1/namespaces/"+OrkaNamespace+"/serviceaccounts/"+profile.WorkerAccount)
		if e != nil {
			return "", e
		}
		meta := statusMap(m, "metadata")
		uid := statusString(meta, "uid")
		if m["apiVersion"] != "v1" || m["kind"] != "ServiceAccount" || meta["name"] != profile.WorkerAccount || meta["namespace"] != OrkaNamespace || !statusOpaque(uid) || meta["deletionTimestamp"] != nil {
			return "", &discoveryReadFailure{reason: "malformed"}
		}
		labels := map[string]string{}
		raw, _ := json.Marshal(meta["labels"])
		if json.Unmarshal(raw, &labels) != nil {
			return "", &discoveryReadFailure{reason: "malformed"}
		}
		return uid, validateOrkaWorkerTrust(profile.WorkerAccount, OrkaNamespace, labels, controller.Metadata.Labels)
	}
	uid, e := readWorker()
	if e != nil {
		return r, e
	}
	s.verifyRuntime = func() error {
		again, ok := o.deployments()
		if !ok {
			return &discoveryReadFailure{reason: discoveryReadReason(o.reason)}
		}
		current, _, e := selectOrkaController(again)
		if e != nil {
			return e
		}
		observed, e := suiteNativeControllerRuntime(current, namespace)
		if e != nil {
			return e
		}
		if observed != profile {
			return &discoveryReadFailure{reason: "stale_input"}
		}
		after, e := readWorker()
		if e != nil {
			return e
		}
		if after != uid {
			return &discoveryReadFailure{reason: "stale_input"}
		}
		return nil
	}
	if e := s.verifyRuntime(); e != nil {
		return r, e
	}
	r.State, r.Reason = "available", "observed"
	return r, nil
}
