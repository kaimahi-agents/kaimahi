package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// The guest runtime must contain exactly the kind nodes whose binds we inspect.
func (a *App) modelGatewayNodes(ctx context.Context) ([]string, error) {
	a = a.withRunContext(ctx)
	raw, err := a.offlineCapture("kind", "get", "nodes", "--name", a.Cfg.KindCluster)
	if err != nil {
		return nil, err
	}
	names, err := offlineNodeNames(raw)
	if err != nil {
		return nil, err
	}
	var list struct {
		APIVersion, Kind string
		Metadata         struct{ Continue string }
		Items            []struct{ Metadata struct{ Name string } }
	}
	observer := toolObserver{a: a, ctx: ctx}
	if !observer.get("/api/v1/nodes?limit=64", &list) || list.APIVersion != "v1" || list.Kind != "NodeList" || list.Metadata.Continue != "" || len(list.Items) == 0 || len(list.Items) > 64 {
		return nil, fmt.Errorf("model node inventory unavailable")
	}
	observed := []string{}
	for _, node := range list.Items {
		observed = append(observed, node.Metadata.Name)
	}
	slices.Sort(observed)
	if !slices.Equal(names, observed) {
		return nil, fmt.Errorf("model node inventory changed")
	}
	return names, nil
}

func modelGatewayRoot(resources []modelOwnedResource) (modelOwnedResource, error) {
	for _, r := range resources {
		if r.Kind == "Deployment" {
			return r, nil
		}
	}
	return modelOwnedResource{}, fmt.Errorf("owned model gateway is unavailable")
}
func mergeModelChildren(a, b []modelChild) []modelChild {
	out := append([]modelChild(nil), a...)
	for _, child := range b {
		if !slices.Contains(out, child) {
			out = append(out, child)
		}
	}
	return out
}
func sameModelChildren(a, b []modelChild) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[modelChild]bool{}
	for _, child := range a {
		if seen[child] || !slices.Contains(b, child) {
			return false
		}
		seen[child] = true
	}
	return true
}

// Wait only for controller/process availability. No inference or CREATE retry.
func (a *App) modelServingGateway(ctx context.Context, root modelOwnedResource) (children []modelChild, err error) {
	ctx, cancel := a.waitContext(ctx, "orka-model-serving-gateway", 30*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			children = nil
			err = ctx.Err()
		}
	}()
	for {
		if current, exists, err := a.readModelObject(ctx, OrkaNamespace, root); err != nil || !exists || current.Terminating {
			return nil, fmt.Errorf("model gateway ownership unavailable")
		}
		kids, err := a.modelChildren(ctx, OrkaNamespace, root)
		if err != nil {
			return nil, err
		}
		ready := false
		for _, child := range kids {
			if child.Kind != "Pod" {
				continue
			}
			raw, e := a.orkaCapture(ctx, nil, "get", "--raw", "/api/v1/namespaces/orka-system/pods/"+child.Name)
			if e != nil {
				return nil, e
			}
			var pod map[string]any
			if modelJSON(raw, &pod) != nil || pod["kind"] != "Pod" || pod["apiVersion"] != "v1" || modelMap(pod["metadata"])["uid"] != child.UID || modelMap(pod["metadata"])["deletionTimestamp"] != nil {
				return nil, fmt.Errorf("model serving Pod changed")
			}
			if modelText(modelMap(pod["spec"])["nodeName"]) == "" {
				continue
			}
			status := modelMap(pod["status"])
			conditions, _ := status["conditions"].([]any)
			for _, c := range conditions {
				m := modelMap(c)
				if status["phase"] == "Running" && m["type"] == "Ready" && m["status"] == "True" {
					ready = true
				}
			}
		}
		if ready {
			return kids, nil
		}
		if err := a.pause(ctx, 100*time.Millisecond); err != nil {
			return nil, err
		}
	}
}

// API disappearance is not process death after node loss or a forced deletion.
// CRI ListContainers is read-only and contains no container environment; never inspect.
func (a *App) modelGatewayStopped(ctx context.Context, root modelOwnedResource, children []modelChild) error {
	a = a.withRunContext(ctx)
	if err := a.checkModelNodeMounts(); err != nil {
		return err
	}
	nodes, err := a.modelGatewayNodes(ctx)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		raw, err := a.offlineCapture("docker", "container", "inspect", node)
		if err != nil {
			return err
		}
		var rows []struct{ State struct{ Running *bool } }
		if offlineJSON(raw, &rows, false) != nil || len(rows) != 1 || rows[0].State.Running == nil {
			return fmt.Errorf("model node state unavailable")
		}
		if !*rows[0].State.Running {
			continue
		}
		raw, err = a.offlineCapture("docker", "exec", node, "crictl", "ps", "-a", "-o", "json")
		if err != nil {
			return err
		}
		var list map[string]any
		if modelJSON(raw, &list) != nil {
			return fmt.Errorf("model process inventory unavailable")
		}
		containers, ok := list["containers"].([]any)
		if !ok || len(containers) > 4096 {
			return fmt.Errorf("model process inventory unavailable")
		}
		for _, value := range containers {
			c := modelMap(value)
			labels := modelMap(c["labels"])
			uid := modelText(labels["io.kubernetes.pod.uid"])
			if !verificationIdentity(uid) || !modelResourceName(modelText(labels["io.kubernetes.pod.namespace"])) || scaffold.ValidateObjectName(modelText(labels["io.kubernetes.pod.name"])) != nil {
				return fmt.Errorf("model process identity unavailable")
			}
			switch c["state"] {
			case "CONTAINER_CREATED", "CONTAINER_RUNNING", "CONTAINER_EXITED", "CONTAINER_UNKNOWN":
			default:
				return fmt.Errorf("model process state unavailable")
			}
			ours := false
			for _, child := range children {
				if child.Kind == "Pod" && uid == child.UID {
					ours = true
				}
			}
			if labels["io.kubernetes.pod.namespace"] == OrkaNamespace && (uid == "" || strings.HasPrefix(modelText(labels["io.kubernetes.pod.name"]), root.Name+"-")) {
				ours = true
			}
			if ours && c["state"] != "CONTAINER_EXITED" {
				return fmt.Errorf("model credential process stop is unknown")
			}
		}
	}
	return nil
}

func (a *App) modelCheckGateway(ctx context.Context, connection OrkaModelConnection, roots []modelOwnedResource) ([]modelChild, error) {
	root, err := modelGatewayRoot(roots)
	if err != nil {
		return nil, err
	}
	kids, err := a.modelServingGateway(ctx, root)
	if err != nil {
		return nil, err
	}
	if err := a.modelHealth(ctx, connection); err != nil {
		return nil, err
	}
	for _, r := range roots {
		if current, present, e := a.readModelObject(ctx, OrkaNamespace, r); e != nil || !present || current.Terminating {
			return nil, fmt.Errorf("model ownership changed during readiness")
		}
	}
	after, err := a.modelChildren(ctx, OrkaNamespace, root)
	if err != nil || !sameModelChildren(kids, after) {
		return nil, fmt.Errorf("model serving closure changed")
	}
	return kids, nil
}

func (a *App) modelHealth(ctx context.Context, connection OrkaModelConnection) error {
	path := "/api/v1/namespaces/orka-system/services/http:" + connection.Provider + "-vekil:1337/proxy/"
	raw, err := a.orkaCapture(ctx, nil, "get", "--raw", path+"readyz")
	if err != nil {
		return fmt.Errorf("model authenticated readiness unavailable")
	}
	var ready map[string]any
	if modelJSON(raw, &ready) != nil || ready["status"] != "ready" {
		return fmt.Errorf("model authenticated readiness unavailable")
	}
	raw, err = a.orkaCapture(ctx, nil, "get", "--raw", path+"v1/models")
	if err != nil {
		return fmt.Errorf("model catalog unavailable")
	}
	var catalog map[string]any
	if modelJSON(raw, &catalog) != nil {
		return fmt.Errorf("model catalog unavailable")
	}
	data, ok := catalog["data"].([]any)
	if !ok || len(data) == 0 || len(data) > 4096 {
		return fmt.Errorf("model catalog unavailable")
	}
	found := 0
	for _, model := range data {
		id := modelText(modelMap(model)["id"])
		if !verificationIdentity(id) {
			return fmt.Errorf("model catalog unavailable")
		}
		if id == connection.Model {
			found++
		}
	}
	if found != 1 {
		return fmt.Errorf("selected model is not advertised")
	}
	return nil
}
