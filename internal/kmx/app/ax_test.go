package app

import (
	"bytes"
	"strings"
	"testing"
)

const axReadyDeployments = `{"kind":"DeploymentList","items":[
  {"metadata":{"name":"ax-controller","generation":4},"spec":{"replicas":1,"template":{"spec":{"containers":[{"image":"registry.example/ax-controller@sha256:111","args":["--substrate-endpoint=api.ate-system.svc.cluster.local:443"],"env":[{"name":"ATENET_ROUTER_ADDR","value":"atenet-router.ate-system.svc.cluster.local:80"}]}]}}},"status":{"observedGeneration":4,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":0}},
  {"metadata":{"name":"ax-server","generation":2},"spec":{"replicas":1,"template":{"spec":{"containers":[{"image":"registry.example/ax-server@sha256:222"}]}}},"status":{"observedGeneration":2,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":0}},
  {"metadata":{"name":"ax-redis","generation":1},"spec":{"replicas":1,"template":{"spec":{"containers":[{"image":"redis@sha256:333"}]}}},"status":{"observedGeneration":1,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":0}}
]}`

func axTestOutput(t *testing.T, script string) (*App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("KMX_TOOLCHAIN", "off")
	a := appWithKubectl(t, script)
	out, ok := a.Out.(*bytes.Buffer)
	if !ok {
		t.Fatalf("fixture output is %T, want *bytes.Buffer", a.Out)
	}
	return a, out
}

// A namespace that is genuinely absent is a real answer from the API server,
// not an error. It says nothing about Substrate: without an AX controller
// there is no configured endpoint for this view to inspect.
func TestAXStatusReportsAnAbsentAXWithoutInventingAnAbsentSubstrate(t *testing.T) {
	a, out := axTestOutput(t, `case "$*" in
  *"-n ax-system get deploy -o json"*) printf 'Error from server (NotFound): namespaces "ax-system" not found\n' >&2; exit 1 ;;
  *"-n ax-system get svc -o json"*) printf 'Error from server (NotFound): namespaces "ax-system" not found\n' >&2; exit 1 ;;
esac
exit 0`)

	if err := a.AXStatus(AXNamespace); err != nil {
		t.Fatalf("status: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"evaluation only — kmx does not install AX",
		"AX deployments", "not detected in namespace ax-system",
		"there is no `kmx ax install`",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status does not contain %q:\n%s", want, text)
		}
	}
}

// A cluster that did not answer is not a cluster without AX. Those states
// have opposite fixes, and status must refuse to collapse them.
func TestAXStatusSeparatesUnreadableFromAbsent(t *testing.T) {
	a, out := axTestOutput(t, `case "$*" in
  *"-n ax-system get deploy -o json"*) printf 'Unable to connect to the server: dial tcp: i/o timeout\n' >&2; exit 1 ;;
esac
exit 0`)

	err := a.AXStatus(AXNamespace)
	if err == nil || !strings.Contains(err.Error(), "not the same as AX being absent") {
		t.Fatalf("an unreadable cluster was not refused distinctly: %v", err)
	}
	if strings.Contains(out.String(), "not detected") {
		t.Fatalf("an unreadable AX was reported as absent:\n%s", out.String())
	}
}

// A Forbidden is a real answer from a live API server, but not an answer
// about presence. It must remain unknown rather than becoming absent.
func TestAXStatusDoesNotTurnRBACDenialIntoAbsence(t *testing.T) {
	a, out := axTestOutput(t, `case "$*" in
  *"-n ax-system get deploy -o json"*) printf 'Error from server (Forbidden): deployments.apps is forbidden\n' >&2; exit 1 ;;
esac
exit 0`)

	err := a.AXStatus(AXNamespace)
	if err == nil || !strings.Contains(err.Error(), "state is unknown") {
		t.Fatalf("RBAC denial was not reported as unknown: %v", err)
	}
	if strings.Contains(out.String(), "not detected") {
		t.Fatalf("RBAC denial was reported as absence:\n%s", out.String())
	}
}

// Status reports the image templates the Deployments declare, not the source
// baseline kmx read while implementing the view. AX uses operator-built
// images, so restating a tag would invent a version nobody deployed. The label
// deliberately does not say "running": during a failed rollout the ready pod
// may still be on the old template.
func TestAXStatusReportsDeclaredImagesAndControllerConnections(t *testing.T) {
	a, out := axTestOutput(t, `case "$*" in
  *"-n ax-system get deploy -o json"*) printf '%s' '`+axReadyDeployments+`'; exit 0 ;;
  *"-n ax-system get svc -o json"*) printf '%s' '{"kind":"ServiceList","items":[{"metadata":{"name":"ax-server"}},{"metadata":{"name":"ax-redis"}}]}'; exit 0 ;;
  *"-n ate-system get svc api -o name"*) printf '%s' 'service/api'; exit 0 ;;
  *"-n ate-system get svc atenet-router -o name"*) printf '%s' 'service/atenet-router'; exit 0 ;;
esac
exit 0`)

	if err := a.AXStatus(AXNamespace); err != nil {
		t.Fatalf("status: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"ax-controller=1/1 ready", "ax-server=1/1 ready", "ax-redis=1/1 ready",
		"image templates declared",
		"registry.example/ax-controller@sha256:111",
		"registry.example/ax-server@sha256:222", "redis@sha256:333",
		"api.ate-system.svc.cluster.local:443",
		"atenet-router.ate-system.svc.cluster.local:80",
		"ate-system/api present", "ate-system/atenet-router present",
		"Ready components do not prove a sandboxed Task",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status does not contain %q:\n%s", want, text)
		}
	}
}

// A ready count can belong to the old ReplicaSet while a replacement fails.
// Generation and updated/available counts carry that distinction.
func TestAXStatusDoesNotCallAnOldReadyReplicaRolledOut(t *testing.T) {
	deployments := strings.Replace(axReadyDeployments,
		`"observedGeneration":4,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":0`,
		`"observedGeneration":3,"updatedReplicas":0,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":1`, 1)
	a, out := axTestOutput(t, `case "$*" in
  *"-n ax-system get deploy -o json"*) printf '%s' '`+deployments+`'; exit 0 ;;
  *"-n ax-system get svc -o json"*) printf '%s' '{"kind":"ServiceList","items":[]}'; exit 0 ;;
  *"-n ate-system get svc "*) printf 'Error from server (NotFound): services "x" not found\n' >&2; exit 1 ;;
esac
exit 0`)

	if err := a.AXStatus(AXNamespace); err != nil {
		t.Fatalf("status: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "generation 4 not observed") {
		t.Fatalf("old ready replica was presented as a finished rollout:\n%s", text)
	}
	if strings.Contains(text, "images running") {
		t.Fatalf("a Deployment template was mislabeled as the running pod image:\n%s", text)
	}
	if !strings.Contains(text, "ate-system/api missing") || !strings.Contains(text, "ate-system/atenet-router missing") {
		t.Fatalf("missing runtime prerequisites were not visible:\n%s", text)
	}
}

// AX itself makes the namespace configurable. The status view must follow the
// operator's choice rather than declaring a valid installation absent because
// it only looked in ax-system.
func TestAXStatusUsesTheSelectedNamespace(t *testing.T) {
	a, out := axTestOutput(t, `case "$*" in
  "version --client") exit 0 ;;
  *"-n evaluation get deploy -o json"*) printf '%s' '`+axReadyDeployments+`'; exit 0 ;;
  *"-n evaluation get svc -o json"*) printf '%s' '{"kind":"ServiceList","items":[{"metadata":{"name":"ax-server"}},{"metadata":{"name":"ax-redis"}}]}'; exit 0 ;;
  *"-n ate-system get svc api -o name"*) printf '%s' 'service/api'; exit 0 ;;
  *"-n ate-system get svc atenet-router -o name"*) printf '%s' 'service/atenet-router'; exit 0 ;;
  *) printf 'wrong namespace or unbounded read: %s\n' "$*" >&2; exit 1 ;;
esac`)

	if err := a.AXStatus("evaluation"); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "ax-controller=1/1 ready") {
		t.Fatalf("selected namespace was not read:\n%s", out.String())
	}
}

// A valid AX controller can target Substrate outside Kubernetes DNS. In that
// case status must not check the stock ate-system Services and imply they are
// the runtime this controller uses.
func TestAXStatusDoesNotCheckStockServicesForExternalSubstrate(t *testing.T) {
	deployments := strings.ReplaceAll(axReadyDeployments,
		"api.ate-system.svc.cluster.local:443", "substrate.example.com:443")
	deployments = strings.ReplaceAll(deployments,
		"atenet-router.ate-system.svc.cluster.local:80", "router.example.com:80")
	a, out := axTestOutput(t, `case "$*" in
  *"-n ax-system get deploy -o json"*) printf '%s' '`+deployments+`'; exit 0 ;;
  *"-n ax-system get svc -o json"*) printf '%s' '{"kind":"ServiceList","items":[{"metadata":{"name":"ax-server"}},{"metadata":{"name":"ax-redis"}}]}'; exit 0 ;;
  *"-n ate-system"*) printf 'stock Service should not have been queried\n' >&2; exit 1 ;;
esac
exit 0`)

	if err := a.AXStatus(AXNamespace); err != nil {
		t.Fatalf("status: %v", err)
	}
	if got := out.String(); strings.Count(got, "external or non-Service endpoint") != 2 {
		t.Fatalf("external endpoints were not reported honestly:\n%s", got)
	}
}

// A two-label hostname is ambiguous: it might be Kubernetes short DNS, or it
// might be an ordinary public domain. Treating example.com as service
// example in namespace com would turn a valid external endpoint into a false
// missing-prerequisite report.
func TestAXStatusDoesNotGuessThatATwoLabelHostIsAService(t *testing.T) {
	deployments := strings.ReplaceAll(axReadyDeployments,
		"api.ate-system.svc.cluster.local:443", "example.com:443")
	deployments = strings.ReplaceAll(deployments,
		"atenet-router.ate-system.svc.cluster.local:80", "router.example:80")
	a, out := axTestOutput(t, `case "$*" in
  *"-n ax-system get deploy -o json"*) printf '%s' '`+deployments+`'; exit 0 ;;
  *"-n ax-system get svc -o json"*) printf '%s' '{"kind":"ServiceList","items":[]}'; exit 0 ;;
  *"-n com get svc"*|*"-n example get svc"*) printf 'ambiguous host was queried as a Service\n' >&2; exit 1 ;;
esac
exit 0`)

	if err := a.AXStatus(AXNamespace); err != nil {
		t.Fatalf("status: %v", err)
	}
	if got := out.String(); strings.Count(got, "external or non-Service endpoint") != 2 {
		t.Fatalf("ambiguous names were asserted to be Services:\n%s", got)
	}
}

// Endpoint text comes from an externally owned Deployment. It must never be
// able to become a kubectl flag and override the context kmx selected.
func TestAXStatusDoesNotPassDeploymentTextAsKubectlFlags(t *testing.T) {
	deployments := strings.Replace(axReadyDeployments,
		"api.ate-system.svc.cluster.local:443", "--context=prod.ate-system.svc:443", 1)
	a, out := axTestOutput(t, `case "$*" in
  *"--context=prod"*) printf 'deployment text became a kubectl flag\n' >&2; exit 1 ;;
  *"-n ax-system get deploy -o json"*) printf '%s' '`+deployments+`'; exit 0 ;;
  *"-n ax-system get svc -o json"*) printf '%s' '{"kind":"ServiceList","items":[]}'; exit 0 ;;
  *"-n ate-system get svc atenet-router -o name"*) printf '%s' 'service/atenet-router'; exit 0 ;;
esac
exit 0`)

	if err := a.AXStatus(AXNamespace); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "invalid Kubernetes Service reference — not queried") {
		t.Fatalf("unsafe Service reference was not refused:\n%s", out.String())
	}
}

// Go's flag package accepts both --flag=value and --flag value. AX uses that
// parser, so status has to understand both spellings from the Deployment.
func TestAXStatusReadsSeparateEndpointFlagValue(t *testing.T) {
	deployments := strings.Replace(axReadyDeployments,
		`"--substrate-endpoint=api.ate-system.svc.cluster.local:443"`,
		`"--substrate-endpoint","api.ate-system.svc.cluster.local:443"`, 1)
	a, out := axTestOutput(t, `case "$*" in
  *"-n ax-system get deploy -o json"*) printf '%s' '`+deployments+`'; exit 0 ;;
  *"-n ax-system get svc -o json"*) printf '%s' '{"kind":"ServiceList","items":[]}'; exit 0 ;;
  *"-n ate-system get svc api -o name"*) printf '%s' 'service/api'; exit 0 ;;
  *"-n ate-system get svc atenet-router -o name"*) printf '%s' 'service/atenet-router'; exit 0 ;;
esac
exit 0`)

	if err := a.AXStatus(AXNamespace); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "ate-system/api present") {
		t.Fatalf("separate endpoint flag value was not parsed:\n%s", out.String())
	}
}

// Every diagnostic read is bounded. A status command invoked during an API
// outage must eventually report unknown rather than hang indefinitely.
func TestAXStatusBoundsEveryKubectlRead(t *testing.T) {
	a, _ := axTestOutput(t, `case "$*" in
  "version --client") exit 0 ;;
  *"--request-timeout=15s"*) ;;
  *) printf 'unbounded read: %s\n' "$*" >&2; exit 1 ;;
esac
case "$*" in
  *"-n ax-system get deploy -o json"*) printf '%s' '`+axReadyDeployments+`' ;;
  *"-n ax-system get svc -o json"*) printf '%s' '{"kind":"ServiceList","items":[]}' ;;
  *"-n ate-system get svc "*) printf '%s' 'service/x' ;;
esac`)

	if err := a.AXStatus(AXNamespace); err != nil {
		t.Fatalf("status used an unbounded read: %v", err)
	}
}

// Malformed output means the API server answered but kmx could not interpret
// it. Calling that a non-response points the operator at networking instead of
// the actual problem, so the two failure shapes stay distinct.
func TestAXStatusDoesNotCallMalformedServiceJSONANonResponse(t *testing.T) {
	a, _ := axTestOutput(t, `case "$*" in
  *"-n ax-system get deploy -o json"*) printf '%s' '`+axReadyDeployments+`'; exit 0 ;;
  *"-n ate-system get svc "*) printf '%s' 'service/x'; exit 0 ;;
  *"-n ax-system get svc -o json"*) printf '%s' '{not-json'; exit 0 ;;
esac
exit 0`)

	err := a.AXStatus(AXNamespace)
	if err == nil || !strings.Contains(err.Error(), "cannot decode Services") {
		t.Fatalf("malformed JSON did not preserve its cause: %v", err)
	}
	if strings.Contains(err.Error(), "cluster did not answer") {
		t.Fatalf("a decode failure was mislabeled as a network failure: %v", err)
	}
}

// A successful kubectl with no Kubernetes List is malformed output, not an
// empty population. Empty, null and an object with no kind must all stay
// unknown rather than becoming "AX absent" or "Services missing".
func TestAXStatusRequiresKubernetesListEnvelopes(t *testing.T) {
	for _, payload := range []string{"", "null", `{}`} {
		t.Run("deployments_"+strings.ReplaceAll(payload, " ", "_"), func(t *testing.T) {
			a, out := axTestOutput(t, `case "$*" in
  *"-n ax-system get deploy -o json"*) printf '%s' '`+payload+`'; exit 0 ;;
esac
exit 0`)
			err := a.AXStatus(AXNamespace)
			if err == nil || !strings.Contains(err.Error(), "expected Kubernetes DeploymentList") && !strings.Contains(err.Error(), "cannot decode AX deployments") {
				t.Fatalf("malformed deployment list became absence: %v", err)
			}
			if strings.Contains(out.String(), "not detected") {
				t.Fatalf("malformed deployment output was reported as absence:\n%s", out.String())
			}
		})
	}

	for _, payload := range []string{"", "null", `{}`} {
		t.Run("services_"+strings.ReplaceAll(payload, " ", "_"), func(t *testing.T) {
			a, _ := axTestOutput(t, `case "$*" in
  *"-n ax-system get deploy -o json"*) printf '%s' '`+axReadyDeployments+`'; exit 0 ;;
  *"-n ate-system get svc "*) printf '%s' 'service/x'; exit 0 ;;
  *"-n ax-system get svc -o json"*) printf '%s' '`+payload+`'; exit 0 ;;
esac
exit 0`)
			err := a.AXStatus(AXNamespace)
			if err == nil || !strings.Contains(err.Error(), "ServiceList") && !strings.Contains(err.Error(), "cannot decode Services") {
				t.Fatalf("malformed service list became missing Services: %v", err)
			}
		})
	}
}

func TestAXStatusRefusesAnUnsafeNamespaceBeforeKubectl(t *testing.T) {
	a, _ := axTestOutput(t, `printf 'kubectl must not run\n' >&2; exit 1`)
	err := a.AXStatus("--context=prod")
	if err == nil || !strings.Contains(err.Error(), "not a Kubernetes DNS label") {
		t.Fatalf("unsafe namespace was accepted: %v", err)
	}
}

// AXStatus is intentionally a view over an externally owned installation.
// Keep the source baseline explicit so a future bump is a reviewed edit, not
// an accidental claim that AX main is compatible today.
func TestAXEvaluationBaselineIsImmutableAndVersioned(t *testing.T) {
	if AXEvaluationVersion == "" || AXEvaluationRevision == "" {
		t.Fatal("AX evaluation baseline is unnamed")
	}
	if len(AXEvaluationRevision) != 40 {
		t.Fatalf("AX evaluation revision %q is not a full commit", AXEvaluationRevision)
	}
	if strings.Contains(AXEvaluationVersion, "latest") || AXEvaluationVersion == "main" {
		t.Fatalf("AX evaluation baseline is mutable: %q", AXEvaluationVersion)
	}
}
