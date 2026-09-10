package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// Metrics prints ONE proxy replica's Prometheus exposition.
//
// The ops port is on no Service, so this forwards to a POD: cluster
// credentials gate it exactly as they gate the admin port. The port itself
// has no auth — k8s/plane/network-policy.yaml is what says who may reach it
// in-cluster — which is precisely why it is not exposed.
//
// One pod, not an aggregate: the plane runs two replicas and each carries
// its own counters, so a merged view would be arithmetic kmx invented. The
// replica is named on stderr and the exposition goes to stdout, so
// `kmx metrics | grep ...` sees metrics and only metrics — CI greps this.
//
// A read: unguarded, like `make plane-metrics`.
func (a *App) Metrics(pod string) error {
	if pod == "" {
		pods, err := a.proxyPods()
		if err != nil {
			return err
		}
		if len(pods) == 0 {
			return fmt.Errorf("plane-metrics: no running kaimahi-proxy pod")
		}
		pod = pods[0]
	}

	port := a.Cfg.OpsPort
	if port == "" {
		port = config.DefaultOpsPort
	}
	fwd, err := admin.StartForward(a, admin.Namespace, "pod/"+pod, port, "9092")
	if err != nil {
		return fmt.Errorf("%w\n  (the ops port is on no Service; OPS_PORT=<free port> moves the local side)", err)
	}
	defer fwd.Close()

	client := &http.Client{
		Timeout: 60 * time.Second,
		// No redirects: this is an unauthenticated port, but a metrics
		// scrape that quietly followed a redirect is a scrape of something
		// else, reported as this replica's.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	base := "http://127.0.0.1:" + port
	live := run.Poll(150, 200*time.Millisecond, func() bool {
		resp, err := client.Get(base + "/livez")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode == http.StatusOK
	})
	if !live {
		return fmt.Errorf("the ops port did not answer on the forward to 127.0.0.1:%s:\n  %s\n"+
			"  The forward is up, so this is pod/%s, not the port.", port, fwd.Detail(), pod)
	}

	resp, err := client.Get(base + "/metrics")
	if err != nil {
		return fmt.Errorf("reading metrics from pod/%s: %w", pod, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("metrics read failed (HTTP %d) from pod/%s", resp.StatusCode, pod)
	}
	// The replica's name is EVIDENCE, not output: it goes to stderr so the
	// exposition on stdout stays machine-readable.
	fmt.Fprintf(a.Err, "# replica: %s\n", pod)
	_, err = io.Copy(a.Out, resp.Body)
	return err
}

// proxyPods returns the kaimahi-proxy pods that can take traffic RIGHT NOW:
// Ready and not terminating.
//
// `status.phase=Running` alone is not that. A pod draining after a rolling
// restart stays Running (and keeps its IP) until its grace period ends, and
// a port-forward to it fails. That distinction cost the clone-free CI job a
// fix of its own (#67).
func (a *App) proxyPods() ([]string, error) {
	out, err := a.kubectlCapture("-n", admin.Namespace, "get", "pods",
		"-l", "app=kaimahi-proxy", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("cannot list the plane's proxy pods: %w", err)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string `json:"name"`
				DeletionTimestamp string `json:"deletionTimestamp"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("cannot read the proxy pod list: %w", err)
	}
	var ready []string
	for _, p := range list.Items {
		if p.Metadata.DeletionTimestamp != "" {
			continue
		}
		for _, c := range p.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ready = append(ready, p.Metadata.Name)
				break
			}
		}
	}
	return ready, nil
}
