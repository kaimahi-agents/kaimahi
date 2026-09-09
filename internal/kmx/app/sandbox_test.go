package app

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// The distinction the sandbox status exists to preserve: a cluster that did
// not answer is not a cluster without a sandbox. Both make kubectl exit
// non-zero, and reporting the second when the first is true would tell an
// operator their tools are unsandboxed on no evidence at all.
func TestUnreachableIsNotTheSameAsAbsent(t *testing.T) {
	unreachableCases := map[string]string{
		"kind cluster stopped": `exit status 1: E0904 memcache.go:265] "Unhandled Error" ` +
			`err="couldn't get current server API group list: Get \"https://127.0.0.1:53049/api\": ` +
			`dial tcp 127.0.0.1:53049: connect: connection refused"`,
		"api server down":     "exit status 1: Unable to connect to the server: dial tcp: connection refused",
		"dns gone":            "exit status 1: Get \"https://api.example.com\": dial tcp: lookup api.example.com: no such host",
		"aks asleep":          "exit status 1: Unable to connect to the server: net/http: TLS handshake timeout",
		"context typo":        `exit status 1: error: context does not exist: kind-typo`,
		"no kubeconfig":       "exit status 1: error: no configuration has been provided, try setting KUBERNETES_MASTER",
		"expired credentials": "exit status 1: error: You must be logged in to the server (the server has asked for the client to provide credentials)",
	}
	for name, msg := range unreachableCases {
		if !unreachable(errors.New(msg)) {
			t.Errorf("%s: should be treated as unreachable, not as 'not installed':\n  %s", name, msg)
		}
	}

	// A real answer from a live API server. These MUST NOT be classified as
	// unreachable, or the command would refuse to report a cluster that
	// answered perfectly well.
	realAnswers := map[string]string{
		"runtimeclass absent": `exit status 1: Error from server (NotFound): runtimeclasses.node.k8s.io "wasmtime-spin-v2" not found`,
		"namespace absent":    `exit status 1: Error from server (NotFound): namespaces "kaimahi-wasm" not found`,
		"daemonset absent":    `exit status 1: Error from server (NotFound): daemonsets.apps "spin-node-installer" not found`,
		"rbac refusal":        `exit status 1: Error from server (Forbidden): runtimeclasses.node.k8s.io is forbidden: User cannot list resource`,
	}
	for name, msg := range realAnswers {
		if unreachable(errors.New(msg)) {
			t.Errorf("%s: the server answered, so this is a real reading:\n  %s", name, msg)
		}
	}

	if unreachable(nil) {
		t.Error("no error is not unreachable")
	}
}

// Conservative by design: an error nobody anticipated is a real answer, not a
// reason to stop reporting. A status command that cried "unreachable" at
// every unfamiliar error would be as useless as one that never did.
func TestUnreachableTreatsUnknownErrorsAsRealAnswers(t *testing.T) {
	if unreachable(errors.New("exit status 1: something nobody predicted")) {
		t.Error("an unrecognised error should not be assumed to be a connection failure")
	}
}

// Only NotFound proves absence. Empty status and refusals are unknown.
func TestStatusOfReportsAbsence(t *testing.T) {
	if got := statusOf("", nil); !strings.Contains(got, "unknown") {
		t.Errorf("empty value: got %q", got)
	}
	if got := statusOf("", errors.New("Error from server (NotFound)")); got != "not installed" {
		t.Errorf("NotFound: got %q", got)
	}
	if got := statusOf("spin", nil); got != "spin" {
		t.Errorf("present: got %q, want the handler name", got)
	}
	for _, message := range []string{"Forbidden", "unexpected failure", "connection refused"} {
		if got := statusOf("", errors.New(message)); !strings.Contains(got, "unknown") || !strings.Contains(got, message) {
			t.Errorf("%s: %q", message, got)
		}
	}
}

// The message has to tell an operator which of the two states they are in,
// because the fixes are opposite: install the sandbox, or go find the
// cluster.
func TestUnreachableMessageSaysKmxDoesNotKnow(t *testing.T) {
	// The wording lives in ToolSandboxStatus; this guards the phrase that
	// carries the meaning.
	const msg = "This is not the same as the sandbox being absent — kmx does not know"
	if !strings.Contains(sandboxUnknownWording, msg) {
		t.Errorf("the unknown-state wording must say kmx does not know, got:\n%s", sandboxUnknownWording)
	}
}

// The manifest grants the only privileged pod in the repo. Two properties of
// it are load-bearing enough to pin.
func TestSandboxManifestKeepsItsSafetyProperties(t *testing.T) {
	manifest, err := os.ReadFile("../../../k8s/wasm/runtime.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(manifest)

	// A mutable tag on a container that runs privileged, with hostPID, and
	// with the node's root filesystem mounted, means whoever controls the
	// tag controls every node in the cluster.
	if !strings.Contains(text, "node-installer@sha256:") {
		t.Error("the privileged installer must be pinned by digest, not by a mutable tag")
	}
	if strings.Contains(text, "node-installer:v") {
		t.Error("a tag reference would be resolved at pull time and is not the pin")
	}

	// The installer runs on Linux nodes only, so only Linux nodes have the
	// shim. Without scheduling constraints a pod naming this class can be
	// placed on a node that cannot run it, and fails at the runtime rather
	// than being steered away by the scheduler.
	// Without a readiness gate the container is Ready the instant it
	// starts, so `rollout status` returns while the installer is still
	// writing the shim — and the command reports a sandbox that is not
	// there yet. Readiness must mean "this node has the shim", not "this
	// node has a pod".
	if !strings.Contains(text, "readinessProbe:") {
		t.Error("the installer must gate readiness on completing, or rollout status proves nothing")
	}
	if !strings.Contains(text, ".kaimahi-installed") {
		t.Error("readiness must test a marker the installer writes only after exiting 0")
	}

	rc := text[strings.Index(text, "kind: RuntimeClass"):]
	if end := strings.Index(rc, "\n---"); end > 0 {
		rc = rc[:end]
	}
	if !strings.Contains(rc, "scheduling:") || !strings.Contains(rc, "kubernetes.io/os: linux") {
		t.Errorf("the RuntimeClass must steer pods to nodes that can run them:\n%s", rc)
	}
}
