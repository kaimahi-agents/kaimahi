package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The node container restarted at this instant; kubelet state from before it
// belongs to the previous run of the node.
const offlineNodeStartedText = "2026-01-02T03:04:05.600000000Z"

func offlineNodeStart() time.Time {
	at, _ := time.Parse(time.RFC3339Nano, offlineNodeStartedText)
	return at
}

// offlineServingTiming keeps the readiness wait short in tests without changing
// any other phase's bound.
func offlineServingTiming() *waitTiming {
	return &waitTiming{pollInterval: time.Millisecond, timeout: func(ctx context.Context, phase string, d time.Duration) (context.Context, context.CancelFunc) {
		if phase == "orka-serving" {
			return context.WithTimeout(ctx, 2*time.Second)
		}
		if d == 0 {
			return ctx, func() {}
		}
		return context.WithTimeout(ctx, d)
	}}
}

// offlineServingPod is the pod list a Deployment's selector returns: one
// Running pod controlled by the Deployment's ReplicaSet, containers started at
// started and the pod Ready since ready.
func offlineServingPod(name string, deploy map[string]any, started, ready time.Time) map[string]any {
	template := deploy["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	var containers, statuses []any
	for _, raw := range template["containers"].([]any) {
		c := raw.(map[string]any)
		containers = append(containers, map[string]any{"name": c["name"], "image": c["image"]})
		statuses = append(statuses, map[string]any{"name": c["name"], "ready": true, "restartCount": 1, "state": map[string]any{"running": map[string]any{"startedAt": started.UTC().Format(time.RFC3339)}}})
	}
	pod := map[string]any{
		"metadata": map[string]any{
			"name":            name + "-5d8f7c9b4-abcde",
			"labels":          map[string]any{"kmx.test/workload": name, "pod-template-hash": "5d8f7c9b4"},
			"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": name + "-5d8f7c9b4", "controller": true}},
		},
		"spec": map[string]any{"nodeName": "test-control-plane", "containers": containers},
		"status": map[string]any{
			"phase":             "Running",
			"podIP":             "10.244.0.4",
			"conditions":        []any{map[string]any{"type": "Ready", "status": "True", "lastTransitionTime": ready.UTC().Format(time.RFC3339)}},
			"containerStatuses": statuses,
		},
	}
	return map[string]any{"items": []any{pod}}
}

func writeOfflinePods(t *testing.T, dir, file string, pods map[string]any) {
	t.Helper()
	raw, err := json.Marshal(pods)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func servingItem(pods map[string]any) map[string]any {
	return pods["items"].([]any)[0].(map[string]any)
}

// offlineLiveDeploy returns the live Deployment fixture that offlineInstallApp
// wrote, so a test can derive a pod from the pinned template.
func offlineLiveDeploy(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(os.Getenv("OFFLINE_LIVE"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Items []map[string]any }
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, d := range doc.Items {
		if d["metadata"].(map[string]any)["name"] == name {
			return d
		}
	}
	t.Fatalf("no live Deployment %s", name)
	return nil
}

// writeOfflineLive replaces one Deployment in the live list, for every read
// (suffix "") or only for the read with that number (suffix ".N").
func writeOfflineLive(t *testing.T, suffix string, changed map[string]any) {
	t.Helper()
	path := os.Getenv("OFFLINE_LIVE")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Items []map[string]any }
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for i, d := range doc.Items {
		if d["metadata"].(map[string]any)["name"] == changed["metadata"].(map[string]any)["name"] {
			doc.Items[i] = changed
		}
	}
	raw, err = json.Marshal(map[string]any{"items": doc.Items})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+suffix, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// stalePods is what the API serves before the kubelet resyncs after a node
// restart: the previous run's Ready pod, consistent with itself.
func stalePods(t *testing.T, name string) map[string]any {
	before := offlineNodeStart().Add(-10 * time.Minute)
	return offlineServingPod(name, offlineLiveDeploy(t, name), before, before.Add(30*time.Second))
}

// crashingPodsFor is the kubelet's view after the restart while the controller
// cannot reach the Service network yet: restarted, not Ready.
func crashingPodsFor(deploy map[string]any) map[string]any {
	name := deploy["metadata"].(map[string]any)["name"].(string)
	pods := offlineServingPod(name, deploy, offlineNodeStart().Add(5*time.Second), offlineNodeStart().Add(5*time.Second))
	status := servingItem(pods)["status"].(map[string]any)
	status["conditions"] = []any{map[string]any{"type": "Ready", "status": "False", "lastTransitionTime": offlineNodeStart().Add(5 * time.Second).UTC().Format(time.RFC3339)}}
	for _, raw := range status["containerStatuses"].([]any) {
		raw.(map[string]any)["ready"] = false
	}
	return pods
}

func crashingPods(t *testing.T, name string) map[string]any {
	return crashingPodsFor(offlineLiveDeploy(t, name))
}

func TestOfflineInstallReuseWaitsForReadinessAfterNodeStart(t *testing.T) {
	a, dir, _, log := offlineInstallApp(t)
	t.Setenv("OFFLINE_RELEASE", `[{"name":"orka","chart":"orka-0.0.0-dev","app_version":"v0.0.0-dev","status":"deployed"}]`)
	// The Deployment status fixture stays 1/1 ready throughout, as the
	// controller manager's pre-restart record does.
	pods := os.Getenv("OFFLINE_PODS")
	writeOfflinePods(t, pods, "orka-api-controller.1", stalePods(t, "orka-api-controller"))
	writeOfflinePods(t, pods, "orka-api-controller.2", crashingPods(t, "orka-api-controller"))
	writeOfflinePods(t, pods, "orka-api-controller.3", crashingPods(t, "orka-api-controller"))
	// The Service then reaches a missing endpoint, then another workload's
	// plain "ok" on the controller's previous address, then Orka itself.
	os.WriteFile(log+".readyz.1", []byte("missing"), 0o600)
	os.WriteFile(log+".readyz.2", []byte("ok"), 0o600)
	if err := a.OrkaInstall(OrkaOptions{OfflineBundle: dir, Provider: "-"}); err != nil {
		t.Fatalf("reuse did not finish once current readiness was shown: %v", err)
	}
	reads, _ := os.ReadFile(log + ".reads.orka-api-controller")
	readyz, _ := os.ReadFile(log + ".readyz-reads")
	if strings.TrimSpace(string(reads)) != "6" || strings.TrimSpace(string(readyz)) != "3" {
		t.Fatalf("reuse accepted readiness before the fresh Ready pod and Orka's typed answer: %s controller reads, %s API reads", reads, readyz)
	}
	out := a.Err.(interface{ String() string }).String()
	if !strings.Contains(out, "Waiting up to 10m0s for offline Orka workloads to show current readiness.") || !strings.Contains(out, "COMPLETE") {
		t.Fatalf("the wait was not announced or did not finish: %s", out)
	}
}

func TestOfflineInstallReuseRefusesUnprovenReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		waits      bool
		setup      func(t *testing.T, pods string)
	}{
		{"pod status predates the node start", "pod status predates the current start of node test-control-plane", true, func(t *testing.T, pods string) {
			writeOfflinePods(t, pods, "orka-api-controller", stalePods(t, "orka-api-controller"))
		}},
		{"container started before the node", "pod status predates the current start of node test-control-plane", true, func(t *testing.T, pods string) {
			// Ready after the node started, but the running container is the
			// previous run's.
			p := offlineServingPod("orka-api-controller", offlineLiveDeploy(t, "orka-api-controller"), offlineNodeStart().Add(-time.Minute), offlineNodeStart().Add(10*time.Second))
			writeOfflinePods(t, pods, "orka-api-controller", p)
		}},
		{"controller restarted but not Ready", "orka-api-controller pod is not Ready", true, func(t *testing.T, pods string) {
			writeOfflinePods(t, pods, "orka-api-controller", crashingPods(t, "orka-api-controller"))
		}},
		{"node container not running", "the start time of node test-control-plane could not be read", true, func(t *testing.T, _ string) {
			t.Setenv("OFFLINE_NODE_STATE", "false")
		}},
		{"Deployment status shows a rollout in progress", "Deployment orka-api-controller status is not a completed rollout", true, func(t *testing.T, _ string) {
			d := offlineLiveDeploy(t, "orka-api-controller")
			d["status"].(map[string]any)["unavailableReplicas"] = 1
			writeOfflineLive(t, "", d)
		}},
		{"another workload answers through the Service", "/readyz through Service orka-system/orka-api did not report status ok", true, func(t *testing.T, _ string) {
			t.Setenv("OFFLINE_READYZ", "ok")
		}},
		{"API Service does not answer", "did not answer /readyz through Service orka-system/orka-api", true, func(t *testing.T, _ string) {
			t.Setenv("OFFLINE_READYZ", "missing")
		}},
		{"no API Service", "no Orka API Service in orka-system", false, func(t *testing.T, _ string) {
			t.Setenv("OFFLINE_SERVICES", `{"items":[]}`)
		}},
		{"extra foreign pod", "not controlled by its ReplicaSet", false, func(t *testing.T, pods string) {
			// One correct pod and one foreign pod: refuse at once rather than
			// waiting on the replica count.
			p := offlineServingPod("orka-api-controller", offlineLiveDeploy(t, "orka-api-controller"), offlineNodeStart().Add(time.Second), offlineNodeStart().Add(2*time.Second))
			foreign := servingItem(offlineServingPod("orka-api-controller", offlineLiveDeploy(t, "orka-api-controller"), offlineNodeStart().Add(time.Second), offlineNodeStart().Add(2*time.Second)))
			foreign["metadata"].(map[string]any)["name"] = "intruder"
			foreign["metadata"].(map[string]any)["ownerReferences"] = []any{}
			p["items"] = append(p["items"].([]any), foreign)
			writeOfflinePods(t, pods, "orka-api-controller", p)
		}},
		{"selected pod is foreign", "not controlled by its ReplicaSet", false, func(t *testing.T, pods string) {
			p := offlineServingPod("orka-api-controller", offlineLiveDeploy(t, "orka-api-controller"), offlineNodeStart().Add(time.Second), offlineNodeStart().Add(2*time.Second))
			servingItem(p)["metadata"].(map[string]any)["ownerReferences"] = []any{}
			writeOfflinePods(t, pods, "orka-api-controller", p)
		}},
		{"workload scaled to zero", "orka-api-controller is scaled to 0", false, func(t *testing.T, _ string) {
			d := offlineLiveDeploy(t, "orka-api-controller")
			d["spec"].(map[string]any)["replicas"] = 0
			writeOfflineLive(t, "", d)
		}},
		{"execution settings change while waiting", "execution settings differ", false, func(t *testing.T, pods string) {
			// Read 1 is the install-state check and read 2 the first look
			// while waiting; the controller's image changes before read 3.
			writeOfflinePods(t, pods, "orka-api-controller", stalePods(t, "orka-api-controller"))
			d := offlineLiveDeploy(t, "orka-api-controller")
			c := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
			c["image"] = "ghcr.io/foreign/orka@sha256:" + strings.Repeat("0", 64)
			writeOfflineLive(t, ".3", d)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, dir, _, log := offlineInstallApp(t)
			t.Setenv("OFFLINE_RELEASE", `[{"name":"orka","chart":"orka-0.0.0-dev","app_version":"v0.0.0-dev","status":"deployed"}]`)
			tc.setup(t, os.Getenv("OFFLINE_PODS"))
			err := a.OrkaInstall(OrkaOptions{OfflineBundle: dir, Provider: "-"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unproven readiness = %v; want %q", err, tc.want)
			}
			if timedOut := strings.Contains(err.Error(), "did not show current readiness within 10m0s"); timedOut != tc.waits {
				t.Fatalf("waited=%v, want %v: %v", timedOut, tc.waits, err)
			}
			if tc.waits && !strings.Contains(err.Error(), "effects retained, no reinstall attempted") {
				t.Fatalf("timeout does not say what was retained: %v", err)
			}
			commands, _ := os.ReadFile(log)
			for _, write := range []string{"helm install", "--server-side", "apply -f -", "delete", "rollout restart"} {
				if strings.Contains(string(commands), write) {
					t.Fatalf("readiness refusal wrote %q: %s", write, commands)
				}
			}
			if _, e := os.Stat(log + ".reader"); e == nil {
				t.Fatal("result reader ran before readiness was shown")
			}
			if strings.Contains(a.Err.(interface{ String() string }).String(), "COMPLETE") {
				t.Fatal("unproven readiness reported success")
			}
		})
	}
}

func TestOrkaServingCheck(t *testing.T) {
	boot := offlineNodeStart()
	controller := func() orkaServingPod {
		var pod orkaServingPod
		deploy := map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "controller", "image": "registry.invalid/orka@sha256:1"}}}}}}
		raw, _ := json.Marshal(servingItem(offlineServingPod("orka-api-controller", deploy, boot.Add(time.Second), boot.Add(2*time.Second))))
		if err := json.Unmarshal(raw, &pod); err != nil {
			t.Fatal(err)
		}
		return pod
	}
	workload := func() orkaServingWorkload {
		return orkaServingWorkload{name: "orka-api-controller", matchLabels: map[string]string{"app.kubernetes.io/name": "orka", "app.kubernetes.io/component": "controller"}, replicas: 1, images: map[string]string{"controller": "registry.invalid/orka@sha256:1"}}
	}
	type outcome struct{ pending, refused string }
	for _, tc := range []struct {
		name     string
		workload func(*orkaServingWorkload)
		pods     func() []orkaServingPod
		node     func(string) (time.Time, error)
		noBoot   bool
		want     outcome
		selector string
	}{
		{name: "current", selector: "app.kubernetes.io/component=controller,app.kubernetes.io/name=orka"},
		{name: "terminating and failed pods are not counted", pods: func() []orkaServingPod {
			old, failed := controller(), controller()
			at := "2026-01-02T03:04:06Z"
			old.Metadata.DeletionTimestamp = &at
			failed.Status.Phase = "Failed"
			return []orkaServingPod{old, controller(), failed}
		}},
		{name: "second live pod", pods: func() []orkaServingPod { return []orkaServingPod{controller(), controller()} }, want: outcome{pending: "2 current pods for 1 replicas"}},
		{name: "extra foreign pod refuses before the count", pods: func() []orkaServingPod {
			foreign := controller()
			foreign.Metadata.OwnerReferences = nil
			return []orkaServingPod{controller(), foreign}
		}, want: outcome{refused: "not controlled by its ReplicaSet"}},
		{name: "no pod", pods: func() []orkaServingPod { return nil }, want: outcome{pending: "0 current pods"}},
		{name: "old image", pods: func() []orkaServingPod {
			p := controller()
			p.Spec.Containers[0].Image = "registry.invalid/orka@sha256:0"
			return []orkaServingPod{p}
		}, want: outcome{pending: "pinned images"}},
		{name: "an injected sidecar is covered by pod readiness", pods: func() []orkaServingPod {
			p := controller()
			p.Spec.Containers = append(p.Spec.Containers, p.Spec.Containers[0])
			p.Spec.Containers[1].Name, p.Spec.Containers[1].Image = "mesh-proxy", "registry.invalid/proxy"
			return []orkaServingPod{p}
		}},
		{name: "Deployment container missing", pods: func() []orkaServingPod {
			p := controller()
			p.Spec.Containers[0].Name = "renamed"
			return []orkaServingPod{p}
		}, want: outcome{pending: "does not run the Deployment's containers"}},
		{name: "pending phase", pods: func() []orkaServingPod {
			p := controller()
			p.Status.Phase = "Pending"
			return []orkaServingPod{p}
		}, want: outcome{pending: "not running"}},
		{name: "container not ready", pods: func() []orkaServingPod {
			p := controller()
			p.Status.ContainerStatuses[0].Ready = false
			return []orkaServingPod{p}
		}, want: outcome{pending: "container controller is not running and ready"}},
		{name: "Ready transition before the node start", pods: func() []orkaServingPod {
			p := controller()
			before := boot.Add(-time.Second)
			p.Status.Conditions[0].LastTransitionTime = &before
			return []orkaServingPod{p}
		}, want: outcome{pending: "predates the current start"}},
		{name: "container start before the node start", pods: func() []orkaServingPod {
			p := controller()
			before := boot.Add(-time.Minute)
			p.Status.ContainerStatuses[0].State.Running.StartedAt = &before
			return []orkaServingPod{p}
		}, want: outcome{pending: "predates the current start"}},
		{name: "Ready in the node's start second", pods: func() []orkaServingPod {
			p := controller()
			same := boot.Truncate(time.Second)
			p.Status.Conditions[0].LastTransitionTime = &same
			p.Status.ContainerStatuses[0].State.Running.StartedAt = &same
			return []orkaServingPod{p}
		}},
		{name: "unreadable node start is a gap", node: func(string) (time.Time, error) { return time.Time{}, errors.New("engine busy") }, want: outcome{pending: "start time of node test-control-plane could not be read"}},
		{name: "foreign node container refuses", node: func(string) (time.Time, error) {
			return time.Time{}, orkaRefusal{errors.New("not a node container")}
		}, want: outcome{refused: "not a node container"}},
		{name: "without a node container only current readiness is required", noBoot: true, pods: func() []orkaServingPod {
			p := controller()
			before := boot.Add(-time.Hour)
			p.Status.Conditions[0].LastTransitionTime = &before
			return []orkaServingPod{p}
		}},
		{name: "two controlling owners", pods: func() []orkaServingPod {
			p := controller()
			p.Metadata.OwnerReferences = append(p.Metadata.OwnerReferences, p.Metadata.OwnerReferences[0])
			return []orkaServingPod{p}
		}, want: outcome{refused: "not controlled by its ReplicaSet"}},
		{name: "another ReplicaSet's pod", pods: func() []orkaServingPod {
			p := controller()
			p.Metadata.OwnerReferences[0].Name = "orka-api-publisher-5d8f7c9b4"
			return []orkaServingPod{p}
		}, want: outcome{refused: "not controlled by its ReplicaSet"}},
		{name: "expression selector", workload: func(w *orkaServingWorkload) { w.matchExpressions = 1 }, want: outcome{refused: "no supported label selector"}},
		{name: "selector injection", workload: func(w *orkaServingWorkload) { w.matchLabels["app"] = "orka,tier!=x" }, want: outcome{refused: "unsupported label selector"}},
		{name: "scaled to zero", workload: func(w *orkaServingWorkload) { w.replicas = 0 }, want: outcome{refused: "scaled to 0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := workload()
			if tc.workload != nil {
				tc.workload(&w)
			}
			pods := []orkaServingPod{controller()}
			if tc.pods != nil {
				pods = tc.pods()
			}
			var selector string
			nodeReads := 0
			r := orkaServingReader{
				pods: func(s string) ([]byte, error) {
					selector = s
					return json.Marshal(map[string]any{"items": pods})
				},
				decode: func(raw []byte, out any) error { return offlineJSON(raw, out, false) },
				nodeStart: func(node string) (time.Time, error) {
					nodeReads++
					if tc.node != nil {
						return tc.node(node)
					}
					if node != "test-control-plane" {
						return time.Time{}, errors.New("unknown node")
					}
					return boot, nil
				},
			}
			if tc.noBoot {
				r.nodeStart = nil
			}
			pending, err := r.check(w)
			switch {
			case tc.want.refused != "":
				if err == nil || !strings.Contains(err.Error(), tc.want.refused) {
					t.Fatalf("refusal = %v, %q; want %q", err, pending, tc.want.refused)
				}
			case err != nil:
				t.Fatalf("unexpected refusal: %v", err)
			case tc.want.pending != "":
				if !strings.Contains(pending, tc.want.pending) {
					t.Fatalf("pending = %q; want %q", pending, tc.want.pending)
				}
			case pending != "":
				t.Fatalf("current pod not accepted: %q", pending)
			}
			if tc.selector != "" && selector != tc.selector {
				t.Fatalf("selector = %q, want %q", selector, tc.selector)
			}
			if tc.name == "current" && nodeReads != 1 {
				t.Fatalf("node start read %d times for one pod", nodeReads)
			}
		})
	}
}

// Only Orka's typed readiness document counts; a plain "ok" from another
// workload on a stale endpoint, a 404 page or a near miss does not.
func TestOrkaReadyzAnswer(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"checks":{"kubernetes":"ok","store":"ok"},"status":"ok"}`: true,
		`{"status":"ok"}`: true,
		`{"status":"not ready","checks":{"store":"unhealthy"}}`: false,
		"ok":                                   false,
		"ok\n":                                 false,
		`"ok"`:                                 false,
		`["ok"]`:                               false,
		`{}`:                                   false,
		`null`:                                 false,
		`{"status":true}`:                      false,
		`{"status":"not ready","status":"ok"}`: false,
		`{"Status":"no","status":"ok"}`:        false,
		`{"status":"ok"} {"status":"ok"}`:      false,
		`404 page not found`:                   false,
		``:                                     false,
	} {
		if got := orkaReadyzAnswer([]byte(raw)); got != want {
			t.Errorf("orkaReadyzAnswer(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestOrkaServingAPIServing(t *testing.T) {
	service := func(name string, selector map[string]any, port string) map[string]any {
		return map[string]any{"metadata": map[string]any{"name": name, "labels": map[string]any{"app.kubernetes.io/name": "orka"}}, "spec": map[string]any{"selector": selector, "ports": []any{map[string]any{"name": port, "port": 8080}}}}
	}
	controller := map[string]any{"app.kubernetes.io/name": "orka", "app.kubernetes.io/component": "controller"}
	typed := `{"status":"ok"}`
	for _, tc := range []struct {
		name     string
		services []any
		read     error
		answer   string
		get      error
		pending  string
		refused  string
	}{
		{name: "typed answer through the API Service", services: []any{service("orka-api", controller, "api")}, answer: typed},
		{name: "stale endpoint answers plain ok", services: []any{service("orka-api", controller, "api")}, answer: "ok", pending: "did not report status ok"},
		{name: "Service proxy fails", services: []any{service("orka-api", controller, "api")}, get: errors.New("503"), pending: "did not answer /readyz through Service orka-system/orka-api"},
		{name: "Service proxy forbidden", services: []any{service("orka-api", controller, "api")}, get: orkaRefusal{errors.New("access forbidden")}, refused: "access forbidden"},
		{name: "Services unreadable", read: errors.New("timeout"), pending: "Orka Services in orka-system could not be read"},
		{name: "no API Service", services: []any{service("orka-api-metrics", controller, "metrics")}, refused: "no Orka API Service"},
		{name: "two API Services", services: []any{service("orka-api", controller, "api"), service("orka", controller, "api")}, refused: "multiple Orka API Services"},
		{name: "unsafe Service name", services: []any{service("orka-api/../x", controller, "api")}, refused: "unsupported name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			r := orkaServingReader{
				services: func() ([]byte, error) {
					if tc.read != nil {
						return nil, tc.read
					}
					return json.Marshal(map[string]any{"items": tc.services})
				},
				get: func(path string) ([]byte, error) {
					paths = append(paths, path)
					return []byte(tc.answer), tc.get
				},
				decode: func(raw []byte, out any) error { return offlineJSON(raw, out, false) },
			}
			pending, err := r.apiServing()
			switch {
			case tc.refused != "":
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("refusal = %v, %q; want %q", err, pending, tc.refused)
				}
			case err != nil:
				t.Fatalf("unexpected refusal: %v", err)
			case !strings.Contains(pending, tc.pending) || (tc.pending == "") != (pending == ""):
				t.Fatalf("pending = %q, want %q", pending, tc.pending)
			}
			if tc.answer == typed && (len(paths) != 1 || paths[0] != "/api/v1/namespaces/orka-system/services/http:orka-api:api/proxy/readyz") {
				t.Fatalf("readiness requested at %v", paths)
			}
		})
	}
}

func TestOrkaServingCheckTreatsFailedReadsAsNotYetReady(t *testing.T) {
	r := orkaServingReader{
		pods:   func(string) ([]byte, error) { return nil, errors.New("connection refused") },
		decode: json.Unmarshal,
	}
	w := orkaServingWorkload{name: "orka-api-controller", matchLabels: map[string]string{"app": "orka"}, replicas: 1, images: map[string]string{"controller": "x"}}
	if pending, err := r.check(w); err != nil || !strings.Contains(pending, "could not be read") {
		t.Fatalf("unreadable pods = %q, %v; want a readiness gap", pending, err)
	}
	r.pods = func(string) ([]byte, error) { return nil, orkaRefusal{errors.New("access forbidden")} }
	if _, err := r.check(w); err == nil || !strings.Contains(err.Error(), "access forbidden") {
		t.Fatalf("forbidden pods = %v; want refusal", err)
	}
	r.pods = func(string) ([]byte, error) { return []byte("{not json"), nil }
	if _, err := r.check(w); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed pod list = %v; want refusal", err)
	}
}

// The node container must be the selected kind cluster's node by name and
// label; reads are bounded by the waiting context.
func TestKindNodeStartedAt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake container engine is a shell script")
	}
	for _, tc := range []struct {
		name, output, want string
		refusal, hang      bool
	}{
		{name: "docker node", output: `[{"Name":"/kind-control-plane","Config":{"Labels":{"io.x-k8s.kind.cluster":"kind"}},"State":{"Running":true,"StartedAt":"2026-01-02T03:04:05.600000000Z"}}]`},
		{name: "podman node", output: `[{"Name":"kind-control-plane","Config":{"Labels":{"io.x-k8s.kind.cluster":"kind"}},"State":{"Running":true,"StartedAt":"2026-01-02T04:04:05.6+01:00"}}]`},
		{name: "another cluster's node", output: `[{"Name":"/kind-control-plane","Config":{"Labels":{"io.x-k8s.kind.cluster":"other"}},"State":{"Running":true,"StartedAt":"2026-01-02T03:04:05Z"}}]`, want: "not a docker node container of kind cluster kind", refusal: true},
		{name: "another container", output: `[{"Name":"/kind-worker","Config":{"Labels":{"io.x-k8s.kind.cluster":"kind"}},"State":{"Running":true,"StartedAt":"2026-01-02T03:04:05Z"}}]`, want: "not a docker node container", refusal: true},
		{name: "stopped node", output: `[{"Name":"/kind-control-plane","Config":{"Labels":{"io.x-k8s.kind.cluster":"kind"}},"State":{"Running":false,"StartedAt":"2026-01-02T03:04:05Z"}}]`, want: "is not running"},
		{name: "no start time", output: `[{"Name":"/kind-control-plane","Config":{"Labels":{"io.x-k8s.kind.cluster":"kind"}},"State":{"Running":true,"StartedAt":"0001-01-01T00:00:00Z"}}]`, want: "has no start time"},
		{name: "wedged engine", hang: true, want: "context deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := t.TempDir()
			body := "#!/bin/sh\nprintf '%s' \"$KMX_TEST_NODE_INSPECT\"\n"
			if tc.hang {
				body = "#!/bin/sh\nexec sleep 30\n"
			}
			if err := os.WriteFile(filepath.Join(tools, "docker"), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("KMX_TEST_NODE_INSPECT", tc.output)
			a := New(&config.Config{ContainerEngine: "docker", KindCluster: "kind"})
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			began := time.Now()
			at, err := a.kindNodeStartedAt(ctx, "kind-control-plane")
			if tc.want == "" {
				if err != nil || !at.Equal(offlineNodeStart()) {
					t.Fatalf("start = %v, %v; want %v", at, err, offlineNodeStart())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want %q", err, tc.want)
			}
			if got := errors.As(err, new(orkaRefusal)); got != tc.refusal {
				t.Fatalf("refusal = %v, want %v: %v", got, tc.refusal, err)
			}
			if tc.hang && time.Since(began) > 5*time.Second {
				t.Fatalf("a wedged engine held the read for %s", time.Since(began))
			}
		})
	}
}

func TestAwaitOrkaServingStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := run.Default()
	r.Context = ctx
	a := &App{Run: r, Err: &strings.Builder{}, waitTiming: offlineServingTiming()}
	calls := 0
	err := a.awaitOrkaServing("Orka", "", func(context.Context) (string, error) {
		calls++
		if calls == 2 {
			cancel()
		}
		return "not yet", nil
	})
	if !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("cancelled wait = %v after %d observations", err, calls)
	}
}

// The bound can cut off the final observation's reads. The refusal names the
// last complete observation, not the interrupted read.
func TestAwaitOrkaServingReportsLastCompleteObservation(t *testing.T) {
	a := &App{Run: run.Default(), Err: &strings.Builder{}, waitTiming: &waitTiming{pollInterval: time.Millisecond, timeout: func(ctx context.Context, _ string, _ time.Duration) (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, 300*time.Millisecond)
	}}}
	err := a.awaitOrkaServing("Orka", "effects retained", func(ctx context.Context) (string, error) {
		if _, deadline := ctx.Deadline(); !deadline {
			t.Fatal("observation is not bounded")
		}
		select {
		case <-ctx.Done():
			return "pods could not be read", nil
		case <-time.After(20 * time.Millisecond):
			return "controller pod is not Ready", nil
		}
	})
	if err == nil || !strings.Contains(err.Error(), "last: controller pod is not Ready") || !strings.HasSuffix(err.Error(), "; effects retained") {
		t.Fatalf("timeout = %v", err)
	}
	if !strings.Contains(a.Err.(*strings.Builder).String(), "Waiting up to 10m0s for Orka") {
		t.Fatalf("wait not announced once: %q", a.Err)
	}
}
