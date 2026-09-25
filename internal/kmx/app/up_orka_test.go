package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestUpRuntimeOwnsItsSteps(t *testing.T) {
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{}}
	for _, tc := range []struct {
		opt  UpOptions
		want string
	}{
		{UpOptions{Runtime: "other"}, `unknown runtime "other"`},
		{UpOptions{Runtime: "kagent", Step: "orka"}, `step "orka" is not available for runtime "kagent"`},
		{UpOptions{Runtime: "orka", Step: "kagent"}, `step "kagent" is not available for runtime "orka"`},
	} {
		err := a.UpWithOptions(tc.opt)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%+v: error=%v, want containing %q", tc.opt, err, tc.want)
		}
	}
}

func TestOrkaUpFollowupSelectsTheToolEnabledAgent(t *testing.T) {
	var diagnostics bytes.Buffer
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-demo"},
		Err: &diagnostics,
	}

	a.printOrkaUpFollowup()

	output := diagnostics.String()
	toolCommand := "kmx --context kind-demo agent chat --interactive --runtime orka --namespace orka-system hello-tools"
	plainCommand := "kmx --context kind-demo agent chat --interactive --runtime orka --namespace orka-system hello-world"
	if !strings.Contains(output, "TRY   Kubernetes tools: "+toolCommand) {
		t.Fatalf("tool follow-up does not select the tool-enabled Agent:\n%s", output)
	}
	if !strings.Contains(output, "Plain Agent:      "+plainCommand) {
		t.Fatalf("plain Agent is not clearly distinguished:\n%s", output)
	}
}

func TestOrkaUpAgentUsesSharedProviderAndToolWithoutSecrets(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "stdin")
	fakeTool(t, dir, "kubectl", fmt.Sprintf(`
body=$(/bin/cat)
printf '%%s\n---\n' "$body" >> %q
case "$*" in
  *'apply --dry-run=server'*) printf '%%s' '{"kind":"Agent"}' ;;
  *'apply --server-side'*) printf '%%s' '{"kind":"Agent","metadata":{"name":"hello-tools","namespace":"orka-system","uid":"agent-uid","generation":4}}' ;;
  *'get agents.core.orka.ai hello-tools'*) printf '%%s' '{"kind":"Agent","metadata":{"name":"hello-tools","namespace":"orka-system","uid":"agent-uid","generation":4},"status":{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":4}]}}' ;;
  *) exit 91 ;;
esac`, log))
	t.Setenv("PATH", dir)

	var diagnostics bytes.Buffer
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-demo"},
		Run: &run.Runner{},
		Err: &diagnostics,
		Out: &bytes.Buffer{},
	}
	if err := a.applyOrkaUpAgent(orkaHelloToolsAgent, "demo", "Use the tool.", []string{quickstartK8sTool}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		`"name":"hello-tools"`,
		`"namespace":"orka-system"`,
		`"name":"local"`,
		`"name":"k8s-get-resources"`,
		`"inline":"Use the tool."`,
		`"kaimahi.dev/description":"demo"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("applied Agent lacks %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"Secret", "secretRef", "api-key", "not-used-by-this-endpoint"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("applied Agent contains %q:\n%s", forbidden, text)
		}
		if strings.Contains(text, `"spec":{"description"`) {
			t.Fatalf("description was placed in unsupported Agent spec:\n%s", text)
		}
	}
}
