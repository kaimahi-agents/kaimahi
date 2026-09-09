package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

// The WASM tool sandbox.
//
// What this is for, stated plainly, because "sandbox" is a word that invites
// overclaiming: it isolates TOOL EXECUTION, not the agent. The agent loop is
// model calls and network waits; the dangerous half is the work a tool
// actually performs, and that is what runs here — as a wasi component on
// wasmtime, with no container runtime involved.
//
// It is also the one isolation path that kagent's CRD does not block.
// `runtimeClassName` is absent from both `declarative.deployment` and
// `byo.deployment`, so an AGENT cannot be placed in a sandbox this way. An
// MCP server is an ordinary Deployment we write ourselves, and the gateway's
// upstream table is a mounted config file — so a sandboxed tool server is
// reachable through configuration rather than a schema change.
//
// The install is a privileged DaemonSet because installing a containerd shim
// means writing a binary onto the node and editing containerd's config, and
// the only portable way to reach a node is a privileged pod. Kaimahi builds
// no shim: containerd-shim-spin and its installer are upstream's, pinned.

// ToolSandbox installs the WASM runtime and reports what it did.
//
// Idempotent: applying the manifests again is a no-op, and the readiness
// waits are what make "installed" mean something rather than "submitted".
func (a *App) ToolSandbox() error {
	if err := a.Guard("install the WASM tool sandbox (privileged node installer)", "tools sandbox"); err != nil {
		return err
	}

	a.notef("Installing the WASM tool sandbox. This writes a containerd shim onto\n" +
		"every Linux node with a PRIVILEGED DaemonSet, and registers a RuntimeClass.\n" +
		"Nothing else in Kaimahi asks for these rights.")

	if err := a.apply("wasm/runtime.yaml"); err != nil {
		return err
	}

	// Rolled out is not the same as installed — but it is now, because the
	// DaemonSet's readiness probe is gated on a marker the installer writes
	// only after it exits 0. Without that probe this wait would return while
	// the shim was still being written, which is exactly the kind of
	// confident-and-wrong reading this command exists to avoid.
	a.notef("waiting for the node installer to finish on every node")
	if err := a.Run.Run("kubectl", a.kubectl("-n", sandboxNamespace,
		"rollout", "status", "ds/spin-node-installer", "--timeout=300s")...); err != nil {
		return fmt.Errorf("the WASM node installer did not roll out: %w\n"+
			"  Its logs say why:  kubectl -n %s logs -l app=spin-node-installer", err, sandboxNamespace)
	}

	// The install itself is now proven by readiness above. This margin is
	// only for the kubelet to observe the containerd the installer restarted
	// out from under it; it is not what makes the shim present.
	time.Sleep(5 * time.Second)

	a.notef("WASM tool sandbox ready. RuntimeClass %q selects it.", sandboxRuntimeClass)
	a.notef("An MCP server opts in with:\n"+
		"    spec.template.spec.runtimeClassName: %s\n"+
		"Then add it to the gateway's upstream table so tool calls are\n"+
		"authenticated, allowlisted and audited on the way through.", sandboxRuntimeClass)
	a.notef("SCOPE: this sandboxes TOOL EXECUTION. The agent still runs as an\n" +
		"ordinary pod — kagent's CRD exposes no runtimeClassName, so an agent\n" +
		"cannot be placed here. What the agent may spend and call is the\n" +
		"plane's job, and stays the plane's job.")
	return nil
}

// ToolSandboxStatus reports whether the sandbox is installed and usable.
//
// Three separate facts, because any one of them can be true while the others
// are not, and "it looked installed" is how a workload ends up Pending with
// no obvious cause.
func (a *App) ToolSandboxStatus() error {
	ui := cliui.New(a.Out)
	var fields []cliui.Field
	// Redirected reports stream each completed read. Rich reports retain the
	// same partial evidence if a later read fails.
	if ui.Rich() {
		fmt.Fprintln(a.Out, ui.Heading("WASM tool sandbox"))
		defer func() { fmt.Fprintln(a.Out, ui.Fields(fields)) }()
	}
	report := func(label, value string) {
		if ui.Rich() {
			fields = append(fields, cliui.Field{Label: label, Value: value})
		} else {
			fmt.Fprintf(a.Out, "%-22s %s\n", label, value)
		}
	}
	class, classErr := a.Run.Capture("kubectl", a.kubectl("get", "runtimeclass",
		sandboxRuntimeClass, "-o", "jsonpath={.handler}")...)
	// A cluster we cannot reach is not a cluster without a sandbox. Both
	// make kubectl exit non-zero, and collapsing them would print a
	// confident "not installed" for a question that was never answered —
	// which is the one failure mode a governance read must not have. An
	// operator would either install what is already there, or conclude
	// their tools are unsandboxed on no evidence at all.
	if unreachable(classErr) {
		return fmt.Errorf("%s\n  %w", sandboxUnknownWording, classErr)
	}
	report("runtimeClass", statusOf(strings.TrimSpace(class), classErr))
	if classErr != nil && !isNotFound(classErr) {
		return fmt.Errorf("cannot read the sandbox: %w", classErr)
	}
	ready, readyErr := a.Run.Capture("kubectl", a.kubectl("-n", sandboxNamespace, "get", "ds",
		"spin-node-installer", "-o", "jsonpath={.status.numberReady}/{.status.desiredNumberScheduled}")...)
	if unreachable(readyErr) {
		return fmt.Errorf("cannot read the node installer: the cluster did not answer.\n  %w", readyErr)
	}
	report("node installer", statusOf(strings.TrimSpace(ready), readyErr))
	if readyErr != nil && !isNotFound(readyErr) {
		return fmt.Errorf("cannot read the node installer: %w", readyErr)
	}

	sandboxed, err := a.Run.Capture("kubectl", a.kubectl("get", "pods", "-A",
		"-o", "jsonpath={range .items[?(@.spec.runtimeClassName=='"+sandboxRuntimeClass+"')]}{.metadata.namespace}/{.metadata.name} {end}")...)
	if unreachable(err) {
		return fmt.Errorf("cannot list sandboxed workloads: the cluster did not answer.\n  %w", err)
	}
	if err != nil {
		report("sandboxed workloads", "unknown - "+firstLine(err.Error()))
		return fmt.Errorf("cannot list sandboxed workloads: %w", err)
	}
	workloads := "none"
	if err == nil && strings.TrimSpace(sandboxed) != "" {
		workloads = strings.TrimSpace(sandboxed)
	}
	report("sandboxed workloads", workloads)
	return nil
}

// sandboxUnknownWording is the message for the state kmx cannot read. It
// names both possibilities because their fixes are opposite: install the
// sandbox, or go find the cluster.
const sandboxUnknownWording = "cannot read the sandbox: the cluster did not answer.\n" +
	"This is not the same as the sandbox being absent — kmx does not know\n" +
	"either way. Check the context is right and the cluster is up:"

// unreachable distinguishes "the API server said no such object" from "there
// was no API server to ask". Capture folds stderr into the error, so the
// classification reads the text kubectl itself printed.
//
// It is deliberately conservative: anything it does not recognise is treated
// as a real answer, because a status command that cried "unreachable" at every
// unfamiliar error would be as useless as one that never did.
func unreachable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sign := range []string{
		"connection refused",
		"could not be reached",
		"unable to connect to the server",
		"couldn't get current server api group list",
		"no such host",
		"i/o timeout",
		"connection timed out",
		"tls handshake timeout",
		"context does not exist",    // a --context naming a cluster kubeconfig lost
		"no configuration has been", // no kubeconfig at all
		"invalid configuration",
		"server has asked for the client to provide credentials",
	} {
		if strings.Contains(msg, sign) {
			return true
		}
	}
	return false
}

func statusOf(value string, err error) string {
	if err != nil && !isNotFound(err) {
		return "unknown - " + firstLine(err.Error())
	}
	if isNotFound(err) {
		return "not installed"
	}
	if value == "" {
		return "unknown - no status returned"
	}
	return value
}

const (
	sandboxNamespace    = "kaimahi-wasm"
	sandboxRuntimeClass = "wasmtime-spin-v2"
)
