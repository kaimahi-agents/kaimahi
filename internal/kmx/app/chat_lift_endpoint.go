package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func foundryResourceState(raw []byte) (bool, error) {
	var resource struct {
		Properties struct{ ProvisioningState string }
	}
	if err := json.Unmarshal(raw, &resource); err != nil {
		return false, err
	}
	switch resource.Properties.ProvisioningState {
	case "Succeeded":
		return true, nil
	case "Failed", "Canceled", "Cancelled", "Deleting":
		return false, fmt.Errorf("Azure resource state: %s", resource.Properties.ProvisioningState)
	case "":
		return false, fmt.Errorf("Azure returned no provisioning state")
	default:
		return false, nil
	}
}

func (b *orkaChatBackend) waitFoundryReady(ctx context.Context, cluster chatLiftTarget, account foundryAccount, deployment string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	label := "Waiting for Foundry account " + account.Name
	args := []string{"cognitiveservices", "account", "show"}
	if deployment != "" {
		label = "Waiting for Foundry deployment " + deployment
		args = []string{"cognitiveservices", "account", "deployment", "show", "--deployment-name", deployment}
	}
	args = append(args, foundryScope(cluster, account)...)
	args = append(args, "-o", "json", "--only-show-errors")
	_, err := b.liftLoading(ctx, label, func(ctx context.Context) ([]byte, error) {
		for {
			raw, err := liftDiscovery(ctx, "az", args...)
			if err != nil {
				return nil, err
			}
			ready, err := foundryResourceState(raw)
			if err != nil {
				return nil, err
			}
			if ready {
				return raw, nil
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	})
	return err
}

const foundryProbeScript = `import json, os, urllib.request, urllib.error
try:
    with open('/credential/api-key') as f:
        key = f.read().strip()
    body = json.dumps({'model': os.environ['MODEL'], 'messages': [{'role':'user','content':'Reply with exactly: ready'}], 'max_completion_tokens': 64}).encode()
    req = urllib.request.Request(os.environ['ENDPOINT'].rstrip('/') + '/chat/completions', body, {'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, timeout=60) as response:
        result = json.loads(response.read(1048576))
    choices = result.get('choices', [])
    if not choices or not choices[0].get('message', {}).get('content', '').strip():
        raise ValueError('no response text')
    print('inference-ok')
except urllib.error.HTTPError as e:
    print('inference-http-' + str(e.code))
    raise SystemExit(1)
except Exception:
    print('inference-connection-failed')
    raise SystemExit(1)
`

func foundryProbeJob(name, namespace, secret, endpoint, model string) map[string]any {
	return map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": name, "namespace": namespace}, "spec": map[string]any{
		"backoffLimit": 0, "activeDeadlineSeconds": 120, "ttlSecondsAfterFinished": 300,
		"template": map[string]any{"spec": map[string]any{
			"restartPolicy": "Never", "automountServiceAccountToken": false,
			"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 1000, "runAsGroup": 1000, "fsGroup": 1000, "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
			"containers": []any{map[string]any{"name": "probe", "image": "python:3.13-alpine", "command": []string{"python3", "-B", "-c", foundryProbeScript},
				"env":             []any{map[string]string{"name": "ENDPOINT", "value": endpoint}, map[string]string{"name": "MODEL", "value": model}},
				"resources":       map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "32Mi"}, "limits": map[string]string{"memory": "128Mi"}},
				"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}},
				"volumeMounts":    []any{map[string]any{"name": "credential", "mountPath": "/credential", "readOnly": true}},
			}}, "volumes": []any{map[string]any{"name": "credential", "secret": map[string]any{"secretName": secret, "items": []any{map[string]string{"key": "api-key", "path": "api-key"}}}}},
		}},
	}}
}

func (b *orkaChatBackend) verifyLiftFoundry(ctx context.Context, target *App, secret, endpoint, model string) error {
	return b.runLiftDeployment(ctx, target, "Connect Foundry inference", []string{"Create endpoint probe", "Verify inference from cluster"}, func(worker *App) error {
		return worker.verifyFoundryEndpoint(worker.operationContext(), b.namespace, secret, endpoint, model)
	})
}

func (worker *App) verifyFoundryEndpoint(parent context.Context, namespace, secret, endpoint, model string) error {
	return worker.verifyFoundryEndpointKey(parent, namespace, secret, "api-key", endpoint, model)
}

func (worker *App) verifyFoundryEndpointKey(parent context.Context, namespace, secret, key, endpoint, model string) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	suffix, err := randomHex(4)
	if err != nil {
		return err
	}
	name := "kmx-inference-check-" + suffix
	job := foundryProbeJob(name, namespace, secret, endpoint, model)
	volumes := job["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["volumes"].([]any)
	volumes[0].(map[string]any)["secret"].(map[string]any)["items"] = []any{map[string]string{"key": key, "path": "api-key"}}
	body, err := json.Marshal(job)
	if err != nil {
		return err
	}
	if err := worker.runPhase(phase{name: "Create endpoint probe", current: 1, total: 2}, func() error {
		_, err := worker.orkaCapture(ctx, body, "-n", namespace, "create", "-f", "-", "-o", "name")
		return err
	}); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = worker.orkaCapture(cleanup, nil, "-n", namespace, "delete", "job", name, "--ignore-not-found=true", "--wait=false")
	}()
	return worker.runPhase(phase{name: "Verify inference from cluster", current: 2, total: 2}, func() error {
		for {
			raw, err := worker.orkaCapture(ctx, nil, "-n", namespace, "get", "job", name, "-o", "json")
			if err != nil {
				return err
			}
			var job struct {
				Status struct{ Succeeded, Failed int }
			}
			if err = json.Unmarshal(raw, &job); err != nil {
				return err
			}
			if job.Status.Succeeded > 0 {
				return nil
			}
			if job.Status.Failed > 0 {
				return fmt.Errorf("target %s cannot invoke Foundry deployment %s; check endpoint authentication, deployment compatibility, cluster egress and private networking (probe Job %s)", worker.Cfg.KubeContext, model, name)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	})
}
