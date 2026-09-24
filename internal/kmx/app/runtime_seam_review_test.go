package app

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// The quickstart wizard installs Orka itself and then deploys an Orka agent
// into the namespace it just prepared. Leaving the runtime omitted would
// send the deployment phase back to the cluster to rediscover a platform
// this same command put there.
func TestQuickstartWizardCreateNamesOrkaRatherThanDetectingIt(t *testing.T) {
	create := quickstartWizardCreate(CreateOptions{})
	if create.Runtime != string(agentruntime.Orka) {
		t.Fatalf("Runtime = %q, want %q", create.Runtime, agentruntime.Orka)
	}
	// The wizard's other defaults are unchanged by naming the runtime.
	if create.Namespace != OrkaNamespace || create.ProviderType != "openai" ||
		create.Secret != "kickstart-provider-key" || create.SecretKey != "api-key" {
		t.Fatalf("wizard defaults changed: %+v", create)
	}
}

// An operator's own explicit values still win; only the runtime is imposed,
// because only the runtime is something this wizard already knows.
func TestQuickstartWizardCreatePreservesSuppliedValues(t *testing.T) {
	create := quickstartWizardCreate(CreateOptions{Namespace: "team-a", ProviderType: "anthropic", Secret: "mine", SecretKey: "token"})
	if create.Namespace != "team-a" || create.ProviderType != "anthropic" || create.Secret != "mine" || create.SecretKey != "token" {
		t.Fatalf("supplied values were overwritten: %+v", create)
	}
}

// The point of naming it: a create built by the wizard makes no discovery
// read at all. The fixture reports NO platform installed, so a create that
// still detected would fail instead of deploying — and every recorded call
// is checked, so a discovery read that happened to succeed would fail here
// too.
func TestQuickstartWizardCreateMakesNoDiscoveryRead(t *testing.T) {
	a, base, _, _, dir := orkaCreateFixture(t, "no-platform")
	opt := quickstartWizardCreate(base)
	if err := a.CreateAgent(opt); err != nil {
		t.Fatalf("a wizard-owned Orka create failed: %v", err)
	}
	for _, call := range orkaCalls(t, dir) {
		if slices.Contains(call.Args, "api-resources") {
			t.Fatalf("a wizard-owned create probed for an installed platform: %v", call.Args)
		}
		if slices.Contains(call.Args, "--raw") {
			t.Fatalf("a wizard-owned create read versioned API discovery: %v", call.Args)
		}
	}
}

// `kmx agent create` with no name opens the wizard. Which runtime was named
// is answerable from the registry alone, so it is answered BEFORE the first
// prompt: asking an operator to describe an agent, name it and pick a model
// and only then refusing the runtime they named at the start spends the
// whole session on a create that was never going to run.
//
// Stdin here is not a terminal, so a check running after the wizard's own
// entry conditions would report that instead — which is exactly what makes
// this assertion about ordering rather than about the message.
func TestCreateAgentInteractiveRefusesAnUnsupportedRuntimeBeforePrompting(t *testing.T) {
	var out, diagnostics bytes.Buffer
	a := &App{Out: &out, Err: &diagnostics, Stdin: os.Stdin}
	err := a.CreateAgentInteractive(CreateOptions{Runtime: string(agentruntime.Kagent)})
	var unsupported *agentruntime.UnsupportedVerbError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, not *UnsupportedVerbError", err)
	}
	if unsupported.Runtime != agentruntime.Kagent || unsupported.Verb != agentruntime.VerbRender {
		t.Fatalf("unsupported = %+v", unsupported)
	}
	if out.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatalf("a refused runtime prompted:\nout=%q\nerr=%q", out.String(), diagnostics.String())
	}
}

// A runtime kmx names but does not implement declines the same way, and an
// ID it does not name at all stays unknown.
func TestCreateAgentInteractiveTypesUnimplementedAndUnknownRuntimes(t *testing.T) {
	t.Run("kagent-v1 declines render", func(t *testing.T) {
		var out, diagnostics bytes.Buffer
		a := &App{Out: &out, Err: &diagnostics, Stdin: os.Stdin}
		err := a.CreateAgentInteractive(CreateOptions{Runtime: string(agentruntime.KagentV1)})
		var unsupported *agentruntime.UnsupportedVerbError
		if !errors.As(err, &unsupported) || unsupported.Runtime != agentruntime.KagentV1 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an unnamed runtime stays unknown", func(t *testing.T) {
		var out, diagnostics bytes.Buffer
		a := &App{Out: &out, Err: &diagnostics, Stdin: os.Stdin}
		err := a.CreateAgentInteractive(CreateOptions{Runtime: "bogus"})
		var unknown *agentruntime.UnknownRuntimeError
		if !errors.As(err, &unknown) || string(unknown.Runtime) != "bogus" {
			t.Fatalf("err = %v", err)
		}
	})
}

// An omitted or Orka runtime is still the wizard's own path: the early check
// must not turn "no --runtime" into a refusal. With stdin that is not a
// terminal, reaching the wizard's entry conditions is what proves the
// runtime check let it through.
func TestCreateAgentInteractivePreservesTheOmittedAndOrkaWizard(t *testing.T) {
	for _, runtime := range []string{"", string(agentruntime.Orka)} {
		t.Run("runtime "+runtime, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			a := &App{Out: &out, Err: &diagnostics, Stdin: os.Stdin}
			err := a.CreateAgentInteractive(CreateOptions{Runtime: runtime})
			if err == nil || !strings.Contains(err.Error(), "non-interactive input") {
				t.Fatalf("err = %v, want the wizard's own entry condition", err)
			}
			var unsupported *agentruntime.UnsupportedVerbError
			var unknown *agentruntime.UnknownRuntimeError
			if errors.As(err, &unsupported) || errors.As(err, &unknown) {
				t.Fatalf("the wizard refused a runtime it supports: %v", err)
			}
		})
	}
}

// Orka's Evaluate is permanently unsupported (DESIGN.md §3), so it returns
// the one shared typed error. There is no second "not yet implemented"
// message behind it: a caller that recovered the typed error would otherwise
// be one capability flag away from an implementation that does not exist.
func TestOrkaEvaluateIsTheSharedUnsupportedVerbError(t *testing.T) {
	adapter := orkaRuntimeAdapter{app: &App{}}
	receipt, err := adapter.Evaluate(t.Context(), agentruntime.AgentRef{Namespace: "orka-system", Name: "sample"}, agentruntime.EvaluationRequest{})
	var unsupported *agentruntime.UnsupportedVerbError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, not *UnsupportedVerbError", err)
	}
	if unsupported.Runtime != agentruntime.Orka || unsupported.Verb != agentruntime.VerbEvaluate {
		t.Fatalf("unsupported = %+v", unsupported)
	}
	if err.Error() != "runtime orka does not support evaluate" {
		t.Fatalf("message = %q", err.Error())
	}
	if receipt != (agentruntime.EvaluationReceipt{}) {
		t.Fatal("a declined evaluate returned a receipt")
	}
}

// A create-configured adapter declares Render and Deploy, and still declines
// Evaluate: the refusal is about the verb, not about how the adapter was
// built.
func TestOrkaEvaluateIsDeclinedEvenWhenConfiguredByACreate(t *testing.T) {
	opt := CreateOptions{Name: "sample", Namespace: "orka-system"}
	adapter := orkaRuntimeAdapter{app: &App{}, create: &opt}
	if !adapter.Capabilities().Render || adapter.Capabilities().Evaluate {
		t.Fatalf("capabilities = %+v", adapter.Capabilities())
	}
	if _, err := adapter.Evaluate(t.Context(), agentruntime.AgentRef{}, agentruntime.EvaluationRequest{}); err == nil {
		t.Fatal("a configured adapter accepted evaluate")
	}
}
