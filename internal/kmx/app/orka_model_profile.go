package app

import (
	"fmt"
	"strconv"
	"strings"
)

const modelCredentialFile = modelCredentialDir + "/copilot-github-token"

// The endpoint owns bytes and serializes delivery; kmx only checks metadata.
func (a *App) modelCredentialInput() error {
	if _, err := a.modelMountSource(); err != nil {
		return err
	}
	raw, err := a.offlineCapture("stat", "-c", "%F:%u:%g:%a:%h:%s", "--", modelCredentialFile)
	f := strings.Split(strings.TrimSpace(string(raw)), ":")
	if err != nil || len(f) != 6 || f[0] != "regular file" || f[1] != "0" || f[2] != "65532" || f[3] != "640" || f[4] != "1" {
		return fmt.Errorf("credential file is unavailable or incompatible")
	}
	size, err := strconv.ParseUint(f[5], 10, 32)
	if err != nil || size == 0 || size > 16384 {
		return fmt.Errorf("credential file size is unavailable or incompatible")
	}
	return nil
}

// The interpreter is static on both qualified platforms. Copying it to a new
// memory volume keeps the approved shell-less upstream Vekil image unchanged.
func modelLauncherScript(tokenFile, tool, executable string) string {
	return fmt.Sprintf(`set -eu
export LC_ALL=C
file=%q
bb=%q
meta=$("$bb" stat -c '%%u:%%g:%%a:%%h:%%F:%%s' -- "$file") || exit 1
case "$meta" in '0:65532:640:1:regular file:'*) ;; *) exit 1 ;; esac
size=${meta##*:}
case "$size" in ''|*[!0-9]*) exit 1 ;; esac
[ "$size" -gt 0 ] && [ "$size" -le 16384 ] || exit 1
token=$("$bb" cat -- "$file") || exit 1
[ "${#token}" -eq "$size" ] || exit 1
case "$token" in ''|*[![:graph:]]*) exit 1 ;; esac
export COPILOT_GITHUB_TOKEN="$token"
exec %q --host 0.0.0.0 --port 1337 --token-dir /run/vekil/auth --state-bindings-mode memory --policy-routing off >/dev/null 2>&1
`, tokenFile, tool, executable)
}

func modelDocuments(provider, model, operation, arch string) ([]map[string]any, error) {
	images := map[string][2]string{
		"amd64": {"b885b08ca26abb6362fee830cc683c255ec740da28ffcb48b711232ffebf2040", "5624bb6777cf469b1fb50ae3f981b15d2715ee2fffaf20b9998cfb7843726835"},
		"arm64": {"9c1788a001e9abac79286d5cb5ccf43cbce67fe9391f68fefc835cfce0947948", "a4b7bbbb14a7e50bec3f6cbc4e2560c9b6792a7ae7062637bdfc25793817d4ff"},
	}
	pins, ok := images[arch]
	if !ok || !modelResourceName(provider) || len(provider) > 45 || !verificationIdentity(model) || !modelUUID(operation) {
		return nil, fmt.Errorf("unsupported model profile")
	}
	name := provider + "-vekil"
	secret := provider + "-model-key"
	labels := map[string]any{"app.kubernetes.io/name": name}
	makeDoc := func(kind, version, name string, spec map[string]any) map[string]any {
		return map[string]any{"apiVersion": version, "kind": kind, "metadata": map[string]any{"name": name, "namespace": OrkaNamespace, "annotations": map[string]any{modelOperationAnnotation: operation}}, "spec": spec}
	}
	security := map[string]any{"runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []any{"ALL"}}}
	memory := map[string]any{"name": "memory", "mountPath": "/run/vekil"}
	init := map[string]any{"name": "launcher", "image": "docker.io/library/busybox@sha256:" + pins[1], "imagePullPolicy": "Never", "command": []any{"/bin/sh", "-ec"}, "args": []any{`umask 077; mkdir -p /run/vekil/bin /run/vekil/auth /run/vekil/home /run/vekil/data /run/vekil/config /run/vekil/tmp; cp /bin/busybox /run/vekil/bin/busybox; chmod 500 /run/vekil/bin/busybox`}, "securityContext": security, "volumeMounts": []any{memory}}
	container := map[string]any{"name": "vekil", "image": "ghcr.io/sozercan/vekil@sha256:" + pins[0], "imagePullPolicy": "Never", "command": []any{"/run/vekil/bin/busybox", "sh", "-ec"}, "args": []any{modelLauncherScript(modelCredentialFile, "/run/vekil/bin/busybox", "/vekil")}, "securityContext": security, "volumeMounts": []any{memory, map[string]any{"name": "credential", "mountPath": modelCredentialDir, "readOnly": true, "mountPropagation": "None"}}, "ports": []any{map[string]any{"name": "http", "containerPort": 1337, "protocol": "TCP"}}, "env": []any{map[string]any{"name": "HOME", "value": "/run/vekil/home"}, map[string]any{"name": "XDG_CONFIG_HOME", "value": "/run/vekil/config"}, map[string]any{"name": "XDG_DATA_HOME", "value": "/run/vekil/data"}, map[string]any{"name": "TMPDIR", "value": "/run/vekil/tmp"}}}
	pod := map[string]any{"serviceAccountName": "default", "automountServiceAccountToken": false, "enableServiceLinks": false, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532, "fsGroup": 65532, "seccompProfile": map[string]any{"type": "RuntimeDefault"}}, "containers": []any{container}, "initContainers": []any{init}, "volumes": []any{map[string]any{"name": "credential", "hostPath": map[string]any{"path": modelCredentialDir, "type": "Directory"}}, map[string]any{"name": "memory", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "128Mi"}}}}
	// Spell admission-sensitive execution defaults in the producer, not reuse policy.
	pod["priority"], pod["preemptionPolicy"] = 0, "PreemptLowerPriority"
	pod["tolerations"] = []any{map[string]any{"key": "node.kubernetes.io/not-ready", "operator": "Exists", "effect": "NoExecute", "tolerationSeconds": 300}, map[string]any{"key": "node.kubernetes.io/unreachable", "operator": "Exists", "effect": "NoExecute", "tolerationSeconds": 300}}
	dep := makeDoc("Deployment", "apps/v1", name, map[string]any{"replicas": 1, "strategy": map[string]any{"type": "Recreate"}, "selector": map[string]any{"matchLabels": labels}, "template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": pod}})
	key := makeDoc("Secret", "v1", secret, nil)
	delete(key, "spec")
	key["type"] = "Opaque"
	key["data"] = map[string]any{"api-key": "bm90LXVzZWQtYnktdGhpcy1lbmRwb2ludA=="}
	service := makeDoc("Service", "v1", name, map[string]any{"type": "ClusterIP", "selector": labels, "ports": []any{map[string]any{"name": "http", "port": 1337, "targetPort": 1337, "protocol": "TCP"}}})
	binding := makeDoc("Provider", "core.orka.ai/v1alpha1", provider, map[string]any{"type": "openai", "defaultModel": model, "baseURL": "http://" + name + ".orka-system.svc:1337/v1", "secretRef": map[string]any{"name": secret, "key": "api-key"}})
	return []map[string]any{key, dep, service, binding}, nil
}
