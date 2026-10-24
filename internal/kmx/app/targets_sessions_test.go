package app

import "testing"

func TestCompiledSessionsCapabilitiesPreserveDevelopmentSuiteEvaluation(t *testing.T) {
	for _, row := range compiledTargets() {
		if row.ID != "agentsessions" {
			continue
		}
		if row.SupportTier != "development-only" {
			t.Fatalf("support tier: %s", row.SupportTier)
		}
		caps := map[string]bool{}
		for _, cap := range row.Capabilities {
			caps[cap.Operation] = cap.Supported
		}
		for _, verb := range []string{"create", "eval", "push", "export", "lift", "inspect"} {
			if !caps[verb] {
				t.Fatalf("native development %s missing", verb)
			}
		}
		for _, verb := range []string{"chat", "run", "diff", "status", "retire"} {
			if supported := caps[verb]; supported {
				t.Fatalf("unsupported lifecycle %s missing or enabled", verb)
			}
		}
		return
	}
	t.Fatal("AgentSessions profile missing")
}
