package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestTargetsCapabilitiesMatchLifecycleDeclarations(t *testing.T) {
	a := &App{Out: &bytes.Buffer{}}
	if err := a.Targets(TargetsOptions{Output: "json"}); err != nil {
		t.Fatal(err)
	}
	var report TargetsReport
	if err := json.Unmarshal(a.Out.(*bytes.Buffer).Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for i, adapter := range []agentruntime.LifecycleAdapter{
		kagentRuntimeAdapter{create: &CreateOptions{}, bindings: &agentruntime.KagentBindings{}},
		orkaRuntimeAdapter{create: &CreateOptions{}},
	} {
		caps := adapter.Capabilities()
		want := map[string]bool{"render": caps.Render, "deploy": caps.Deploy, "status": caps.Status, "evaluate": caps.Evaluate}
		for _, got := range report.Targets[i+1].Capabilities {
			if supported, ok := want[got.Operation]; ok && got.Supported != supported {
				t.Fatalf("%s %s: report=%t adapter=%t", adapter.ID(), got.Operation, got.Supported, supported)
			}
		}
	}
}

func TestTargetsOrkaDetectionIsScopedReadOnlyAndFailClosed(t *testing.T) {
	controller := func(containers string) string {
		return `{"metadata":{"name":"controller","labels":{"app.kubernetes.io/name":"orka","app.kubernetes.io/component":"controller"}},"spec":{"template":{"spec":{"containers":` + containers + `}}}}`
	}
	pinned := controller(`[{"name":"sidecar","image":"example.test/sidecar:v9"},{"name":"controller","image":"ghcr.io/orka-agents/orka@` + orkaControllerDigest + `"}]`)
	for _, tc := range []struct {
		name, response, state, version, code string
		exit                                 string
	}{
		{name: "absent", response: `{"kind":"DeploymentList","items":[]}`, state: "absent"},
		{name: "pinned with sidecar", response: `{"kind":"DeploymentList","items":[` + pinned + `]}`, state: "present", version: "v0.2.0"},
		{name: "tag is not qualification", response: `{"kind":"DeploymentList","items":[` + controller(`[{"name":"controller","image":"ghcr.io/orka-agents/orka:v0.2.0"}]`) + `]}`, state: "present"},
		{name: "foreign digest", response: `{"kind":"DeploymentList","items":[` + controller(`[{"name":"controller","image":"example.test/foreign@`+orkaControllerDigest+`"}]`) + `]}`, state: "present"},
		{name: "ambiguous", response: `{"kind":"DeploymentList","items":[` + pinned + `,` + pinned + `]}`, state: "unreadable", code: "ambiguous"},
		{name: "invalid JSON", response: `PRIVATE-REMOTE-PAYLOAD`, state: "unreadable", code: "malformed"},
		{name: "missing list", response: `{}`, state: "unreadable", code: "malformed"},
		{name: "null list", response: `{"kind":"DeploymentList","items":null}`, state: "unreadable", code: "malformed"},
		{name: "wrong resource", response: `{"kind":"SecretList","items":[]}`, state: "unreadable", code: "malformed"},
		{name: "read error", response: `{"kind":"DeploymentList","items":[]}`, state: "unreadable", code: "read_failed", exit: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls")
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$KMX_TARGET_CALLS\"\nprintf '%s' \"$KMX_TARGET_RESPONSE\"\nprintf 'PRIVATE-REMOTE-PAYLOAD' >&2\nexit \"${KMX_TARGET_EXIT:-0}\"\n"
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			t.Setenv("KMX_HOME", filepath.Join(dir, "state"))
			t.Setenv("KMX_TARGET_CALLS", calls)
			t.Setenv("KMX_TARGET_RESPONSE", tc.response)
			t.Setenv("KMX_TARGET_EXIT", tc.exit)
			var out, diagnostics bytes.Buffer
			a := &App{Cfg: &config.Config{KubeContext: "kind-test"}, Run: &run.Runner{}, Out: &out, Err: &diagnostics}
			err := a.Targets(TargetsOptions{Output: "json", Detect: true})
			var unreadable *TargetsUnreadableError
			if (tc.state == "unreadable") != errors.As(err, &unreadable) {
				t.Fatalf("state=%s err=%v", tc.state, err)
			}
			var report TargetsReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			d := report.Targets[2].Detection
			if d.State != tc.state || d.Version != tc.version || tc.code != "" && (d.Error == nil || d.Error.Code != tc.code) {
				t.Fatalf("detection=%+v err=%v", d, err)
			}
			if report.Targets[1].Detection.State != "not-probed" {
				t.Fatal("Kagent discovery was restored")
			}
			if strings.Contains(out.String()+diagnostics.String(), "PRIVATE-REMOTE-PAYLOAD") {
				t.Fatal("remote payload leaked")
			}
			raw, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != "--context kind-test --request-timeout=10s -n orka-system get deploy -o json\n" {
				t.Fatalf("unexpected command or mutation: %s", raw)
			}
			if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
				t.Fatalf("read-only view wrote state: %v", err)
			}
		})
	}
}

func TestTargetsTableCarriesSameDetectionAndRefusalsAsJSON(t *testing.T) {
	var table, raw bytes.Buffer
	for _, tc := range []struct {
		output string
		out    *bytes.Buffer
	}{{"table", &table}, {"json", &raw}} {
		if err := (&App{Out: tc.out}).Targets(TargetsOptions{Output: tc.output}); err != nil {
			t.Fatal(err)
		}
	}
	var report TargetsReport
	if err := json.Unmarshal(raw.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for _, row := range report.Targets {
		for _, want := range []string{row.ID, row.SupportTier, row.Detection.State} {
			if !strings.Contains(table.String(), want) {
				t.Fatalf("table missing %q", want)
			}
		}
		for _, c := range row.Capabilities {
			if !c.Supported && !strings.Contains(table.String(), c.Reason) {
				t.Fatalf("table missing refusal %q", c.Reason)
			}
		}
	}
}
