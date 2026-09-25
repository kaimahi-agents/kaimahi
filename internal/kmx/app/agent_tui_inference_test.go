package app

import (
	"context"
	"encoding/json"
	"io"
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

func TestConsoleInferenceOverlaySourcesFormsAndDemo(t *testing.T) {
	for _, kind := range []string{"foundry", "ollama", "copilot", "apikey"} {
		m := newAgentTUIModel(AgentTUIOptions{Demo: true})
		m = tuiKey(m, tea.KeyEnter, "")
		m = tuiKey(m, 'f', "f")
		if m.inference == nil || m.action != nil {
			t.Fatal("inference left console")
		}
		view := ansi.Strip(m.View().Content)
		if strings.Index(view, "Add inference source") < strings.Index(view, "demo-provider") {
			t.Fatal("add source is not last")
		}
		m = tuiKey(m, tea.KeyDown, "")
		m = tuiKey(m, tea.KeyEnter, "")
		if m.inference.stage != "kinds" {
			t.Fatal("add source did not open connector choices")
		}
		for i, k := range m.inference.sourceKinds() {
			if k == kind {
				m.inference.selection = i
			}
		}
		m = tuiKey(m, tea.KeyEnter, "")
		values := map[string][]string{
			"foundry": {"https://example.openai.azure.com", "chat-model", ""},
			"ollama":  {"http://ollama.ollama.svc.cluster.local:11434", "qwen2.5:3b"},
			"copilot": {"gpt-4.1"},
			"apikey":  {"openai", "https://api.example.com/v1", "test-model", "provider-key", "api-key"},
		}[kind]
		for _, value := range values {
			m.inference.fields[m.inference.field].input.SetValue(value)
			m = tuiKey(m, tea.KeyEnter, "")
		}
		if m.inference.stage != "name" || m.inference.fields[0].input.Value() == "" {
			t.Fatalf("%s must ask for name last", kind)
		}
		m = tuiKey(m, tea.KeyEnter, "")
		if m.inference.stage != "review" {
			t.Fatalf("%s form: %v", kind, m.inference.err)
		}
		for _, size := range [][2]int{{64, 18}, {80, 24}, {120, 40}} {
			m.width, m.height = size[0], size[1]
			view := m.View().Content
			if lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height {
				t.Fatalf("%s overlay overflow at %v", kind, size)
			}
		}
		m = tuiKey(m, tea.KeyDown, "")
		m = tuiKey(m, tea.KeyEnter, "")
		if m.inference.stage != "done" || m.action != nil {
			t.Fatal("demo saved a source")
		}
		m = tuiKey(m, tea.KeyEnter, "")
		if m.inference != nil {
			t.Fatal("result did not close")
		}
	}
}

func TestConsoleInferenceOverlaySaveCancelAndStaleLoad(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m = tuiKey(m, tea.KeyEnter, "")
	m = tuiKey(m, 'f', "f")
	p := m.inference
	updated, _ := m.Update(consoleInferenceLoaded{pane: &consoleInferencePane{}, err: context.Canceled})
	m = updated.(agentTUIModel)
	if p.err != nil {
		t.Fatal("stale lookup replaced current pane")
	}
	p.source = consoleInferenceSource{Kind: "cluster", Name: "selected"}
	p.stage = "review"
	p.selection = 1
	m.opt.Demo = false
	cancelled := false
	m.startInference = func(env agentTUIEnvironment, agent agentTUIAgent, snapshot consoleInferenceSnapshot, source consoleInferenceSource, model string) (<-chan consoleInferenceSaved, context.CancelFunc) {
		if source.Name != "selected" || env.Name != "kind-local-demo" || agent.Name != "assistant" {
			t.Fatal("save routed incorrectly")
		}
		return make(chan consoleInferenceSaved, 1), func() { cancelled = true }
	}
	m.loadInventory = func(agentTUIEnvironment) ([]agentTUIAgent, error) { return nil, nil }
	m = tuiKey(m, tea.KeyEnter, "")
	m = tuiKey(m, tea.KeyEsc, "")
	if !cancelled || p.stage != "saving" {
		t.Fatal("cancellation closed before worker joined")
	}
	updated, _ = m.Update(consoleInferenceSaved{err: context.Canceled})
	m = updated.(agentTUIModel)
	if p.stage != "done" {
		t.Fatal("save result lost")
	}
}

func TestConsoleInferenceSourcePersistenceAndValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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
	} {
		if s.validate("orka") == nil {
			t.Fatalf("invalid source accepted: %s", s.Kind)
		}
	}
	if source.validate("kagent") == nil {
		t.Fatal("host inference exposed for unsupported runtime")
	}
}

func TestConsoleInferenceCreatesConnectorWithoutRewritingAgentTools(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "body")
	fakeTool(t, dir, "kubectl", `case "$*" in
 *'create --validate=strict -f -'*) cat > "$BODY_LOG"; printf '%s' '{"kind":"ModelConfig"}' ;;
 *) exit 1 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Cfg: &config.Config{KubeContext: "remote"}, Run: &run.Runner{Env: []string{"BODY_LOG=" + log}}}
	s := consoleInferenceSource{Kind: "ollama", Name: "local-model", Endpoint: "http://ollama:11434", Model: "qwen2.5:3b"}
	if err := a.consoleCreateConnector(t.Context(), agentTUIAgent{Runtime: "kagent", Namespace: "kagent"}, s); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	spec := doc["spec"].(map[string]any)
	if doc["kind"] != "ModelConfig" || spec["provider"] != "Ollama" || spec["ollama"].(map[string]any)["host"] != s.Endpoint {
		t.Fatalf("connector=%s", raw)
	}
}

func TestConsoleInferenceAzureDiscoveryAndFinalName(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m = tuiKey(m, tea.KeyEnter, "")
	m = tuiKey(m, 'f', "f")
	p := m.inference
	p.azureAvailable = false
	if strings.Contains(strings.Join(p.sourceKinds(), ","), "azure") {
		t.Fatal("Azure offered without CLI")
	}
	p.azureAvailable = true
	p.stage = "kinds"
	p.selection = 0
	if p.sourceKinds()[0] != "azure" {
		t.Fatal("Azure discovery missing")
	}
	for _, stage := range []string{"azure-subscriptions", "azure-accounts", "azure-deployments"} {
		updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated.(agentTUIModel)
		if m.inference.stage != "azure-loading" || cmd == nil {
			t.Fatal("Azure selection did not discover next stage")
		}
		updated, _ = m.Update(cmd())
		m = updated.(agentTUIModel)
		if m.inference.stage != stage {
			t.Fatalf("stage=%s want=%s", m.inference.stage, stage)
		}
	}
	m = tuiKey(m, tea.KeyEnter, "")
	if p.stage != "name" || p.fields[0].input.Value() != "foundry-chat-model" {
		t.Fatalf("name step=%s value=%s", p.stage, p.fields[0].input.Value())
	}
	p.fields[0].input.SetValue("my-foundry")
	m = tuiKey(m, tea.KeyEnter, "")
	if p.stage != "review" || p.source.Name != "my-foundry" || p.source.Endpoint != "https://example.openai.azure.com" {
		t.Fatalf("source=%+v", p.source)
	}
}

func TestConsoleInferenceAzureCommandsPinScope(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "az", `case "$*" in
 'account list '*) printf '%s' '[{"name":"Demo","id":"sub-test","tenantId":"tenant-test"}]' ;;
 'cognitiveservices account list --subscription sub-test '*) printf '%s' '[{"name":"demo","kind":"OpenAI","resourceGroup":"rg-test","location":"westus3","properties":{"endpoint":"https://example.openai.azure.com/"}}]' ;;
 'cognitiveservices account deployment list --subscription sub-test --resource-group rg-test --name demo '*) printf '%s' '[{"name":"ready","properties":{"provisioningState":"Succeeded","model":{"name":"gpt-4.1","version":"test"}}},{"name":"pending","properties":{"provisioningState":"Creating"}}]' ;;
 *) exit 1 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, stage := range []string{"azure-subscriptions", "azure-accounts", "azure-deployments"} {
		choices, err := consoleAzureChoices(t.Context(), stage, "sub-test", "rg-test", "demo")
		if err != nil || len(choices) != 1 {
			t.Fatalf("%s choices=%+v err=%v", stage, choices, err)
		}
		if stage == "azure-deployments" && choices[0].Model != "ready" {
			t.Fatal("non-ready deployment offered")
		}
	}
}

func TestConsoleRemoteInferenceNeverOffersOrLoadsHostSources(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, runtime := range []string{"orka", "kagent"} {
		p := consoleInferencePane{env: agentTUIEnvironment{Name: "remote"}, agent: agentTUIAgent{Runtime: runtime}, azureAvailable: true}
		kinds := strings.Join(p.sourceKinds(), ",")
		if strings.Contains(kinds, "copilot") || !strings.Contains(kinds, "foundry-cluster") || !strings.Contains(kinds, "azure") {
			t.Fatalf("remote source kinds=%s", kinds)
		}
		p.setFields("foundry-cluster")
		if p.fields[2].label != "Existing cluster Secret name" {
			t.Fatal("remote Foundry asks for host login instead of cluster credential")
		}
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
	if err := a.consoleSaveInference(t.Context(), env, agent, consoleInferenceSnapshot{}, source, ""); err == nil || !strings.Contains(err.Error(), "remote environments") {
		t.Fatalf("host save not rejected before auth: %v", err)
	}
}

func TestRemoteManualFoundryProbeUsesClusterSecretWithoutAzureCLI(t *testing.T) {
	dir := t.TempDir()
	body := filepath.Join(dir, "probe")
	t.Setenv("PROBE_BODY", body)
	fakeTool(t, dir, "kubectl", `case "$*" in
 *'create -f -'*) /bin/cat > "$PROBE_BODY" ;;
 *'get job'*) printf '%s' '{"status":{"succeeded":1}}' ;;
 *'delete job'*) exit 0 ;;
 *) exit 1 ;;
esac`)
	t.Setenv("PATH", dir) // No az or copilot exists on this path.
	a := &App{Cfg: &config.Config{KubeContext: "remote"}, Run: &run.Runner{}, Err: io.Discard}
	source := consoleInferenceSource{Kind: "foundry-cluster", Name: "remote-foundry", Endpoint: "https://example.openai.azure.com", Model: "deployment", Secret: "existing-key", SecretKey: "custom-key"}
	configured, err := a.consolePrepareClusterFoundry(t.Context(), agentTUIAgent{Runtime: "orka", Namespace: "agents"}, source)
	if err != nil {
		t.Fatal(err)
	}
	if configured.Kind != "apikey" || configured.Endpoint != "https://example.openai.azure.com/openai/v1" {
		t.Fatalf("connector=%+v", configured)
	}
	raw, err := os.ReadFile(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"secretName":"existing-key"`, `"key":"custom-key"`, `"path":"api-key"`, `"namespace":"agents"`, `"value":"deployment"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("cluster probe missing %s", want)
		}
	}
	if strings.Contains(string(raw), "stringData") {
		t.Fatal("probe embedded credential material")
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
 *'-n agents get providers.core.orka.ai -o json'*) printf '%s' '{"items":[]}' ;;
 *'-n inference get providers.core.orka.ai shared'*) printf '%s' '{"metadata":{"name":"shared"},"spec":{"type":"openai","defaultModel":"shared-model"}}' ;;
 *) exit 1 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{}}
	snapshot, err := a.consoleLoadInference(t.Context(), agentTUIEnvironment{Name: "remote"}, agentTUIAgent{Runtime: "orka", Name: "demo", Namespace: "agents"})
	if err != nil || len(snapshot.Sources) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	source := snapshot.Sources[0]
	if source.Namespace != "inference" || source.Name != "shared" {
		t.Fatal("lost shared Provider identity")
	}
	raw, err := consoleInferencePatch("orka", snapshot.Version, source.Name, source.Namespace, "", nil)
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
