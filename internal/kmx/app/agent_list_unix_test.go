//go:build !windows

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnixOrkaAgentListAcceptsKubectlListEnvelope(t *testing.T) {
	a, dir := agentListFixture(t)
	t.Setenv("KMX_INSPECT_AGENTS", `{"apiVersion":"v1","kind":"List","metadata":{"resourceVersion":"7"},"items":[{"apiVersion":"core.orka.ai/v1alpha1","kind":"Agent","metadata":{"name":"concierge","namespace":"team-a"},"spec":{"providerRef":{"name":"local"},"model":{"name":"fixture-model"}},"status":{"ready":true}}]}`)
	if err := a.ListAgents("table", "team-a"); err != nil {
		t.Fatal(err)
	}
	if output := a.Out.(*bytes.Buffer).String(); !strings.Contains(output, "concierge") || !strings.Contains(output, "fixture-model") {
		t.Fatalf("table output = %q", output)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if call := string(calls); !strings.Contains(call, "-n team-a get agents.core.orka.ai -o json") || strings.Contains(call, "get --raw") {
		t.Fatalf("kubectl calls = %q", call)
	}
}

func TestOrkaAgentTableDecoderDoesNotRelaxRawPages(t *testing.T) {
	raw := []byte(`{"apiVersion":"v1","kind":"List","metadata":{},"items":[{"apiVersion":"core.orka.ai/v1alpha1","kind":"Agent","metadata":{"name":"concierge","namespace":"team-a"}}]}`)
	if agents, err := decodeOrkaAgentList(raw, "team-a"); err != nil || len(agents) != 1 {
		t.Fatalf("table decode = %d Agents, %v", len(agents), err)
	}
	if _, _, err := decodeOrkaAgentListPage(raw, "team-a"); err == nil || !strings.Contains(err.Error(), "invalid list identity") {
		t.Fatalf("raw page decode = %v", err)
	}
	wrongItem := []byte(`{"apiVersion":"v1","kind":"List","metadata":{},"items":[{"apiVersion":"v1","kind":"Secret","metadata":{"name":"model-key","namespace":"team-a"}}]}`)
	if _, err := decodeOrkaAgentList(wrongItem, "team-a"); err == nil || !strings.Contains(err.Error(), "invalid Agent identity") {
		t.Fatalf("generic list with non-Agent item = %v", err)
	}
}
