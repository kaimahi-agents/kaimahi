package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// Declarative Provider/Agent creation may RESUME: lift and quickstart both
// rerun, and both must reuse what already matches rather than collide with
// it. Tasks never enter this path. Compare server-defaulted desired specs
// without writing anything.
func (a *App) matchingOrkaResource(ctx context.Context, namespace string, desired map[string]any) (*orkaIdentity, error) {
	kind, name := desired["kind"].(string), orkaObjectName(desired)
	if kind != "Provider" && kind != "Agent" {
		return nil, fmt.Errorf("reuse only supports Provider and Agent")
	}
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", orkaPlural(kind), name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var existing map[string]any
	if err = json.Unmarshal(raw, &existing); err != nil {
		return nil, err
	}
	meta, _ := existing["metadata"].(map[string]any)
	uid, _ := meta["uid"].(string)
	version, _ := meta["resourceVersion"].(string)
	generation, _ := meta["generation"].(float64)
	if existing["kind"] != kind || meta["name"] != name || meta["namespace"] != namespace || uid == "" || version == "" || generation < 1 || meta["deletionTimestamp"] != nil {
		return nil, fmt.Errorf("existing %s/%s has invalid or terminating identity", kind, name)
	}
	// Preserve metadata for the dry run; replace only spec. This applies defaults
	// under the installed schema, avoiding false conflicts over omitted defaults.
	candidate := map[string]any{"apiVersion": desired["apiVersion"], "kind": kind, "metadata": meta, "spec": desired["spec"]}
	body, err := json.Marshal(candidate)
	if err != nil {
		return nil, err
	}
	normalized, err := a.orkaCapture(ctx, body, "-n", namespace, "replace", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("cannot validate existing %s/%s for reuse: %w", kind, name, err)
	}
	var admitted map[string]any
	if err = json.Unmarshal(normalized, &admitted); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(existing["spec"], admitted["spec"]) {
		return nil, fmt.Errorf("%s/%s exists with different configuration; select its inference configuration or use a different Agent name", kind, name)
	}
	return &orkaIdentity{Kind: kind, Name: name, UID: uid, Generation: int64(generation)}, nil
}
