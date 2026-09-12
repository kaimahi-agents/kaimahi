package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestToolResponsesCannotClaimRetiredGatewayGovernance(t *testing.T) {
	var out bytes.Buffer
	v := newStreamView("agent", "off", &chatRenderer{out: &out}, &chatGovernancePosture{})
	v.consumeTool("function_call", false, json.RawMessage(`{"id":"one","name":"delete","args":{}}`), &out)
	v.consumeTool("function_response", false, json.RawMessage(`{"id":"one","name":"delete","response":{"isError":true,"content":[{"text":"tool not permitted; approval request filed"}]}}`), &out)
	if v.denied || strings.Contains(out.String(), "KAIMAHI") {
		t.Fatalf("retired gateway claim: %s", &out)
	}
}

func TestStatusEvidenceIsModelOnly(t *testing.T) {
	d := &statusData{planeThere: true, planeDesired: 1, planeReady: 1}
	d.agents.Items = []agentStatus{agentOn("agent", "model")}
	d.models.Items = []modelStatus{modelAt("model", governedModelURL, "model-token")}
	d.servers.Items = []json.RawMessage{json.RawMessage(`{"kind":"RemoteMCPServer","metadata":{"name":"old-owner-route"},"spec":{"url":"https://kaimahi-mcp-gateway.kaimahi:8081/upstream/kagent-tools/mcp","headersFrom":[{"valueFrom":{"type":"Secret","name":"retired-token"}}]}}`)}
	d.secrets = []string{"model-token"}
	g := d.governanceOf()
	if !governanceReady(g) {
		t.Fatalf("retired tool credential affects model readiness: %+v", g)
	}
	if g.Credentials.Required != 1 || g.Credentials.Present != 1 {
		t.Fatalf("model credentials: %+v", g.Credentials)
	}
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "toolSeams") || strings.Contains(string(raw), "retired-token") {
		t.Fatalf("managed tool evidence remains: %s", raw)
	}
	var out bytes.Buffer
	writeGovernance(&out, g)
	if strings.Contains(out.String(), "tool seams") {
		t.Fatalf("tool count remains: %s", &out)
	}
	d.serverErr = "Forbidden"
	if !governanceReady(d.governanceOf()) {
		t.Fatal("raw MCP inventory failure affected model readiness")
	}
}
