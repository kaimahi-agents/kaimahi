package app

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// randomHex returns n cryptographically random bytes, hex encoded.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("entropy read failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// secretManifest renders an Opaque Secret. Values are base64-encoded into
// `data`, so nothing has to be escaped and no value can break out of the
// document.
func secretManifest(name, namespace string, values, annotations map[string]string) []byte {
	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: Secret\nmetadata:\n")
	fmt.Fprintf(&b, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(annotations) > 0 {
		b.WriteString("  annotations:\n")
		for _, k := range sortedKeys(annotations) {
			fmt.Fprintf(&b, "    %s: %q\n", k, annotations[k])
		}
	}
	b.WriteString("type: Opaque\ndata:\n")
	for _, k := range sortedKeys(values) {
		fmt.Fprintf(&b, "  %s: %s\n", k, base64.StdEncoding.EncodeToString([]byte(values[k])))
	}
	return []byte(b.String())
}

// sortedKeys keeps the rendered document stable, so re-running a step
// produces byte-identical YAML and `kubectl apply` reports no change.
func sortedKeys(m map[string]string) []string {
	return slices.Sorted(maps.Keys(m))
}

func (a *App) applySecretIn(namespace string, body []byte, name string) error {
	fmt.Fprintf(a.Err, "kubectl --context %s -n %s apply -f - # (Secret %s)\n", a.Cfg.KubeContext, namespace, name)
	quiet := *a.Run
	quiet.Echo = false
	return quiet.RunStdin(body, "kubectl", a.kubectl("-n", namespace, "apply", "-f", "-")...)
}
