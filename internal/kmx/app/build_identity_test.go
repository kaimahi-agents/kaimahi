package app

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
)

func TestAppUserAgentUsesInvokingBuild(t *testing.T) {
	a := &App{}
	a.SetBuildIdentity(version.Build{Version: "v0.4.0", Release: true})
	req := httptest.NewRequest("GET", "https://example.test", nil)
	a.setUserAgent(req)
	if got, want := req.Header.Get("User-Agent"), "kmx/v0.4.0"; got != want {
		t.Fatalf("User-Agent = %q, want %q", got, want)
	}
}

func TestAnnotateCreatedByVersion(t *testing.T) {
	a := &App{}
	a.SetBuildIdentity(version.Build{Version: "v0.4.0", Release: true})
	doc := map[string]any{"metadata": map[string]any{"annotations": map[string]any{"example.test/retained": "yes"}}}
	if err := a.annotateCreatedByVersion(doc); err != nil {
		t.Fatal(err)
	}
	annotations := doc["metadata"].(map[string]any)["annotations"].(map[string]any)
	if got := annotations[createdByVersionAnnotation]; got != "v0.4.0" {
		t.Fatalf("%s = %v", createdByVersionAnnotation, got)
	}
	if annotations["example.test/retained"] != "yes" {
		t.Fatal("existing annotation was not retained")
	}
}

func TestAnnotateCreatedByVersionRefusesConflict(t *testing.T) {
	a := &App{}
	a.SetBuildIdentity(version.Build{Version: "v0.4.0", Release: true})
	doc := map[string]any{"metadata": map[string]any{"annotations": map[string]any{createdByVersionAnnotation: "v0.3.0"}}}
	if err := a.annotateCreatedByVersion(doc); err == nil {
		t.Fatal("conflicting reserved annotation was accepted")
	}
}

func TestValidateCreatedByVersionRequiresAdmissionAndCreateRetention(t *testing.T) {
	doc := map[string]any{
		"apiVersion": "core.orka.ai/v1alpha1",
		"kind":       "Task",
		"metadata": map[string]any{
			"name": "task", "namespace": "agents",
			"annotations": map[string]any{createdByVersionAnnotation: "v0.4.0"},
		},
		"spec": map[string]any{"type": "ai"},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCreatedByVersion(raw, doc, "agents", false); err != nil {
		t.Fatal(err)
	}
	var changed map[string]any
	if json.Unmarshal(raw, &changed) != nil {
		t.Fatal("decode fixture")
	}
	delete(changed["metadata"].(map[string]any)["annotations"].(map[string]any), createdByVersionAnnotation)
	raw, _ = json.Marshal(changed)
	if err := validateCreatedByVersion(raw, doc, "agents", false); err == nil {
		t.Fatal("stripped build identity passed admission")
	}
}
