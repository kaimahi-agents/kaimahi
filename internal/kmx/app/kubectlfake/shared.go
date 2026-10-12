package kubectlfake

import (
	"encoding/json"
	"fmt"
	"strings"
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

func (r Request) getenvLiftTest(key, fallback string) string {
	return GetenvFallback(r.Getenv, key, fallback)
}

func GetenvFallback(getenv func(string) string, key, fallback string) string {
	if value := getenv(key); value != "" {
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

func (r Request) pins() {
	if err := json.NewEncoder(r.Stdout).Encode(map[string]string{
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
		fmt.Fprintln(r.Stderr, err)
		exit(1)
	}
	exit(0)
}

// Shared helpers are forwarded by the executable fixtures, not copied.
func OrkaToken() string                       { return orkaTestToken() }
func OrkaPlural(kind string) string           { return orkaPlural(kind) }
func OrkaAnnotationPointer(key string) string { return orkaAnnotationPointer(key) }
func WorkerController(name, chart, instance, managedBy, args string) string {
	return workerController(name, chart, instance, managedBy, args)
}
func WorkerAccount(name, namespace, chart, instance, trust string) string {
	return workerAccount(name, namespace, chart, instance, trust)
}

const OrkaResultAccount = orkaResultAccount
const OrkaPortableMarker = orkaPortableMarker
const OrkaRenderedMarker = orkaRenderedMarker

type OrkaCall = orkaCall
