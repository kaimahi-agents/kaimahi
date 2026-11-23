package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"reflect"
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
// revision and template identify its current ReplicaSet.
type orkaServingWorkload struct {
	name, uid        string
	revision         string
	template         any
	matchLabels      map[string]string
	matchExpressions int
	replicas         int
	images           map[string]string
}

// orkaRevisionAnnotation is the rollout revision the Deployment controller
// records on a Deployment and on each of its ReplicaSets.
const orkaRevisionAnnotation = "deployment.kubernetes.io/revision"

// orkaTemplate decodes a pod template's spec as S and also keeps the whole
// template, so a ReplicaSet's template can be compared with its Deployment's.
type orkaTemplate[S any] struct {
	Spec  S `json:"spec"`
	whole any
}

func (t *orkaTemplate[S]) UnmarshalJSON(raw []byte) error {
	var typed struct {
		Spec S `json:"spec"`
	}
	if err := json.Unmarshal(raw, &typed); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var whole any
	if err := dec.Decode(&whole); err != nil {
		return err
	}
	t.Spec, t.whole = typed.Spec, whole
	return nil
}

// orkaSameTemplate compares two pod templates as the Deployment controller
// does, ignoring the pod-template-hash label it adds to its ReplicaSets.
func orkaSameTemplate(deployment, replicaSet any) bool {
	strip := func(template any) any {
		object, ok := template.(map[string]any)
		if !ok {
			return template
		}
		meta, ok := object["metadata"].(map[string]any)
		if !ok {
			return template
		}
		labels, ok := meta["labels"].(map[string]any)
		if !ok {
			return template
		}
		labels = maps.Clone(labels)
		delete(labels, "pod-template-hash")
		meta, object = maps.Clone(meta), maps.Clone(object)
		meta["labels"], object["metadata"] = labels, meta
		return object
	}
	return deployment != nil && reflect.DeepEqual(strip(deployment), strip(replicaSet))
}

// orkaServingReader separates the Kubernetes and container-engine reads from
// the readiness rules, so offline and online installs share one check.
// replicaSets lists every ReplicaSet in the namespace: a selected pod's owner
// need not carry the Deployment's labels itself. nodeStart is nil when the
// target has no node container to consult.
type orkaServingReader struct {
	pods        func(selector string) ([]byte, error)
	replicaSets func() ([]byte, error)
	services    func() ([]byte, error)
	get         func(path string) ([]byte, error)
	decode      func([]byte, any) error
	nodeStart   func(node string) (time.Time, error)
}

// orkaOwnerRef is the part of an ownerReference that identifies its owner.
type orkaOwnerRef struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Controller *bool  `json:"controller"`
}

type orkaServingReplicaSet struct {
	Metadata struct {
		Name              string            `json:"name"`
		UID               string            `json:"uid"`
		Annotations       map[string]string `json:"annotations"`
		DeletionTimestamp *string           `json:"deletionTimestamp"`
		OwnerReferences   []orkaOwnerRef    `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		Template orkaTemplate[struct{}] `json:"template"`
	} `json:"spec"`
}

// orkaControllerRef returns the single controller reference, if there is exactly one.
func orkaControllerRef(refs []orkaOwnerRef) (orkaOwnerRef, bool) {
	var found orkaOwnerRef
	count := 0
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			found = ref
			count++
		}
	}
	return found, count == 1
}

type orkaServingPod struct {
	Metadata struct {
		Name              string            `json:"name"`
		Labels            map[string]string `json:"labels"`
		DeletionTimestamp *string           `json:"deletionTimestamp"`
		OwnerReferences   []orkaOwnerRef    `json:"ownerReferences"`
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
			Type   string `json:"type"`
			Status string `json:"status"`
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
		// A complete observation without a gap is current readiness even if
		// the bound passed just after its last read.
		if pending == "" && err == nil {
			return nil
		}
		// A read cut off by the bound says nothing new; keep the last
		// complete observation as the reason.
		if ctx.Err() == nil || last == "" {
			if err != nil {
				return err
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
// current: Running, Ready and on the Deployment's images, created from its
// current ReplicaSet, with its containers started no earlier than its node's
// current start.
//
// Deployment status is written by the controller manager and pod status by
// the kubelet; on a single-node cluster both restart with the node and keep
// their pre-restart values until they resync. The node container's start time
// is the reference neither can carry over. A node restart stops every
// container on it, so a container started since then was started by the
// restarted kubelet, and the readiness that kubelet reports for it comes from
// probing that container. The Ready condition's transition time is not
// compared: it changes only when Ready changes, not on each probe.
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
	// Read after the pods: a pod's ReplicaSet exists before the pod does.
	sets, pending, err := r.ownedReplicaSets(w)
	if pending != "" || err != nil {
		return pending, err
	}
	// A foreign pod will not go away by waiting, so every pod's owner is
	// checked before any of them is waited on.
	gap := ""
	for _, pod := range live {
		owner, ok := orkaControllerRef(pod.Metadata.OwnerReferences)
		if !ok || owner.Kind != "ReplicaSet" || owner.UID == "" || !orkaDNSName(pod.Metadata.Name) {
			return "", fmt.Errorf("a pod selected by %s is not controlled by its ReplicaSet; refusing to verify a foreign workload", w.name)
		}
		reason := ""
		switch sets[owner.UID] {
		case "foreign":
			return "", fmt.Errorf("a pod selected by %s belongs to a ReplicaSet that %s does not control; refusing to verify a foreign workload", w.name, w.name)
		case "":
			reason = fmt.Sprintf("%s pod's ReplicaSet was not observed", w.name)
		case "deleting":
			reason = fmt.Sprintf("%s pod's ReplicaSet is being deleted", w.name)
		case "earlier":
			reason = fmt.Sprintf("%s pod is from a ReplicaSet that is not its current revision and pod template", w.name)
			if w.revision == "" {
				reason = fmt.Sprintf("%s has no rollout revision recorded yet", w.name)
			}
		}
		if gap == "" {
			gap = reason
		}
	}
	if gap != "" {
		return gap, nil
	}
	if len(live) != w.replicas {
		return fmt.Sprintf("%s has %d current pods for %d replicas", w.name, len(live), w.replicas), nil
	}
	starts := map[string]time.Time{}
	for _, pod := range live {
		if pod.Status.Phase != "Running" {
			return fmt.Sprintf("%s pod is not running", w.name), nil
		}
		podReady := false
		for _, c := range pod.Status.Conditions {
			if c.Type == "Ready" {
				podReady = c.Status == "True"
			}
		}
		if !podReady {
			return fmt.Sprintf("%s pod is not Ready", w.name), nil
		}
		// Pod readiness already covers containers an admission webhook added;
		// the Deployment's own containers are matched here.
		var started []time.Time
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

// ownedReplicaSets maps each observed ReplicaSet UID to "current", "earlier",
// "deleting" or "foreign". Ownership is by the Deployment's UID: a ReplicaSet
// name and template hash do not establish it. Of the ReplicaSets it owns, only
// one at the Deployment's revision with the Deployment's pod template is
// current; pods of an earlier one can share its images and still run older
// arguments or environment.
func (r orkaServingReader) ownedReplicaSets(w orkaServingWorkload) (map[string]string, string, error) {
	if w.uid == "" {
		return nil, "", fmt.Errorf("%s has no UID; refusing to guess its pods' owner", w.name)
	}
	raw, err := r.replicaSets()
	if errors.As(err, new(orkaRefusal)) {
		return nil, "", err
	}
	if err != nil {
		return nil, fmt.Sprintf("%s ReplicaSets could not be read", w.name), nil
	}
	var list struct {
		Items []orkaServingReplicaSet `json:"items"`
	}
	if r.decode(raw, &list) != nil {
		return nil, "", fmt.Errorf("%s ReplicaSet list is malformed; refusing to guess readiness", w.name)
	}
	sets := map[string]string{}
	for _, set := range list.Items {
		if set.Metadata.UID == "" {
			return nil, "", fmt.Errorf("%s ReplicaSet list has an entry without a UID; refusing to guess readiness", w.name)
		}
		owner, ok := orkaControllerRef(set.Metadata.OwnerReferences)
		switch {
		case !ok || owner.Kind != "Deployment" || owner.UID != w.uid:
			sets[set.Metadata.UID] = "foreign"
		case set.Metadata.DeletionTimestamp != nil:
			sets[set.Metadata.UID] = "deleting"
		case w.revision == "" || set.Metadata.Annotations[orkaRevisionAnnotation] != w.revision || !orkaSameTemplate(w.template, set.Spec.Template.whole):
			sets[set.Metadata.UID] = "earlier"
		default:
			sets[set.Metadata.UID] = "current"
		}
	}
	return sets, "", nil
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

// apiServing returns "" when Orka's API answers its own typed /readyz through
// its Service, the route clients use. A Ready controller pod shows only that
// its health port answered, and Service endpoints can still name a previous
// run's pod address, where another workload may answer. The Service must
// select only pods of one of the checked Deployments (its selector contains
// that Deployment's whole selector), so whatever answers is a pod the wait
// has already held to current ownership and readiness.
func (r orkaServingReader) apiServing(checked []map[string]string) (string, error) {
	raw, err := r.services()
	if errors.As(err, new(orkaRefusal)) {
		return "", err
	}
	if err != nil {
		return fmt.Sprintf("Orka Services in %s could not be read", OrkaNamespace), nil
	}
	var list orkaServiceList
	if r.decode(raw, &list) != nil {
		return "", fmt.Errorf("Orka Service list in %s is malformed; refusing to guess readiness", OrkaNamespace)
	}
	name, port, err := selectOrkaAPIService(list, OrkaNamespace)
	if err != nil {
		return "", fmt.Errorf("cannot identify Orka's API Service: %w", err)
	}
	if !orkaDNSName(name) || !orkaDNSName(port) {
		return "", fmt.Errorf("Orka's API Service in %s has an unsupported name or port name", OrkaNamespace)
	}
	if !orkaServiceSelectsChecked(list, name, checked) {
		return "", fmt.Errorf("Orka's API Service %s/%s can select pods outside the checked Deployments; refusing to judge readiness through it", OrkaNamespace, name)
	}
	answer, err := r.get(fmt.Sprintf("/api/v1/namespaces/%s/services/http:%s:%s/proxy/readyz", OrkaNamespace, name, port))
	if errors.As(err, new(orkaRefusal)) {
		return "", err
	}
	if err != nil {
		return fmt.Sprintf("Orka's API did not answer /readyz through Service %s/%s", OrkaNamespace, name), nil
	}
	if !orkaReadyzAnswer(answer) {
		return fmt.Sprintf("Orka's API /readyz through Service %s/%s did not report status ok", OrkaNamespace, name), nil
	}
	return "", nil
}

// orkaServiceSelectsChecked reports whether the named Service's selector
// contains every label of one checked Deployment's selector, so every pod it
// selects is one of that Deployment's pods.
func orkaServiceSelectsChecked(list orkaServiceList, name string, checked []map[string]string) bool {
	for _, service := range list.Items {
		if service.Metadata.Name != name {
			continue
		}
		for _, labels := range checked {
			covered := len(labels) != 0
			for key, value := range labels {
				if got, ok := service.Spec.Selector[key]; !ok || got != value {
					covered = false
				}
			}
			if covered {
				return true
			}
		}
	}
	return false
}

// orkaReadyzAnswer accepts only Orka's typed readiness document. A plain "ok",
// a 404 page or any other workload's answer is not Orka's API reporting ready.
func orkaReadyzAnswer(raw []byte) bool {
	var answer struct {
		Status *string `json:"status"`
	}
	return offlineJSON(raw, &answer, false) == nil && answer.Status != nil && *answer.Status == "ok"
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
	// Stderr is only classified, never returned: it can echo request data.
	stderr := &orkaBoundedBuffer{remaining: 4 << 10}
	cmd.Stdout, cmd.Stderr = &out, stderr
	if err := cmd.Run(); err != nil {
		if cancelled := ctx.Err(); cancelled != nil {
			return nil, cancelled
		}
		reason := strings.ToLower(stderr.buffer.String())
		switch {
		case strings.Contains(reason, `cannot get resource "services/proxy"`):
			return nil, fmt.Errorf("%s request failed: access to services/proxy forbidden", tool)
		case strings.Contains(reason, "error from server (forbidden)"), strings.Contains(reason, "(unauthorized)"),
			strings.Contains(reason, "must be logged in"):
			return nil, fmt.Errorf("%s request failed: access forbidden", tool)
		case strings.Contains(reason, "no such container"), strings.Contains(reason, "no such object"):
			return nil, fmt.Errorf("%s request failed: no such container", tool)
		}
		return nil, fmt.Errorf("%s local inspection failed or timed out", tool)
	}
	return out.buffer.Bytes(), nil
}

// orkaServingDenial turns the API server's own access denials into refusals:
// waiting cannot grant a permission. A read through the Service proxy refuses
// only on the services/proxy RBAC denial, because a workload behind a stale
// endpoint can itself answer 401 or 403.
func orkaServingDenial(what string, err error, proxied bool) error {
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "access to services/proxy forbidden"):
		return orkaRefusal{fmt.Errorf("cannot read %s in %s: access to services/proxy forbidden", what, OrkaNamespace)}
	case !proxied && strings.Contains(err.Error(), "access forbidden"):
		return orkaRefusal{fmt.Errorf("cannot read %s in %s: access forbidden", what, OrkaNamespace)}
	}
	return err
}

// kindNodeStartedAt reads when the selected kind cluster's node container last
// started. Kubelet and controller state written before that is from a previous
// run of the node. A container that is not that cluster's node, or that the
// selected engine does not have, refuses: waiting cannot change either.
func (a *App) kindNodeStartedAt(ctx context.Context, node string) (time.Time, error) {
	raw, err := a.offlineCaptureWithin(ctx, a.Cfg.ContainerEngine, "container", "inspect", node)
	if err != nil {
		if strings.HasSuffix(err.Error(), "no such container") {
			return time.Time{}, orkaRefusal{fmt.Errorf("%s has no container for node %s of kind cluster %s; if the cluster runs under another engine, set CONTAINER_ENGINE to it", a.Cfg.ContainerEngine, node, a.Cfg.KindCluster)}
		}
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

// orkaServingObserver is the online install-reuse view: the controller kmx
// accepted when it inspected the release, by name and UID, still the pinned
// controller, with current pod readiness and Orka's typed /readyz through its
// API Service. On the selected kind cluster pod readiness must also postdate
// the node container's current start.
func (a *App) orkaServingObserver(accepted orkaDeployment) func(context.Context) (string, error) {
	var first [][2]string
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
		// Selection is by role, so a replacement under another name would be
		// selected too. The controller must stay the one accepted before the
		// wait; any other required Deployment must stay as first observed.
		seen := make([][2]string, len(required))
		for i, d := range required {
			seen[i] = [2]string{d.Metadata.Name, d.Metadata.UID}
		}
		if first == nil {
			first = seen
		}
		if seen[0] != [2]string{accepted.Metadata.Name, accepted.Metadata.UID} || !slices.Equal(first, seen) {
			return "", fmt.Errorf("Orka's %s was replaced while kmx waited for it; refusing to adopt the replacement. %s", accepted.Metadata.Name, a.orkaInstallRecovery())
		}
		// Every refusal this Deployment list shows comes before any rollout
		// gap, so a pending rollout cannot hide a permanent condition.
		for _, d := range required {
			// A deletion held by a finalizer can keep the same UID, a complete
			// rollout and Ready pods; the installation is still going away.
			if d.Metadata.DeletionTimestamp != nil {
				return "", fmt.Errorf("Orka's %s is being deleted; refusing to reuse it. %s", d.Metadata.Name, a.orkaInstallRecovery())
			}
			if d.Spec.Replicas <= 0 {
				return "", a.orkaDeploymentReady(d)
			}
		}
		for _, d := range required {
			if err := a.orkaDeploymentReady(d); err != nil {
				return err.Error(), nil
			}
		}
		read := func(what string, proxied bool, args ...string) ([]byte, error) {
			raw, err := a.orkaCapture(ctx, nil, args...)
			if err != nil {
				return nil, orkaServingDenial(what, err, proxied)
			}
			return raw, nil
		}
		reader := orkaServingReader{
			pods: func(selector string) ([]byte, error) {
				return read("Orka pods", false, "-n", OrkaNamespace, "get", "pods", "--selector="+selector, "-o", "json")
			},
			replicaSets: func() ([]byte, error) {
				return read("Orka ReplicaSets", false, "-n", OrkaNamespace, "get", "replicasets", "-o", "json")
			},
			services: func() ([]byte, error) {
				return read("Orka Services", false, "-n", OrkaNamespace, "get", "services", "-o", "json")
			},
			get: func(path string) ([]byte, error) {
				return read("Orka's API readiness through its Service", true, "get", "--raw", path)
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
				name: d.Metadata.Name, uid: d.Metadata.UID, revision: d.Metadata.Annotations[orkaRevisionAnnotation],
				template: d.Spec.Template.whole, matchLabels: d.Spec.Selector.MatchLabels,
				matchExpressions: len(d.Spec.Selector.MatchExpressions), replicas: int(d.Spec.Replicas), images: images,
			})
			if pending != "" || err != nil {
				return pending, err
			}
		}
		checked := make([]map[string]string, 0, len(required))
		for _, d := range required {
			checked = append(checked, d.Spec.Selector.MatchLabels)
		}
		return reader.apiServing(checked)
	}
}
