package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// `--runtime kagent` was the only way to ask for the legacy runtime by name,
// and it now has no implementation behind it. It must be refused by name
// rather than accepted and quietly resolved to Orka: a caller who asked for
// kagent asked for a different platform, and answering from Orka instead
// would be answering a question nobody put.
//
// The refusal is local. Nothing about it needs a cluster, so nothing about it
// may reach one.
func TestChatRefusesTheRetiredKagentRuntimeWithoutReachingACluster(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	var out bytes.Buffer
	a := &App{Cfg: &config.Config{KubeContext: "kind-test"}, Run: &run.Runner{}, Out: &out, Err: &out}
	err := a.ChatWithOptions(ChatOptions{Agent: "demo", Runtime: "kagent", Interactive: true})
	if err == nil {
		t.Fatal("the retired runtime was accepted")
	}
	for _, want := range []string{"kagent", "orka"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}
}

// One-shot chat was kagent's transport: a port-forward to its controller and
// an A2A task printed as JSON. Orka chat is the interactive session, so a
// bare message must be refused with the command that works rather than
// dialing a controller that is no longer installed.
func TestOneShotChatIsRefusedWithTheInteractiveCommand(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	var out bytes.Buffer
	a := &App{Cfg: &config.Config{KubeContext: "kind-test"}, Run: &run.Runner{}, Out: &out, Err: &out}
	err := a.ChatWithOptions(ChatOptions{Agent: "demo", Task: "hello"})
	if err == nil || !strings.Contains(err.Error(), "--interactive") {
		t.Fatalf("a one-shot chat was not redirected to the interactive session: %v", err)
	}
}

// `agent list` reports Orka Agents and nothing else. A bare list reads the
// namespace the pinned installer uses — the same default `agent chat`
// resolves against — rather than the legacy runtime's fixed namespace.
func TestAgentListIsOrkaOnlyAndDefaultsToTheOrkaNamespace(t *testing.T) {
	a, dir := agentListFixture(t)
	if err := a.ListAgents("table", ""); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if !strings.Contains(string(calls), "agents.core.orka.ai") {
		t.Fatalf("a bare agent list did not read Orka Agents:\n%s", calls)
	}
	if !strings.Contains(string(calls), "-n "+OrkaNamespace) {
		t.Fatalf("a bare agent list did not use the Orka namespace default:\n%s", calls)
	}
}

// The legacy kind must not be read on any agent-list path, in any output
// format: a JSON or YAML list that still reached for agents.kagent.dev would
// be the retired runtime surviving behind a flag.
func TestAgentListNeverReadsTheLegacyKind(t *testing.T) {
	for _, format := range []string{"table", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			a, dir := agentListFixture(t)
			if err := a.ListAgents(format, ""); err != nil {
				t.Fatal(err)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if strings.Contains(string(calls), "kagent.dev") {
				t.Fatalf("agent list still reads the legacy kind:\n%s", calls)
			}
		})
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
