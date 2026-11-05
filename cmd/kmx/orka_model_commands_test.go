package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func TestOrkaModelCommandRecognition(t *testing.T) {
	for _, verb := range []string{"connect", "disconnect"} {
		args := []string{"cluster", "orka", "model", verb, "-ojsonl"}
		if got := provisioningJSONInvocation(args); got != "cluster.orka.model."+verb {
			t.Fatalf("recognition %s: %s", verb, got)
		}
		for _, flag := range []string{"--namespace", "--provider", "--connection", "--credential-generation", "--route", "--model"} {
			if got := provisioningJSONInvocation([]string{"cluster", "orka", "model", verb, flag, "-ojsonl"}); got != "" {
				t.Fatal("operand selected output")
			}
		}
	}
}
func TestOrkaModelCommandEarlyErrors(t *testing.T) {
	for _, verb := range []string{"connect", "disconnect"} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		err := execute([]string{"cluster", "orka", "model", verb, "-ojsonl"}, deps)
		if err == nil || *loads != 0 {
			t.Fatalf("missing selectors reached config: %v loads%d", err, *loads)
		}
		r := setupResultRecord(t, out.Bytes(), 1)
		if r["error"].(map[string]any)["code"] != "invalid_arguments" {
			t.Fatal(r)
		}
	}
}
func TestOrkaModelHelpInert(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"cluster", "orka", "model", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 || !strings.Contains(out.String(), "connect") || !strings.Contains(out.String(), "disconnect") || strings.Contains(out.String(), "prove") {
		t.Fatal("help accessed config or advertised proof")
	}
	for _, verb := range []string{"connect", "disconnect"} {
		out.Reset()
		if err := execute([]string{"cluster", "orka", "model", verb, "--help"}, deps); err != nil {
			t.Fatal(err)
		}
		if *loads != 0 {
			t.Fatal("verb help loaded config")
		}
		if verb == "disconnect" && (!strings.Contains(out.String(), "No credential reads") || strings.Contains(out.String(), "Use the fixed guest-memory Copilot credential profile")) {
			t.Fatal("disconnect help claims credential reads", out.String())
		}
	}
}
func TestOrkaModelSetupFields(t *testing.T) {
	var out bytes.Buffer
	m := newProvisioningOutput(&out, "cluster.orka.model.connect", context.Background())
	defer m.stop()
	result := app.OrkaModelResult{Connection: &app.OrkaModelConnection{ID: "11111111-1111-4111-8111-111111111111", Route: "copilot", Provider: "copilot", Model: "gpt-4.1", CredentialGeneration: "22222222-2222-4222-8222-222222222222", State: "unknown"}, RemoteState: "unknown"}
	m.selectSetup(provisioningSelection{mode: "apply", model: &result, deferPreflight: true})
	m.setup.entered = true
	m.beginPreflight()
	m.endPreflight("failed")
	if err := m.finishSetup(app.NewMachineError("execution_unknown", "preflight")); err == nil {
		t.Fatal("failure succeeded")
	}
	r := setupResultRecord(t, out.Bytes(), 1)
	if r["connection"].(map[string]any)["id"] != result.Connection.ID || r["proof"] != nil {
		t.Fatal("result lost connection or invented proof")
	}
}
