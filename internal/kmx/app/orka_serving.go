package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Reusing an installed Orka waits as long as a fresh Helm install may. After a
// node restart the controller can crash on a not-yet-routable Service network
// before it serves, and container back-off grows with each restart.
const (
	orkaServingTimeout  = 10 * time.Minute
	orkaServingInterval = 2 * time.Second
	orkaServingReadCap  = 15 * time.Second
)

// orkaRefusal marks an observation that waiting cannot change.
type orkaRefusal struct{ error }

func (r orkaRefusal) Unwrap() error { return r.error }

// orkaServingWorkload is one Deployment whose own pods must be shown ready.
type orkaServingWorkload struct {
	name             string
	matchLabels      map[string]string
	matchExpressions int
	replicas         int
	images           map[string]string
}

// orkaServingReader separates the Kubernetes and container-engine reads from
// the readiness rules, so offline and online installs share one check.
// nodeStart is nil when the target has no node container to consult.
type orkaServingReader struct {
	pods      func(selector string) ([]byte, error)
	decode    func([]byte, any) error
	nodeStart func(node string) (time.Time, error)
}

type orkaServingPod struct {
	Metadata struct {
		Name              string            `json:"name"`
		Labels            map[string]string `json:"labels"`
		DeletionTimestamp *string           `json:"deletionTimestamp"`
		OwnerReferences   []struct {
			Kind       string `json:"kind"`
			Name       string `json:"name"`
			Controller *bool  `json:"controller"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		NodeName   string `json:"nodeName"`
		Containers []struct {
			Name  string `json:"name"`
			Image string `json:"image"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		Conditions []struct {
			Type               string     `json:"type"`
			Status             string     `json:"status"`
			LastTransitionTime *time.Time `json:"lastTransitionTime"`
		} `json:"conditions"`
		ContainerStatuses []struct {
			Name  string `json:"name"`
			Ready bool   `json:"ready"`
			State struct {
				Running *struct {
					StartedAt *time.Time `json:"startedAt"`
				} `json:"running"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

// awaitOrkaServing repeats observe until it reports no readiness gap. observe
// returns a non-empty reason for a gap worth waiting on and an error for a
// refusal. Every read it makes is bounded by the context it receives. Nothing
// here writes, restarts or repairs a workload.
func (a *App) awaitOrkaServing(what, retained string, observe func(context.Context) (string, error)) error {
	parent := a.operationContext()
	ctx, cancel := a.waitContext(parent, "orka-serving", orkaServingTimeout)
	defer cancel()
	last := ""
	for {
		pending, err := observe(ctx)
		if cancelled := parent.Err(); cancelled != nil {
			return cancelled
		}
		// A read cut off by the bound says nothing new; keep the last
		// complete observation as the reason.
		if ctx.Err() == nil || last == "" {
			if err != nil {
				return err
			}
			if pending == "" {
				return nil
			}
			if last == "" {
				a.notef("Waiting up to %s for %s to show current readiness.", orkaServingTimeout, what)
			}
			last = pending
		}
		if err := a.pause(ctx, orkaServingInterval); err != nil {
			if cancelled := parent.Err(); cancelled != nil {
				return cancelled
			}
			msg := fmt.Sprintf("%s did not show current readiness within %s (last: %s)", what, orkaServingTimeout, last)
			if retained != "" {
				msg += "; " + retained
			}
			return fmt.Errorf("%s", msg)
		}
	}
}

// check returns "" only when every pod of w reports kubelet readiness that is
// current: Running, Ready and on the Deployment's images, with its Ready
// transition and container starts no older than its node's current start.
//
// Deployment status is written by the controller manager and pod status by
// the kubelet; on a single-node cluster both restart with the node and keep
// their pre-restart values until they resync. The node container's start time
// is the reference neither can carry over. A pod Ready since then was probed
// by its kubelet after the restart, through the pod's current address.
func (r orkaServingReader) check(w orkaServingWorkload) (string, error) {
	selector, err := orkaServingSelector(w)
	if err != nil {
		return "", err
	}
	raw, err := r.pods(selector)
	if errors.As(err, new(orkaRefusal)) {
		return "", err
	}
	if err != nil {
		return fmt.Sprintf("%s pods could not be read", w.name), nil
	}
	var list struct {
		Items []orkaServingPod `json:"items"`
	}
	if r.decode(raw, &list) != nil {
		return "", fmt.Errorf("%s pod list is malformed; refusing to guess readiness", w.name)
	}
	var live []orkaServingPod
	for _, pod := range list.Items {
		if pod.Metadata.DeletionTimestamp != nil || pod.Status.Phase == "Succeeded" || pod.Status.Phase == "Failed" {
			continue
		}
		live = append(live, pod)
	}
	if len(live) != w.replicas {
		return fmt.Sprintf("%s has %d current pods for %d replicas", w.name, len(live), w.replicas), nil
	}
	starts := map[string]time.Time{}
	for _, pod := range live {
		if !orkaServingOwned(w.name, pod) {
			return "", fmt.Errorf("a pod selected by %s is not controlled by its ReplicaSet; refusing to verify a foreign workload", w.name)
		}
		if pod.Status.Phase != "Running" {
			return fmt.Sprintf("%s pod is not running", w.name), nil
		}
		var readySince *time.Time
		for _, c := range pod.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				readySince = c.LastTransitionTime
			}
		}
		if readySince == nil {
			return fmt.Sprintf("%s pod is not Ready", w.name), nil
		}
		// Pod readiness already covers containers an admission webhook added;
		// the Deployment's own containers are matched here.
		started := []time.Time{*readySince}
		matched := 0
		for _, c := range pod.Spec.Containers {
			image, ok := w.images[c.Name]
			if !ok {
				continue
			}
			matched++
			if image != c.Image {
				return fmt.Sprintf("%s pod does not run the Deployment's pinned images", w.name), nil
			}
			ready := false
			for _, s := range pod.Status.ContainerStatuses {
				if s.Name == c.Name && s.Ready && s.State.Running != nil && s.State.Running.StartedAt != nil {
					ready = true
					started = append(started, *s.State.Running.StartedAt)
				}
			}
			if !ready {
				return fmt.Sprintf("%s container %s is not running and ready", w.name, c.Name), nil
			}
		}
		if matched != len(w.images) {
			return fmt.Sprintf("%s pod does not run the Deployment's containers", w.name), nil
		}
		if r.nodeStart == nil {
			continue
		}
		node := pod.Spec.NodeName
		if !orkaDNSName(node) {
			return fmt.Sprintf("%s pod has no valid node", w.name), nil
		}
		boot, seen := starts[node]
		if !seen {
			boot, err = r.nodeStart(node)
			if errors.As(err, new(orkaRefusal)) {
				return "", err
			}
			if err != nil {
				return fmt.Sprintf("the start time of node %s could not be read", node), nil
			}
			starts[node] = boot
		}
		// Kubernetes timestamps have whole-second precision.
		for _, at := range started {
			if at.Before(boot.Truncate(time.Second)) {
				return fmt.Sprintf("%s pod status predates the current start of node %s", w.name, node), nil
			}
		}
	}
	return "", nil
}

func orkaServingSelector(w orkaServingWorkload) (string, error) {
	if w.replicas <= 0 {
		return "", fmt.Errorf("%s is scaled to %d, so nothing is running to verify", w.name, w.replicas)
	}
	if w.matchExpressions != 0 || len(w.matchLabels) == 0 || len(w.images) == 0 {
		return "", fmt.Errorf("%s has no supported label selector or containers; refusing to guess its pods", w.name)
	}
	keys := make([]string, 0, len(w.matchLabels))
	for key, value := range w.matchLabels {
		if !orkaLabelPart(key, true) || !orkaLabelPart(value, false) {
			return "", fmt.Errorf("%s has an unsupported label selector; refusing to guess its pods", w.name)
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+w.matchLabels[key])
	}
	return strings.Join(parts, ","), nil
}

// A Deployment's pods are controlled by a ReplicaSet named after the
// Deployment and the pod's template hash.
func orkaServingOwned(deployment string, pod orkaServingPod) bool {
	hash := pod.Metadata.Labels["pod-template-hash"]
	controllers := 0
	for _, ref := range pod.Metadata.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			controllers++
			if ref.Kind != "ReplicaSet" || hash == "" || ref.Name != deployment+"-"+hash {
				return false
			}
		}
	}
	return controllers == 1 && orkaDNSName(pod.Metadata.Name)
}

// orkaDNSName accepts a Kubernetes object or node name that is also safe as a
// single command argument.
func orkaDNSName(s string) bool {
	if s == "" || len(s) > 253 || s[0] == '-' || s[0] == '.' || s[len(s)-1] == '-' || s[len(s)-1] == '.' {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.')
	}) < 0
}

func orkaLabelPart(s string, key bool) bool {
	if s == "" || len(s) > 316 || strings.HasPrefix(s, "-") {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || key && r == '/')
	}) < 0
}

// offlineCaptureWithin is offlineCapture for reads made while waiting: bounded
// by ctx and a per-call deadline, so a wedged engine or API server cannot hold
// the wait past its bound.
func (a *App) offlineCaptureWithin(ctx context.Context, tool string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, orkaServingReadCap)
	defer cancel()
	prepared := a.Run.Command(tool, args...)
	cmd := exec.CommandContext(callCtx, prepared.Path, prepared.Args[1:]...)
	cmd.Env, cmd.Dir = prepared.Env, prepared.Dir
	cmd.WaitDelay = time.Second
	var out offlineOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if cancelled := ctx.Err(); cancelled != nil {
			return nil, cancelled
		}
		return nil, fmt.Errorf("%s local inspection failed or timed out", tool)
	}
	return out.buffer.Bytes(), nil
}

// kindNodeStartedAt reads when the selected kind cluster's node container last
// started. Kubelet and controller state written before that is from a previous
// run of the node. A container that is not that cluster's node refuses.
func (a *App) kindNodeStartedAt(ctx context.Context, node string) (time.Time, error) {
	raw, err := a.offlineCaptureWithin(ctx, a.Cfg.ContainerEngine, "container", "inspect", node)
	if err != nil {
		return time.Time{}, err
	}
	var rows []struct {
		Name   string
		Config struct{ Labels map[string]string }
		State  struct {
			Running   bool
			StartedAt string
		}
	}
	if offlineJSON(raw, &rows, false) != nil || len(rows) != 1 || strings.TrimPrefix(rows[0].Name, "/") != node || rows[0].Config.Labels["io.x-k8s.kind.cluster"] != a.Cfg.KindCluster {
		return time.Time{}, orkaRefusal{fmt.Errorf("pod node %s is not a %s node container of kind cluster %s; refusing to judge its readiness", node, a.Cfg.ContainerEngine, a.Cfg.KindCluster)}
	}
	if !rows[0].State.Running {
		return time.Time{}, fmt.Errorf("kind node %s is not running", node)
	}
	started, err := time.Parse(time.RFC3339Nano, rows[0].State.StartedAt)
	if err != nil || started.IsZero() {
		return time.Time{}, fmt.Errorf("kind node %s has no start time", node)
	}
	return started, nil
}

// orkaServingObserver is the online install-reuse view: the controller set
// that OrkaReady verifies, still the pinned controller kmx accepted, with
// current pod readiness. On the selected kind cluster that readiness must
// also postdate the node container's current start.
func (a *App) orkaServingObserver() func(context.Context) (string, error) {
	uids := map[string]string{}
	return func(ctx context.Context) (string, error) {
		deployments, err := a.orkaDeployments(ctx)
		if err != nil {
			if strings.Contains(err.Error(), "access forbidden") {
				return "", fmt.Errorf("cannot read Orka Deployments in %s: access forbidden", OrkaNamespace)
			}
			return fmt.Sprintf("Orka Deployments in %s could not be read", OrkaNamespace), nil
		}
		required, err := orkaRequiredDeployments(deployments)
		if err != nil {
			return "", err
		}
		if err := a.orkaPinnedController(required[0]); err != nil {
			return "", err
		}
		for _, d := range required {
			if seen, ok := uids[d.Metadata.Name]; ok && seen != d.Metadata.UID {
				return "", fmt.Errorf("Orka's %s was replaced while kmx waited for it; refusing to adopt the replacement. %s", d.Metadata.Name, a.orkaInstallRecovery())
			}
			uids[d.Metadata.Name] = d.Metadata.UID
			if d.Spec.Replicas <= 0 {
				return "", a.orkaDeploymentReady(d)
			}
			if err := a.orkaDeploymentReady(d); err != nil {
				return err.Error(), nil
			}
		}
		reader := orkaServingReader{
			pods: func(selector string) ([]byte, error) {
				raw, err := a.orkaCapture(ctx, nil, "-n", OrkaNamespace, "get", "pods", "--selector="+selector, "-o", "json")
				if err != nil && strings.Contains(err.Error(), "access forbidden") {
					return nil, orkaRefusal{fmt.Errorf("cannot read Orka pods in %s: access forbidden", OrkaNamespace)}
				}
				return raw, err
			},
			decode: json.Unmarshal,
		}
		if a.Cfg.KindCluster != "" && a.Cfg.KubeContext == "kind-"+a.Cfg.KindCluster {
			if _, err := exec.LookPath(a.Cfg.ContainerEngine); err != nil {
				return "", fmt.Errorf("%s is not on PATH, so the start of kind cluster %s's nodes cannot be compared with Orka's readiness", a.Cfg.ContainerEngine, a.Cfg.KindCluster)
			}
			reader.nodeStart = func(node string) (time.Time, error) { return a.kindNodeStartedAt(ctx, node) }
		}
		for _, d := range required {
			images := map[string]string{}
			for _, c := range d.Spec.Template.Spec.Containers {
				images[c.Name] = c.Image
			}
			pending, err := reader.check(orkaServingWorkload{
				name: d.Metadata.Name, matchLabels: d.Spec.Selector.MatchLabels,
				matchExpressions: len(d.Spec.Selector.MatchExpressions), replicas: int(d.Spec.Replicas), images: images,
			})
			if pending != "" || err != nil {
				return pending, err
			}
		}
		return "", nil
	}
}
