package app

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestSessionsReceiptSizePreflight(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(fmt.Sprintf("selected=%t", selected), func(t *testing.T) {
			s := &sessionsEvalServer{model: "test-model", answers: map[string]string{"hello": "hello"}}
			a, opt, _ := newSessionsAppFixture(t, s)
			for _, name := range []string{"a.yaml", "b.yaml"} {
				if err := os.Remove(filepath.Join(opt.BundleDir, "eval", name)); err != nil {
					t.Fatal(err)
				}
			}
			for name, body := range oversizedEvaluationSuite() {
				if err := os.WriteFile(filepath.Join(opt.BundleDir, "eval", name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if selected {
				opt.Case = "case-0"
			}
			err := a.EvaluateAgentBundle(opt)
			s.mu.Lock()
			creates, execs := len(s.creates), len(s.execs)
			s.mu.Unlock()
			if selected {
				if err != nil || creates != 1 || execs != 1 {
					t.Fatalf("single case: err=%v creates=%d execs=%d", err, creates, execs)
				}
				r, _ := readSessionsAppReceipt(t, opt.BundleDir)
				if r.FullCaseSet || len(r.Cases) != 1 || len(r.Cases[0].Assertions) != 1000 {
					t.Fatalf("wrong selected evidence: %+v", r)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "receipt") || !strings.Contains(err.Error(), "1 MiB") || !strings.Contains(err.Error(), "at most 231 cases with one assertion each") || !strings.Contains(err.Error(), "--case <id> to run one case") || strings.Contains(err.Error(), "finished") {
				t.Errorf("want receipt-size preflight refusal with case limit and single-case guidance, got %v", err)
			}
			if creates != 0 || execs != 0 {
				t.Errorf("oversized evaluation mutated host: creates=%d execs=%d", creates, execs)
			}
			if _, err := os.Stat(filepath.Join(opt.BundleDir, "receipts")); !os.IsNotExist(err) {
				t.Errorf("refused evaluation changed receipts: %v", err)
			}
		})
	}
}

func TestSessionsReceiptSizePreflightPrecedesDial(t *testing.T) {
	a, opt, _ := newSessionsAppFixture(t, &sessionsEvalServer{})
	for name, body := range oversizedEvaluationSuite() {
		if err := os.WriteFile(filepath.Join(opt.BundleDir, "eval", name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Invalid transport configuration exposes any attempt to dial first.
	opt.Sessions = "not-an-endpoint"
	err := a.EvaluateAgentBundle(opt)
	if err == nil || !strings.Contains(err.Error(), "receipt") || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("dial preceded size refusal: %v", err)
	}
}

func TestSessionsKnownMetadataPreflightPrecedesDial(t *testing.T) {
	for _, field := range []string{"model", "escaped model", "endpoint"} {
		t.Run(field, func(t *testing.T) {
			a, opt, _ := newSessionsAppFixture(t, &sessionsEvalServer{})
			opt.Case = "one"
			opt.Sessions = "not-an-endpoint"
			model := strings.Repeat("x", maxSessionsReceiptBytes)
			if field == "escaped model" {
				model = strings.Repeat("<", maxSessionsReceiptBytes/6)
			}
			if field == "endpoint" {
				opt.Sessions = strings.Repeat("<", maxSessionsReceiptBytes/6)
			} else {
				setEvaluationPortableModel(t, opt.BundleDir, model)
			}
			err := a.EvaluateAgentBundle(opt)
			if err == nil || !strings.Contains(err.Error(), "1 MiB receipt budget") || strings.Contains(err.Error(), "finished") {
				t.Fatalf("dial preceded known metadata refusal: %v", err)
			}
			if _, err := os.Stat(filepath.Join(opt.BundleDir, "receipts")); !os.IsNotExist(err) {
				t.Errorf("metadata refusal changed receipts: %v", err)
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
