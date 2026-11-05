package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

type modelChild struct{ APIVersion, Kind, Name, UID, OwnerUID string }

func modelChildAPI(kind string) (string, string, bool) {
	switch kind {
	case "ReplicaSet":
		return "apps/v1", "/apis/apps/v1", true
	case "Pod":
		return "v1", "/api/v1", true
	}
	return "", "", false
}

// Collect a complete bounded controller-owner closure, never a label-only
// deletion set. A label collision with a different owner refuses cleanup.
func (a *App) modelChildren(ctx context.Context, ns string, root modelOwnedResource) ([]modelChild, error) {
	if root.Kind != "Deployment" {
		return nil, nil
	}
	labels := modelMap(modelMap(root.Spec["selector"])["matchLabels"])
	template := modelMap(modelMap(root.Spec["template"])["spec"])
	if len(labels) == 0 || template == nil {
		return nil, fmt.Errorf("invalid model cleanup projection")
	}
	parents := map[string]modelChild{root.UID: {APIVersion: root.APIVersion, Kind: root.Kind, Name: root.Name, UID: root.UID}}
	var children []modelChild
	for _, kind := range []string{"ReplicaSet", "Pod"} {
		version, prefix, _ := modelChildAPI(kind)
		plural := "replicasets"
		if kind == "Pod" {
			plural = "pods"
		}
		raw, err := a.orkaCapture(ctx, nil, "get", "--raw", prefix+"/namespaces/"+ns+"/"+plural+"?limit=64")
		if err != nil {
			return nil, err
		}
		var list struct {
			APIVersion, Kind string
			Metadata         map[string]any
			Items            []map[string]any
		}
		if modelJSON(raw, &list) != nil || list.APIVersion != version || list.Kind != kind+"List" || list.Metadata == nil || list.Items == nil || len(list.Items) > 64 || (list.Metadata["continue"] != nil && list.Metadata["continue"] != "") {
			return nil, fmt.Errorf("model child closure is incomplete")
		}
		for _, obj := range list.Items {
			meta := modelMap(obj["metadata"])
			name, uid := modelText(meta["name"]), modelText(meta["uid"])
			if !modelResourceName(name) || !verificationIdentity(uid) || meta["namespace"] != ns {
				return nil, fmt.Errorf("model child metadata is invalid")
			}
			matches := true
			for k, v := range labels {
				if modelMap(meta["labels"])[k] != v {
					matches = false
				}
			}
			owners, _ := meta["ownerReferences"].([]any)
			owner := ""
			if len(owners) == 1 {
				ref := modelMap(owners[0])
				parent, ok := parents[modelText(ref["uid"])]
				if ok && ref["controller"] == true && ref["apiVersion"] == parent.APIVersion && ref["kind"] == parent.Kind && ref["name"] == parent.Name {
					if kind == "ReplicaSet" && parent.Kind == "Deployment" || kind == "Pod" && parent.Kind == "ReplicaSet" {
						owner = parent.UID
					}
				}
			}
			if owner == "" {
				if matches {
					return nil, fmt.Errorf("foreign model child")
				}
				continue
			}
			if !matches {
				return nil, fmt.Errorf("model child labels changed")
			}
			spec := modelMap(obj["spec"])
			if kind == "ReplicaSet" {
				spec = modelMap(modelMap(spec["template"])["spec"])
			}
			if kind == "Pod" {
				nodes, e := a.modelGatewayNodes(ctx)
				if e != nil {
					return nil, e
				}
				node := modelText(spec["nodeName"])
				if !slices.Contains(nodes, node) {
					return nil, fmt.Errorf("model Pod placement changed")
				}
				copy := map[string]any{}
				for k, v := range spec {
					if k != "nodeName" {
						copy[k] = v
					}
				}
				spec = copy
			}
			if !modelSpecMatches(template, spec, ".template.spec", "Deployment") {
				return nil, fmt.Errorf("model child execution changed")
			}
			child := modelChild{version, kind, name, uid, owner}
			parents[uid] = child
			children = append(children, child)
		}
	}
	return children, nil
}

// The caller publishes a pending action and complete child identities first.
// This helper sends at most one conditional request, never retries ambiguity.
func (a *App) modelDeleteOwned(ctx context.Context, ns string, root modelOwnedResource, expected ...modelChild) error {
	if root.Purpose != "connection" || root.UID == "" || root.ResourceVersion == "" {
		return fmt.Errorf("invalid model delete authority")
	}
	current, exists, err := a.readModelObject(ctx, ns, root)
	if err != nil {
		return err
	}
	if !exists || current.Terminating {
		return nil
	}
	kids, err := a.modelChildren(ctx, ns, current)
	if err != nil {
		return err
	}
	if !sameModelChildren(expected, kids) {
		return fmt.Errorf("model child closure changed before delete")
	}
	current, exists, err = a.readModelObject(ctx, ns, current)
	if err != nil {
		return err
	}
	if !exists || current.Terminating {
		return nil
	}
	path, err := modelPath(ns, current)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Foreground", "preconditions": map[string]any{"uid": current.UID, "resourceVersion": current.ResourceVersion}})
	raw, err := a.orkaCapture(ctx, body, "delete", "--raw", path, "-f", "-")
	if err != nil {
		return fmt.Errorf("model DELETE acknowledgment unknown")
	}
	var ack map[string]any
	if modelJSON(raw, &ack) != nil {
		return fmt.Errorf("model DELETE acknowledgment invalid")
	}
	if ack["kind"] == "Status" && ack["apiVersion"] == "v1" && ack["status"] == "Success" {
		return nil
	}
	if ack["kind"] == root.Kind && modelMap(ack["metadata"])["uid"] == root.UID {
		return nil
	}
	return fmt.Errorf("model DELETE acknowledgment invalid")
}

// Polling observes only known identities. Even after root disappearance, a
// retained child cannot silently disappear from the closure by losing labels.
func (a *App) modelChildrenGone(ctx context.Context, ns string, children []modelChild) (bool, error) {
	gone := true
	for _, c := range children {
		version, prefix, ok := modelChildAPI(c.Kind)
		if !ok || version != c.APIVersion || !modelResourceName(c.Name) || !verificationIdentity(c.UID) {
			return false, fmt.Errorf("invalid retained child identity")
		}
		plural := "pods"
		if c.Kind == "ReplicaSet" {
			plural = "replicasets"
		}
		resource := plural
		if group, _, grouped := strings.Cut(version, "/"); grouped {
			resource += "." + group
		}
		raw, err := a.orkaCapture(ctx, nil, "-n", ns, "get", resource, c.Name, "--ignore-not-found=true", "-o", "jsonpath={.metadata.uid}")
		if err != nil {
			return false, err
		}
		if len(raw) == 0 {
			continue
		}
		if string(raw) != c.UID {
			return false, fmt.Errorf("retained child identity changed")
		}
		raw, err = a.orkaCapture(ctx, nil, "get", "--raw", prefix+"/namespaces/"+ns+"/"+plural+"/"+c.Name)
		if err != nil {
			return false, err
		}
		var obj map[string]any
		if modelJSON(raw, &obj) != nil || obj["apiVersion"] != version || obj["kind"] != c.Kind {
			return false, fmt.Errorf("retained child read is invalid")
		}
		meta := modelMap(obj["metadata"])
		if meta["uid"] != c.UID || meta["name"] != c.Name || meta["namespace"] != ns {
			return false, fmt.Errorf("retained child identity changed")
		}
		refs, _ := modelMap(obj["metadata"])["ownerReferences"].([]any)
		if len(refs) != 1 || !reflect.DeepEqual(modelMap(refs[0])["uid"], c.OwnerUID) {
			return false, fmt.Errorf("retained child ownership changed")
		}
		gone = false
	}
	return gone, nil
}
