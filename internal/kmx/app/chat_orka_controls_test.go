package app

import (
	"strings"
	"testing"
)

func TestChatLiftMovedBeforeTerminalAndMutation(t *testing.T) {
	f := newOrkaFixture(t, nil)
	b := &orkaChatBackend{app: f.app, agent: "sample", namespace: "agents"}
	changed, err := b.Configure(t.Context(), "/lift", newChatRenderer(f.out))
	want := "Deploy moved: kmx deploy <ref>@<digest> --to-context … --evidence …"
	if changed || err == nil || err.Error() != want {
		t.Fatalf("moved notice: changed=%t err=%v", changed, err)
	}
	if calls := f.calls(t); calls != "" {
		t.Fatalf("retired Lift accessed cluster: %s", calls)
	}
	if f.out.Len() != 0 {
		t.Fatal("retirement polluted answer stdout")
	}
}

func TestChatToolsRefuseBeforeTerminalAndMutation(t *testing.T) {
	f := newOrkaFixture(t, nil)
	b := &orkaChatBackend{app: f.app, agent: "sample", namespace: "agents"}
	changed, err := b.Configure(t.Context(), "/tools", newChatRenderer(f.out))
	if changed || err == nil || !strings.Contains(err.Error(), "authored") || !strings.Contains(err.Error(), "eval") {
		t.Errorf("wrong refusal: changed=%v err=%v", changed, err)
	}
	if calls := f.calls(t); calls != "" {
		t.Fatalf("commands before refusal: %s", calls)
	}
	err = b.pickTools(t.Context(), newChatRenderer(f.out))
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("tool entrypoint still edits: %v", err)
	}
	if calls := f.calls(t); calls != "" {
		t.Fatalf("tool entrypoint commands: %s", calls)
	}
}

func TestOrkaEnabledToolsSummaryCountsEffectiveTools(t *testing.T) {
	summary, err := orkaEnabledToolsSummary([]byte(`{"spec":{"tools":[{"name":"k8s-get-resources"},{"name":"disabled","enabled":false},{"name":"remember","enabled":false},{"name":"k8s-get-resources","enabled":true}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "(5 tools enabled) k8s-get-resources, propose_memory, recall_memory, remember, search_transcript"
	if summary != want {
		t.Fatalf("summary=%q want=%q", summary, want)
	}
	summary, err = orkaEnabledToolsSummary([]byte(`{"spec":{}}`))
	if err != nil || !strings.HasPrefix(summary, "(4 tools enabled)") {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
	if _, err := orkaEnabledToolsSummary([]byte(`invalid`)); err == nil {
		t.Fatal("invalid Agent accepted")
	}
}
