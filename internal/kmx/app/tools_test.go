package app

import (
	"encoding/json"
	"net/http"
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

// Only the committed tools agent has a known direct tool selection.
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

func TestUngovernToolsOnlyPatchesToolWiring(t *testing.T) {
	f := newPgFixture(t)
	// This fixture stops at the generation read, after recording the patch.
	if err := f.app.UngovernTools(ToolsOptions{}); err == nil {
		t.Fatal("fixture unexpectedly completed the rollout")
	}
	args := f.args()
	if strings.Contains(args, "apply") {
		t.Fatalf("ungovern reapplied the entire agent: %s", args)
	}
	for _, line := range strings.Split(args, "\n") {
		if !strings.Contains(line, "patch agents.kagent.dev hello-tools --type merge -p ") {
			continue
		}
		var patch map[string]map[string]map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.SplitN(line, " -p ", 2)[1]), &patch); err != nil {
			t.Fatal(err)
		}
		declarative := patch["spec"]["declarative"]
		if len(patch) != 1 || len(patch["spec"]) != 1 || len(declarative) != 1 || declarative["tools"] == nil {
			t.Fatalf("patch changes more than tool wiring: %s", line)
		}
		if !strings.Contains(string(declarative["tools"]), `"name":"kagent-tool-server"`) || !strings.Contains(string(declarative["tools"]), `"toolNames":["k8s_get_resources"]`) {
			t.Fatalf("wrong direct selection: %s", line)
		}
		return
	}
	t.Fatalf("no tools patch: %s", args)
}

func TestGovernToolsRejectsUnsupportedCommittedSecretBeforeGuard(t *testing.T) {
	for _, opt := range []ToolsOptions{{Secret: "custom"}, {SecretNamespace: "other"}} {
		a := &App{Cfg: cfgForToolsTest()}
		if err := a.GovernTools(opt); err == nil || !strings.Contains(err.Error(), "unsupported --secret") {
			t.Fatalf("unsupported token destination accepted: %v", err)
		}
	}
}

func TestGovernToolsCustomSecretMustMatchServerReferenceBeforeIssuance(t *testing.T) {
	for _, tc := range []struct {
		name, reference string
		issue           bool
	}{
		{"matching", `[{"name":"Authorization","valueFrom":{"type":"Secret","name":"custom","key":"api-key"}}]`, true},
		{"wrong name", `[{"name":"Authorization","valueFrom":{"type":"Secret","name":"other","key":"api-key"}}]`, false},
		{"wrong key", `[{"name":"Authorization","valueFrom":{"type":"Secret","name":"custom","key":"token"}}]`, false},
		{"no reference", `[]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issued := false
			f := newGovernFixture(t, "", func(w http.ResponseWriter, r *http.Request) {
				issued = true
				http.Error(w, "stop after reference validation", http.StatusBadRequest)
			})
			t.Setenv("KMX_TEST_TOOL_SERVER", `{"spec":{"headersFrom":`+tc.reference+`}}`)
			err := f.app.GovernTools(ToolsOptions{Server: "custom-server", Secret: "custom"})
			if err == nil || issued != tc.issue {
				t.Fatalf("issued=%t, want %t; error=%v", issued, tc.issue, err)
			}
			if !tc.issue && strings.Contains(f.args(), "port-forward") {
				t.Fatalf("admin session opened before reference validation: %s", f.args())
			}
		})
	}
}
