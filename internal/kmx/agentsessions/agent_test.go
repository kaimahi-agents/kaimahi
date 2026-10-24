package agentsessions

import (
	"encoding/json"
	"strings"
	"testing"
)

const agentFixture = `{"apiVersion":"agents.aks-durable-agents.io/v1alpha1","kind":"Agent","metadata":{"name":"greeter","namespace":"agents","uid":"uid-1","generation":1,"resourceVersion":"10"},"spec":{"package":{"reference":"registry.example/greeter@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"member":"greeter","modelBindingRef":{"name":"default"}},"status":{"agentUID":"uid-1","observedGeneration":1,"packageDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","member":"greeter","resolvedBindingDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","revisionDigest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","harnessName":"a-greeter","servedDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","conditions":[{"type":"PackageVerified","status":"True","observedGeneration":1,"reason":"Verified","message":"verified","lastTransitionTime":"2026-10-24T00:00:00Z"},{"type":"Registered","status":"True","observedGeneration":1,"reason":"Admitted","message":"registered","lastTransitionTime":"2026-10-24T00:00:00Z"}]}}`

// Each independent contract mismatch must veto registration, even when all other
// acknowledgments are current. Raw controller messages are never projected.
func TestAgentRegisteredExactContract(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"current":    func(map[string]any) {},
		"uid":        func(d map[string]any) { d["status"].(map[string]any)["agentUID"] = "old" },
		"generation": func(d map[string]any) { d["status"].(map[string]any)["observedGeneration"] = 2 },
		"member":     func(d map[string]any) { d["status"].(map[string]any)["member"] = "other" },
		"digest": func(d map[string]any) {
			d["status"].(map[string]any)["packageDigest"] = "sha256:" + strings.Repeat("b", 64)
		},
		"served":   func(d map[string]any) { delete(d["status"].(map[string]any), "servedDigest") },
		"deleting": func(d map[string]any) { d["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-24T00:00:00Z" },
		"condition-generation": func(d map[string]any) {
			d["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["observedGeneration"] = 0
		},
		"condition-false": func(d map[string]any) {
			d["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)["status"] = "False"
		},
		"case-folded-status": func(d map[string]any) {
			s := d["status"].(map[string]any)
			s["AgentUID"] = s["agentUID"]
			delete(s, "agentUID")
		},
		"case-folded-condition": func(d map[string]any) {
			c := d["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)
			c["Status"] = c["status"]
			delete(c, "status")
		},
		"condition-missing": func(d map[string]any) { d["status"].(map[string]any)["conditions"] = []any{} },
		"condition-null":    func(d map[string]any) { d["status"].(map[string]any)["conditions"] = []any{nil} },
		"condition-invalid-status": func(d map[string]any) {
			d["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["status"] = "bogus"
		},
		"condition-duplicate": func(d map[string]any) {
			s := d["status"].(map[string]any)
			s["conditions"] = append(s["conditions"].([]any), s["conditions"].([]any)[1])
		},
	} {
		t.Run(name, func(t *testing.T) {
			var d map[string]any
			json.Unmarshal([]byte(agentFixture), &d)
			change(d)
			raw, _ := json.Marshal(d)
			a, err := DecodeAgent(raw)
			if name == "condition-duplicate" || name == "case-folded-condition" || name == "condition-null" || name == "condition-invalid-status" {
				if err == nil {
					t.Fatal("accepted malformed conditions")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if AgentRegistered(a) != (name == "current") {
				t.Fatalf("registration for %s = %t", name, AgentRegistered(a))
			}
		})
	}
}

func TestDecodeAgentRejectsAmbiguousOrInvalidSpec(t *testing.T) {
	for _, raw := range []string{
		strings.Replace(agentFixture, `"member":"greeter","modelBindingRef"`, `"member":"greeter","extra":true,"modelBindingRef"`, 1),
		strings.Replace(agentFixture, `"member":"greeter"`, `"member":"greeter","member":"other"`, 1),
		strings.Replace(agentFixture, `"generation":1`, `"generation":1.5`, 1),
		strings.Replace(agentFixture, `"member":"greeter","modelBindingRef"`, `"Member":"greeter","modelBindingRef"`, 1),
		strings.Replace(agentFixture, `"name":"greeter"`, `"name":"INVALID"`, 1),
		strings.Replace(agentFixture, `registry.example/greeter@sha256:`, `registry.example/greeter:tag@sha256:`, 1),
	} {
		if _, err := DecodeAgent([]byte(raw)); err == nil {
			t.Fatal("accepted malformed Agent")
		}
	}
}
