package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestConsoleInferenceMenuRefusesInDemoAndLive(t *testing.T) {
	for _, demo := range []bool{true, false} {
		m := newAgentTUIModel(AgentTUIOptions{Demo: true})
		m.opt.Demo = demo
		m = tuiKey(m, tea.KeyEnter, "")
		m = tuiKey(m, 'f', "f")
		if m.inference != nil || m.action != nil || !strings.Contains(m.status, "authored member/workspace") {
			t.Fatalf("editor not retired: %s", m.status)
		}
		for _, size := range [][2]int{{64, 18}, {80, 24}, {120, 40}} {
			m.width, m.height = size[0], size[1]
			view := m.View().Content
			if lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height {
				t.Fatalf("refusal overflow at %v", size)
			}
		}
	}
}

func TestConsoleInferenceLateResultsCannotReopenRetiredEditor(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m = tuiKey(m, tea.KeyEnter, "")
	m = tuiKey(m, 'f', "f")
	for _, msg := range []tea.Msg{consoleInferenceLoaded{pane: &consoleInferencePane{}, err: context.Canceled}, consoleInferenceSaved{err: context.Canceled}} {
		updated, _ := m.Update(msg)
		m = updated.(agentTUIModel)
		if m.inference != nil || m.action != nil || !strings.Contains(m.status, "authored") {
			t.Fatal("late response reopened a retired editor")
		}
	}
}

func TestConsoleInferenceSourcePersistenceAndValidation(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	env := agentTUIEnvironment{Name: "kind-test", Local: true}
	agent := agentTUIAgent{Name: "demo", Namespace: "agents", Runtime: "orka"}
	source := consoleInferenceSource{Kind: "foundry", Name: "test", Endpoint: "https://example.openai.azure.com", Model: "test-model"}
	if err := saveConsoleInference(env, agent, &source); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConsoleInference(env, agent)
	if err != nil || loaded.Model != source.Model {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	path, _ := consoleInferencePath(env, agent)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("metadata permissions incorrect")
	}
	other, err := loadConsoleInference(agentTUIEnvironment{Name: "remote"}, agent)
	if err != nil || other != nil {
		t.Fatal("source leaked across environments")
	}
	if err := saveConsoleInference(env, agent, nil); err != nil {
		t.Fatal(err)
	}
	if loaded, err := loadConsoleInference(env, agent); err != nil || loaded != nil {
		t.Fatal("cluster selection did not clear override")
	}
	for _, s := range []consoleInferenceSource{
		{Kind: "foundry", Endpoint: "https://untrusted.example.com", Model: "x"},
		{Kind: "apikey", Name: "demo", Provider: "openai", Endpoint: "https://user:pass@example.com", Model: "x", Secret: "key", SecretKey: "api-key"},
		{Kind: "ollama", Name: "demo", Endpoint: "http://example.com?token=x", Model: "x"},
		// The retired runtime's connector kind is not a source the console knows.
		{Kind: "modelconfig", Name: "demo", Endpoint: "https://example.com", Model: "x"},
	} {
		if s.validate() == nil {
			t.Fatalf("invalid source accepted: %s", s.Kind)
		}
	}
}

func TestConsoleInferenceRefusalDoesNotStartAzureDiscovery(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.opt.Demo = false
	m.loadAzureInference = func(context.Context, string, string, string, string) ([]consoleAzureChoice, error) {
		t.Fatal("retired editor started Azure discovery")
		return nil, nil
	}
	m.loadInference = func(agentTUIEnvironment, agentTUIAgent) (consoleInferenceSnapshot, error) {
		t.Fatal("retired editor started live reads")
		return consoleInferenceSnapshot{}, nil
	}
	m = tuiKey(m, tea.KeyEnter, "")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	m = updated.(agentTUIModel)
	if cmd != nil || m.inference != nil || m.action != nil || !strings.Contains(m.status, "eval") {
		t.Fatalf("retired inference action started work: %s", m.status)
	}
}

// Azure console reads are prepared by the Runner like every other lift call:
// KMX_LIFT_KEPT is added and KMX_LIFT_DROPPED is removed even though the
// process itself has the latter set, and the subscriptions read is the one
// that fails closed if the Runner environment was never applied.
func TestConsoleInferenceAzureCommandsPinScope(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "az", `case "$*" in
 'account list '*)
  if [ "$KMX_LIFT_KEPT" != "added" ] || [ -n "$KMX_LIFT_DROPPED" ]; then exit 1; fi
  printf '%s' '[{"name":"Demo","id":"sub-test","tenantId":"tenant-test"}]' ;;
 'cognitiveservices account list --subscription sub-test '*) printf '%s' '[{"name":"demo","kind":"OpenAI","resourceGroup":"rg-test","location":"westus3","properties":{"endpoint":"https://example.openai.azure.com/"}}]' ;;
 'cognitiveservices account deployment list --subscription sub-test --resource-group rg-test --name demo '*) printf '%s' '[{"name":"ready","properties":{"provisioningState":"Succeeded","model":{"name":"gpt-4.1","version":"test"}}},{"name":"pending","properties":{"provisioningState":"Creating"}}]' ;;
 *) exit 1 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_LIFT_DROPPED", "inherited")
	a := &App{Run: &run.Runner{Env: []string{"KMX_LIFT_KEPT=added"}, Unset: []string{"KMX_LIFT_DROPPED"}}}
	for _, stage := range []string{"azure-subscriptions", "azure-accounts", "azure-deployments"} {
		choices, err := a.consoleAzureChoices(t.Context(), stage, "sub-test", "rg-test", "demo")
		if err != nil || len(choices) != 1 {
			t.Fatalf("%s choices=%+v err=%v", stage, choices, err)
		}
		if stage == "azure-deployments" && choices[0].Model != "ready" {
			t.Fatal("non-ready deployment offered")
		}
	}
}

func TestConsoleRemoteInferenceNeverOffersOrLoadsHostSources(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	p := consoleInferencePane{env: agentTUIEnvironment{Name: "remote"}, agent: agentTUIAgent{Runtime: "orka"}, azureAvailable: true}
	kinds := strings.Join(p.sourceKinds(), ",")
	if strings.Contains(kinds, "copilot") || !strings.Contains(kinds, "foundry-cluster") || !strings.Contains(kinds, "azure") {
		t.Fatalf("remote source kinds=%s", kinds)
	}
	p.setFields("foundry-cluster")
	if p.fields[2].label != "Existing cluster Secret name" {
		t.Fatal("remote Foundry asks for host login instead of cluster credential")
	}
	env := agentTUIEnvironment{Name: "remote"}
	agent := agentTUIAgent{Runtime: "orka", Name: "demo", Namespace: "agents"}
	source := consoleInferenceSource{Kind: "copilot", Name: "host", Model: "test"}
	// Simulate a legacy remote override left by an earlier console version.
	path, _ := consoleInferencePath(env, agent)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(source)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if loaded, err := loadConsoleInference(env, agent); err != nil || loaded != nil {
		t.Fatal("legacy remote host override was applied")
	}
	if err := saveConsoleInference(env, agent, &source); err == nil {
		t.Fatal("remote host override saved")
	}
	a := &App{}
	if err := a.consoleSaveInference(t.Context(), env, agent, consoleInferenceSnapshot{}, source, ""); err == nil || !strings.Contains(err.Error(), "authored member/workspace") {
		t.Fatalf("host save not rejected before auth: %v", err)
	}
}

func TestRemoteChatRejectsHostInferenceBeforeLogin(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `printf '%s' '{"contexts":[{"name":"kind-misleading","context":{"cluster":"remote"}}],"clusters":[{"name":"remote","cluster":{"server":"https://remote.example.com"}}]}'`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Cfg: &config.Config{KubeContext: "kind-misleading"}, Run: &run.Runner{}}
	b := &orkaChatBackend{app: a}
	for _, command := range []string{"/inference", "/inference-foundry", "/inference-copilot"} {
		if _, err := b.Configure(t.Context(), command, nil); err == nil || !strings.Contains(err.Error(), "remote agents") {
			t.Fatalf("%s not rejected: %v", command, err)
		}
	}
	if err := b.sendHostTurn(t.Context(), "test", nil, nil); err == nil {
		t.Fatal("remote host turn was allowed")
	}
}

func TestConsoleListsRejectMissingAndNullItems(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"items":null}`, `{"items":{}}`, `{"kind":"Status"}`} {
		var list objectList[agentTUIMetadata]
		if err := decodeConsoleList([]byte(body), &list); err == nil {
			t.Fatalf("accepted malformed list: %s", body)
		}
	}
	var list objectList[agentTUIMetadata]
	if err := decodeConsoleList([]byte(`{"items":[]}`), &list); err != nil || list.Items == nil {
		t.Fatal("explicit empty list rejected")
	}
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `case "$*" in
 *'config view'*) printf '%s' '{"contexts":[{"name":"remote","context":{"cluster":"r"}}],"clusters":[{"name":"r","cluster":{"server":"https://remote.example.com"}}]}' ;;
 *api-resources*) printf 'agents.core.orka.ai\n' ;;
 *'get agents.core.orka.ai demo'*) printf '%s' '{"metadata":{"resourceVersion":"42"}}' ;;
 *) printf '%s' '{"items":null}' ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{}}
	env := agentTUIEnvironment{Name: "remote"}
	if agents, err := a.agentTUIInventory(t.Context(), env, "agents"); err == nil || len(agents) != 0 {
		t.Fatal("inventory did not surface malformed list")
	}
	if _, err := a.consoleLoadInference(t.Context(), env, agentTUIAgent{Runtime: "orka", Name: "demo", Namespace: "agents"}); err == nil {
		t.Fatal("inference did not surface malformed list")
	}
}

func TestConsoleInferenceSharedProviderKeepsNamespace(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `case "$*" in
 *'config view'*) printf '%s' '{"contexts":[{"name":"remote","context":{"cluster":"r"}}],"clusters":[{"name":"r","cluster":{"server":"https://remote.example.com"}}]}' ;;
 *'get agents.core.orka.ai demo'*) printf '%s' '{"metadata":{"resourceVersion":"42"},"spec":{"providerRef":{"name":"shared","namespace":"inference"}}}' ;;
 *'-n agents get providers.core.orka.ai -o json'*) printf '%s' '{"items":[{"metadata":{"name":"local"},"spec":{"type":"azure-openai","defaultModel":"local-model","azure":{"deploymentName":"local-deployment"}}}]}' ;;
 *'-n inference get providers.core.orka.ai shared'*) printf '%s' '{"metadata":{"name":"shared"},"spec":{"type":"azure-openai","defaultModel":"shared-model","azure":{"deploymentName":"shared-deployment"}}}' ;;
 *) exit 1 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{}}
	snapshot, err := a.consoleLoadInference(t.Context(), agentTUIEnvironment{Name: "remote"}, agentTUIAgent{Runtime: "orka", Name: "demo", Namespace: "agents"})
	if err != nil || len(snapshot.Sources) != 2 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if snapshot.Sources[0].AzureDeployment != "local-deployment" {
		t.Fatal("lost local Azure Provider deployment")
	}
	source := snapshot.Sources[1]
	if source.Namespace != "inference" || source.Name != "shared" || source.AzureDeployment != "shared-deployment" {
		t.Fatal("lost shared Azure Provider identity or deployment")
	}
	raw, err := consoleInferencePatch(snapshot.Version, source.Name, source.Namespace, "", nil)
	if err != nil || !strings.Contains(string(raw), `"namespace":"inference"`) {
		t.Fatalf("patch=%s err=%v", raw, err)
	}
}

func TestConsoleOllamaEndpointNormalizationAndReviewModel(t *testing.T) {
	for _, suffix := range []string{"", "/", "/v1", "/v1/"} {
		endpoint := "http://ollama:11434" + suffix
		if got := consoleOllamaEndpoint(endpoint, false); got != "http://ollama:11434" {
			t.Fatal(got)
		}
		if got := consoleOllamaEndpoint(endpoint, true); got != "http://ollama:11434/v1" {
			t.Fatal(got)
		}
	}
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.inference = &consoleInferencePane{agent: *m.selected(), env: m.columns[0].Env, model: "old-override"}
	m.inference.setFields("ollama")
	if m.inference.model != "" {
		t.Fatal("new connector retained old override")
	}
	m.inference.source = consoleInferenceSource{Kind: "ollama", Model: "new-model", Name: "new"}
	m.inference.model = "stale-override"
	m.inference.stage = "review"
	view := ansi.Strip(m.inferenceView())
	if !strings.Contains(view, "Model: new-model") || strings.Contains(view, "stale-override") {
		t.Fatal("review model differs from saved connector")
	}
}
