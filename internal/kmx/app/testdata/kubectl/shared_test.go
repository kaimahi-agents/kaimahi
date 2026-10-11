// Package kubectl contains executable-boundary fixtures only. It intentionally
// does not import App: each fake CLI invocation should initialize its protocol
// fixture, not the terminal UI and other unrelated application dependencies.
package kubectl

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

type orkaCall struct {
	Args     []string
	Document map[string]any
	Patch    []map[string]any
}

const (
	OrkaNamespace      = "orka-system"
	orkaResultAccount  = "orka-result-reader"
	orkaPortableMarker = "kaimahi.dev/portable-digest"
	orkaRenderedMarker = "kaimahi.dev/rendered-digest"
)

func orkaTestToken() string { return "private-" + "session-token-never-print" }

func orkaPlural(kind string) string { return strings.ToLower(kind) + "s.core.orka.ai" }

func orkaAnnotationPointer(key string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(key)
}

func getenvLiftTest(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func workerController(name, chart, instance, managedBy, args string) string {
	return fmt.Sprintf(`{"metadata":{"name":%q,"labels":{"app.kubernetes.io/name":"orka","app.kubernetes.io/component":"controller","helm.sh/chart":%q,"app.kubernetes.io/instance":%q,"app.kubernetes.io/managed-by":%q}},"spec":{"template":{"spec":{"containers":[{"name":"controller","args":%s},{"name":"sidecar","args":["--ai-worker-service-account-name=wrong-sidecar"]}]}}}}`, name, chart, instance, managedBy, args)
}

func workerAccount(name, namespace, chart, instance, trust string) string {
	return fmt.Sprintf(`{"metadata":{"name":%q,"namespace":%q,"labels":{"orka.ai/worker":"true","orka.ai/worker-trust":%q,"helm.sh/chart":%q,"app.kubernetes.io/instance":%q,"app.kubernetes.io/name":"orka","app.kubernetes.io/managed-by":"Helm"}}}`, name, namespace, trust, chart, instance)
}

func TestFixtureProtocolHelper(t *testing.T) {
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{
		"namespace":         OrkaNamespace,
		"resultAccount":     orkaResultAccount,
		"portableMarker":    orkaPortableMarker,
		"renderedMarker":    orkaRenderedMarker,
		"token":             orkaTestToken(),
		"providerPlural":    orkaPlural("Provider"),
		"annotationPointer": orkaAnnotationPointer("a~/b"),
		"workerController":  workerController("controller", "orka-0.2.0", "orka", "Helm", `["--ai-worker-service-account-name=worker"]`),
		"workerAccount":     workerAccount("worker", OrkaNamespace, "orka-0.2.0", "orka", "ai"),
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestFixtureBuildModeHelper(t *testing.T) {
	fmt.Printf("race=%t\n", fixtureRace)
	os.Exit(0)
}
