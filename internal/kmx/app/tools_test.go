package app

import (
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func cfgForToolsTest() *config.Config {
	return &config.Config{
		KindCluster: "kaimahi-p1",
		KubeContext: "kind-kaimahi-p1", ContextSource: config.SourceKubeCtx,
		ToolsCredential: config.DefaultToolsCredential,
	}
}

// `kmx tools ungovern` re-applies k8s/tools-agent.yaml, and that manifest
// names ONE agent. Ungoverning a different one would apply the committed
// agent and then wait for an unrelated rollout — reporting success while
// the agent the operator named was still riding the gateway.
func TestUngovernToolsRefusesAnAgentItCannotRestore(t *testing.T) {
	a := &App{Cfg: cfgForToolsTest(), Out: &strings.Builder{}, Err: &strings.Builder{}}
	err := a.UngovernTools(ToolsOptions{Agent: "billing"})
	if err == nil {
		t.Fatal("ungoverning an agent with no committed form was accepted")
	}
	if !strings.Contains(err.Error(), `restores the committed agent "hello-tools", not "billing"`) {
		t.Errorf("unexpected refusal: %v", err)
	}
}
