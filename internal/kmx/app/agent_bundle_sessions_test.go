package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These refusals must happen before kubectl or a Sessions connection is needed.
func TestSessionsEvaluationSelection(t *testing.T) {
	for _, tc := range []struct {
		name string
		opt  EvaluateAgentBundleOptions
		want string
	}{
		{"context", EvaluateAgentBundleOptions{Sessions: "127.0.0.1:8080", ToContext: "test"}, "--sessions cannot be combined with --to-context"},
		{"result port", EvaluateAgentBundleOptions{Sessions: "127.0.0.1:8080", ResultPort: "19180"}, "--result-port is only for Orka"},
		{"CA without sessions", EvaluateAgentBundleOptions{SessionsCA: "ca.pem"}, "--sessions-ca requires --sessions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (&App{Out: &bytes.Buffer{}}).EvaluateAgentBundle(tc.opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestSessionsReceiptPrivacyAndIdentity(t *testing.T) {
	bundle := t.TempDir()
	r := sessionsEvaluationReceipt{
		Bundle: "sample", PortableDigest: strings.Repeat("a", 64), CasesDigest: strings.Repeat("b", 64), FullCaseSet: true,
		Target: sessionsEvaluationTarget{Runtime: "agentsessions", Identity: sessionsHostIdentity{Version: 1, Provenance: "host-reported", Address: "127.0.0.1:8080", Harness: "chat", Model: "test-model"}},
		Result: "pass", Cases: []sessionsEvaluationResult{{ID: "one", Verdict: "pass", SessionUID: "session-1", Model: "test-model", JournalHead: sessionsJournalHead{Seq: 5, Hash: strings.Repeat("c", 64)}, AnswerSHA256: strings.Repeat("d", 64)}},
	}
	path, err := writeSessionsEvaluationReceipt(bundle, r)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	target := got["target"].(map[string]any)
	if target["runtime"] != "agentsessions" {
		t.Fatalf("target: %v", target)
	}
	identity := target["identity"].(map[string]any)
	if identity["version"] != float64(1) || identity["provenance"] != "host-reported" || identity["model"] != "test-model" {
		t.Fatalf("identity: %v", identity)
	}
	for _, key := range []string{"clusterUID", "agentUID", "context", "namespace", "descriptor"} {
		if _, ok := target[key]; ok {
			t.Errorf("invented identity field %s", key)
		}
	}
	for _, key := range []string{"input", "instructions", "config", "answer", "matched", "missing", "tools", "messages"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("payload field %s in receipt", key)
		}
	}
	legacy := bundleEvaluationReceiptPath(bundle, "127.0.0.1:8080", "chat", "test-model")
	if path == legacy {
		t.Fatal("sessions receipt shares Orka filename identity")
	}
	for _, check := range []struct {
		path string
		mode os.FileMode
	}{{path, 0600}, {filepath.Dir(path), 0700}} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != check.mode {
			t.Errorf("%s mode %o, want %o", check.path, info.Mode().Perm(), check.mode)
		}
	}
	// Atomic replacement must not follow an existing receipt symlink.
	canary := filepath.Join(bundle, "private-canary")
	if err := os.WriteFile(canary, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(canary, path); err != nil {
		t.Fatal(err)
	}
	if _, err := writeSessionsEvaluationReceipt(bundle, r); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(canary); err != nil || string(raw) != "unchanged" {
		t.Fatalf("receipt writer followed link: %s %v", raw, err)
	}
}

func TestSessionsReceiptRefusesLinkedDirectory(t *testing.T) {
	bundle := t.TempDir()
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(bundle, "receipts")); err != nil {
		t.Fatal(err)
	}
	_, err := writeSessionsEvaluationReceipt(bundle, sessionsEvaluationReceipt{})
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("linked receipts accepted: %v", err)
	}
	entries, err := os.ReadDir(other)
	if err != nil || len(entries) != 0 {
		t.Fatalf("linked directory changed: %v %v", entries, err)
	}
}

func TestSessionsUnsupportedPortableBehavior(t *testing.T) {
	base := "apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata:\n  name: sample\nspec:\n  instructions: exact instructions\n  model:\n    name: test-model\n"
	for _, tc := range []struct{ name, source, want string }{
		{"core coordination", base + "  coordination:\n    allowedAgents:\n      - name: helper\n", "spec.coordination"},
		{"tools", base + "extensions:\n  orka:\n    apiVersion: core.orka.ai/v1alpha1\n    agent:\n      tools:\n        - name: inventory\n", "extensions.orka.agent.tools"},
		{"skills", base + "extensions:\n  orka:\n    apiVersion: core.orka.ai/v1alpha1\n    agent:\n      skills:\n        - name: skill\n", "extensions.orka.agent.skills"},
		{"provider limits", base + "extensions:\n  orka:\n    apiVersion: core.orka.ai/v1alpha1\n    provider:\n      rateLimit:\n        requestsPerMinute: 2\n", "extensions.orka.provider.rateLimit"},
		{"agent limits", base + "extensions:\n  orka:\n    apiVersion: core.orka.ai/v1alpha1\n    agent:\n      rateLimit:\n        requestsPerMinute: 2\n", "extensions.orka.agent.rateLimit"},
		{"disabled coordination", base + "extensions:\n  orka:\n    apiVersion: core.orka.ai/v1alpha1\n    agent:\n      coordination:\n        enabled: false\n", "extensions.orka.agent.coordination"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := t.TempDir()
			if err := os.WriteFile(filepath.Join(bundle, "agent.yaml"), []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			_, _, _, err := readSessionsEvaluationSource(bundle)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want unsupported %s, got %v", tc.want, err)
			}
		})
	}
}
