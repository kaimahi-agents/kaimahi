package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/template"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestRetireDependentsKubectl(t *testing.T) {
	if os.Getenv("KMX_DEPENDENTS_DIR") == "" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if len(args) != 7 || args[0] != "--context" || args[1] != "kind-test" || args[2] != "--request-timeout=10s" || args[3] != "get" || args[5] != "--all-namespaces" || !strings.HasPrefix(args[6], "-o=go-template=") {
		os.Exit(2)
	}
	if args[4] == os.Getenv("KMX_DEPENDENTS_DENIED") {
		fmt.Fprint(os.Stderr, os.Getenv("KMX_DEPENDENTS_ERROR"))
		os.Exit(1)
	}
	if args[4] == os.Getenv("KMX_DEPENDENTS_OVERSIZE") {
		_, _ = os.Stdout.Write([]byte(strings.Repeat("X", 5<<20)))
		os.Exit(0)
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("KMX_DEPENDENTS_DIR"), args[4]+".json"))
	if err != nil {
		os.Exit(2)
	}
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		os.Exit(2)
	}
	project, err := template.New("projection").Parse(strings.TrimPrefix(args[6], "-o=go-template="))
	if err != nil || project.Execute(os.Stdout, data) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func dependentsFixture(t *testing.T, lists map[string]string) *App {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fakeTool(t, dir, "kubectl", "exec "+shellArg(exe)+" -test.run=^TestRetireDependentsKubectl$ -- \"$@\"")
	for _, kind := range []string{"tasks.core.orka.ai", "gatewaybindings.gateway.orka.ai", "repositoryscans.core.orka.ai", "repositorymonitors.core.orka.ai", "agents.core.orka.ai"} {
		raw := lists[kind]
		if raw == "" {
			raw = `{"items":[]}`
		}
		if err := os.WriteFile(filepath.Join(dir, kind+".json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_DEPENDENTS_DIR", dir)
	return &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Run: &run.Runner{}, Out: io.Discard, Err: io.Discard}
}

func TestRetireAgentDependentsAllReferenceKindsAndNamespaces(t *testing.T) {
	a := dependentsFixture(t, map[string]string{
		"tasks.core.orka.ai":              `{"items":[{"metadata":{"name":"pending","namespace":"other"},"spec":{"agentRef":{"name":"target","namespace":"home"}},"status":{"phase":"Pending"}},{"metadata":{"name":"scheduled","namespace":"home"},"spec":{"agentRef":{"name":"target"}},"status":{"phase":"Scheduled"}},{"metadata":{"name":"running","namespace":"home"},"status":{"phase":"Running","agentExecutionBinding":{"agent":{"name":"target","namespace":"home"}}}},{"metadata":{"name":"finalizing","namespace":"home"},"spec":{"agentRef":{"name":"target"}},"status":{"phase":"Finalizing"}},{"metadata":{"name":"done","namespace":"home"},"spec":{"agentRef":{"name":"target"}},"status":{"phase":"Succeeded"}},{"metadata":{"name":"different","namespace":"other"},"spec":{"agentRef":{"name":"target"}},"status":{"phase":"Running"}}]}`,
		"gatewaybindings.gateway.orka.ai": `{"items":[{"metadata":{"name":"ingress","namespace":"home"},"spec":{"agentRef":{"name":"target"}}},{"metadata":{"name":"elsewhere","namespace":"other"},"spec":{"agentRef":{"name":"target"}}}]}`,
		"repositoryscans.core.orka.ai":    `{"items":[{"metadata":{"name":"audit","namespace":"other"},"spec":{"analysisAgentRef":{"name":"target","namespace":"home"}}},{"metadata":{"name":"patch","namespace":"home"},"spec":{"patchAgentRef":{"name":"target"}}}]}`,
		"repositorymonitors.core.orka.ai": `{"items":[{"metadata":{"name":"watch","namespace":"other"},"spec":{"agents":{"reviewer":{"name":"target","namespace":"home"},"triager":{"name":"not-target"}}}},{"metadata":{"name":"repair","namespace":"home"},"spec":{"agents":{"implementer":{"name":"target"}}}}]}`,
		"agents.core.orka.ai":             `{"items":[{"metadata":{"name":"coordinator","namespace":"other"},"spec":{"coordination":{"allowedAgents":[{"name":"target","namespace":"home"}]}}},{"metadata":{"name":"target","namespace":"home"},"spec":{"coordination":{"allowedAgents":[{"name":"target"}]}}}]}`,
	})
	got, err := a.retireAgentDependents(context.Background(), "home", "target")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Agent other/coordinator (coordination.allowedAgents)", "GatewayBinding home/ingress (spec.agentRef)", "RepositoryMonitor home/repair (spec.agents.implementer)", "RepositoryMonitor other/watch (spec.agents.reviewer)", "RepositoryScan home/patch (spec.patchAgentRef)", "RepositoryScan other/audit (spec.analysisAgentRef)", "Task home/finalizing (Finalizing)", "Task home/running (Running)", "Task home/scheduled (Scheduled)", "Task other/pending (Pending)"}
	if !slices.Equal(got, want) {
		t.Fatalf("dependents = %q; want %q", got, want)
	}
}

func TestRetireProviderDependentsRefuseOtherAgentAndActiveTask(t *testing.T) {
	a := dependentsFixture(t, map[string]string{
		"agents.core.orka.ai": `{"items":[{"metadata":{"name":"another","namespace":"home"},"spec":{"providerRef":{"name":"target"}}},{"metadata":{"name":"fallback","namespace":"home"},"spec":{"model":{"fallbacks":[{"providerRef":"target"}]}}},{"metadata":{"name":"target","namespace":"home"},"spec":{"providerRef":{"name":"target"}}}]}`,
		"tasks.core.orka.ai":  `{"items":[{"metadata":{"name":"task","namespace":"home"},"spec":{"ai":{"providerRef":{"name":"target"}}},"status":{"phase":"Running"}}]}`,
	})
	got, err := a.retireProviderDependents(context.Background(), "home", "target")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Agent home/another (spec.providerRef)", "Agent home/fallback (spec.model.fallbacks.providerRef)", "Task home/task (spec.ai.providerRef; Running)"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s in %v", want, got)
		}
	}
}

func TestRetireAgentDependentsMissingPhaseIsNotTerminal(t *testing.T) {
	a := dependentsFixture(t, map[string]string{"tasks.core.orka.ai": `{"items":[{"metadata":{"name":"fresh","namespace":"home"},"spec":{"agentRef":{"name":"target"}}}]}`})
	got, err := a.retireAgentDependents(context.Background(), "home", "target")
	if err != nil || !slices.Equal(got, []string{"Task home/fresh (phase not yet reported)"}) {
		t.Fatalf("fresh Task not blocking: %v %v", got, err)
	}
}

func TestRetireAgentDependentsListFailureFailsClosed(t *testing.T) {
	for _, kind := range []string{"tasks.core.orka.ai", "gatewaybindings.gateway.orka.ai", "repositoryscans.core.orka.ai", "repositorymonitors.core.orka.ai", "agents.core.orka.ai"} {
		t.Run(kind, func(t *testing.T) {
			a := dependentsFixture(t, nil)
			t.Setenv("KMX_DEPENDENTS_DENIED", kind)
			t.Setenv("KMX_DEPENDENTS_ERROR", `Error from server (Forbidden): tasks is forbidden: User "private-token-must-not-escape" cannot list resource`)
			got, err := a.retireAgentDependents(context.Background(), "home", "target")
			if err == nil || !strings.Contains(err.Error(), kind) || !strings.Contains(err.Error(), "cluster-wide list permission") || strings.Contains(err.Error(), "private-token") || len(got) > 0 {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestRetireDependentsProjectionOmitsSensitiveFields(t *testing.T) {
	projection := retireDependentsProjection()
	for _, forbidden := range []string{"{@}", ".spec.prompt", ".spec.systemPrompt", ".spec.credentials", "-o=json"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("projection includes sensitive field %s", forbidden)
		}
	}
	a := dependentsFixture(t, map[string]string{"tasks.core.orka.ai": `{"items":[{"metadata":{"name":"task","namespace":"home"},"spec":{"agentRef":{"name":"target"},"prompt":"private-token-must-not-escape"},"status":{"phase":"Running"}}]}`})
	got, err := a.retireAgentDependents(context.Background(), "home", "target")
	if err != nil || !slices.Equal(got, []string{"Task home/task (Running)"}) {
		t.Fatalf("projected inventory: %v, %v", got, err)
	}
}

func TestRetireDependentsNonRBACFailureAndOversize(t *testing.T) {
	for _, tc := range []struct{ name, stderr string }{{"timeout", "Unable to connect to server: context deadline exceeded private-token-must-not-escape"}, {"other", "transport private-token-must-not-escape"}} {
		t.Run(tc.name, func(t *testing.T) {
			a := dependentsFixture(t, nil)
			t.Setenv("KMX_DEPENDENTS_DENIED", "tasks.core.orka.ai")
			t.Setenv("KMX_DEPENDENTS_ERROR", tc.stderr)
			_, err := a.retireProviderDependents(context.Background(), "home", "target")
			if err == nil || strings.Contains(err.Error(), "cluster-wide list permission") || strings.Contains(err.Error(), "private-token") || !strings.Contains(err.Error(), "tasks.core.orka.ai") || tc.name == "timeout" && !strings.Contains(err.Error(), "timed out") {
				t.Fatalf("unsafe/misclassified failure: %v", err)
			}
		})
	}
	a := dependentsFixture(t, nil)
	t.Setenv("KMX_DEPENDENTS_OVERSIZE", "tasks.core.orka.ai")
	_, err := a.retireAgentDependents(context.Background(), "home", "target")
	if err == nil || !strings.Contains(err.Error(), "size limit") || strings.Contains(err.Error(), "cluster-wide list permission") {
		t.Fatalf("oversize response: %v", err)
	}
}

func TestRetireAgentDependentsMalformedListFailsClosed(t *testing.T) {
	for name, list := range map[string]string{
		"null list":        `{"items":null}`,
		"missing identity": `{"items":[{"metadata":{"name":"dependent"},"spec":{"agentRef":{"name":"target"}}}]}`,
		"incomplete ref":   `{"items":[{"metadata":{"name":"dependent","namespace":"home"},"spec":{"agentRef":{"namespace":"home"}}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			a := dependentsFixture(t, map[string]string{"agents.core.orka.ai": list})
			got, err := a.retireAgentDependents(context.Background(), "home", "target")
			if err == nil || len(got) > 0 || strings.Contains(err.Error(), "cluster-wide list permission") {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestRetireDependentsCancelledFailsClosed(t *testing.T) {
	a := dependentsFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := a.retireProviderDependents(ctx, "home", "target")
	if err == nil || len(got) > 0 || !strings.Contains(err.Error(), "tasks.core.orka.ai") || strings.Contains(err.Error(), "cluster-wide list permission") {
		t.Fatalf("got %v, %v", got, err)
	}
}
