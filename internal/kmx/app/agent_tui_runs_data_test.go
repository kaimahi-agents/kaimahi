package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func consoleRunsFixture(t *testing.T, body string) (*App, agentTUIEnvironment, agentTUIAgent, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	fakeTool(t, dir, "kubectl", `printf '%s|%s\n' "$KUBECONFIG" "$*" >> "$KMX_RUN_CALLS"
case "$*" in
  *'get tasks.core.orka.ai -o json'*) printf '%s' "$KMX_TASK_LIST" ;;
  *) exit 86 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_RUN_CALLS", log)
	t.Setenv("KMX_TASK_LIST", body)
	return &App{Cfg: &config.Config{KubeContext: "wrong-default"}, Run: &run.Runner{}},
		agentTUIEnvironment{Name: "selected", Kubeconfig: filepath.Join(dir, "selected-kubeconfig")},
		agentTUIAgent{Name: "lead", Namespace: "team", Runtime: "orka"}, log
}

func TestConsoleRecentOrkaRunsPinsContextAndOnlyListsTasks(t *testing.T) {
	a, env, agent, log := consoleRunsFixture(t, `{"items":[]}`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	got, err := a.consoleRecentOrkaRuns(ctx, env, agent)
	if err != nil || got.Count != 0 || got.Missing != "" || len(got.Roots) != 0 {
		t.Fatalf("empty list: %+v, %v", got, err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := env.Kubeconfig + "|--context selected --request-timeout=10s -n team get tasks.core.orka.ai -o json\n"
	if string(calls) != want {
		t.Fatalf("unexpected kubectl reads or writes: %q, want %q", calls, want)
	}
	cancel()
	if _, err := a.consoleRecentOrkaRuns(ctx, env, agent); err == nil {
		t.Fatal("cancelled read succeeded")
	}
}

func TestConsoleRecentOrkaRunsExcludesWrongAgentNamespaceAndDelegatedChildren(t *testing.T) {
	body := `{"items":[
{"kind":"Task","metadata":{"name":"root","namespace":"team","uid":"root-uid","creationTimestamp":"2025-01-02T03:04:05Z"},"spec":{"agentRef":{"name":"lead"},"prompt":"private-prompt"},"status":{"phase":"Running"}},
{"kind":"Task","metadata":{"name":"other-agent","namespace":"team","uid":"other-uid"},"spec":{"agentRef":{"name":"other"}}},
{"kind":"Task","metadata":{"name":"wrong-ns","namespace":"elsewhere","uid":"wrong-uid"},"spec":{"agentRef":{"name":"lead","namespace":"team"}}},
{"kind":"Task","metadata":{"name":"wrong-ref-ns","namespace":"team","uid":"wrong-ref-uid"},"spec":{"agentRef":{"name":"lead","namespace":"elsewhere"}}},
{"kind":"Task","metadata":{"name":"child","namespace":"team","uid":"child-uid","labels":{"orka.ai/parent-task":"root","orka.ai/coordinator":"true","orka.ai/delegated-agent":"lead"},"annotations":{"orka.ai/parent-task-name":"root"},"ownerReferences":[{"kind":"Task","name":"root","uid":"root-uid"}]},"spec":{"agentRef":{"name":"lead"}}},
{"kind":"Task","metadata":{"name":"scheduled","namespace":"team","uid":"scheduled-uid","ownerReferences":[{"kind":"Task","name":"root","uid":"root-uid"}]},"spec":{"agentRef":{"name":"lead"}},"status":{"phase":"Scheduled"}}
]}`
	a, env, agent, _ := consoleRunsFixture(t, body)
	got, err := a.consoleRecentOrkaRuns(t.Context(), env, agent)
	if err != nil || got.Missing != "" || got.Count != 2 {
		t.Fatalf("roots=%+v err=%v", got, err)
	}
	want := []consoleRunRef{
		{Name: "root", UID: "root-uid", Namespace: "team", Status: "Running", CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)},
		{Name: "scheduled", UID: "scheduled-uid", Namespace: "team", Status: "Scheduled"},
	}
	if !reflect.DeepEqual(got.Roots, want) {
		t.Fatalf("roots=%+v, want %+v", got.Roots, want)
	}
	if strings.Contains(fmt.Sprintf("%+v", got), "private-prompt") {
		t.Fatal("prompt exposed in run references")
	}
}

func TestConsoleRecentOrkaRunsDoesNotHideUnverifiedDelegation(t *testing.T) {
	body := `{"items":[{"kind":"Task","metadata":{"name":"orphan","namespace":"team","uid":"orphan-uid","labels":{"orka.ai/parent-task":"root","orka.ai/coordinator":"true","orka.ai/delegated-agent":"lead"},"ownerReferences":[{"kind":"Task","name":"root","uid":"old-uid"}]},"spec":{"agentRef":{"name":"lead"}}}]}`
	a, env, agent, _ := consoleRunsFixture(t, body)
	got, err := a.consoleRecentOrkaRuns(t.Context(), env, agent)
	if err != nil || got.Count != 1 || got.Roots[0].Name != "orphan" {
		t.Fatalf("unverified parent incorrectly hid Task: %+v err=%v", got, err)
	}
}

func TestConsoleRecentOrkaRunsAcceptsNativeSubdomainTaskName(t *testing.T) {
	a, env, agent, _ := consoleRunsFixture(t, `{"items":[{"kind":"Task","metadata":{"name":"nightly.invoice-check","namespace":"team","uid":"run-uid"},"spec":{"agentRef":{"name":"lead"}}}]}`)
	got, err := a.consoleRecentOrkaRuns(t.Context(), env, agent)
	if err != nil || got.Count != 1 || got.Missing != "" || got.Roots[0].Name != "nightly.invoice-check" {
		t.Fatalf("native Task name: %+v err=%v", got, err)
	}
}
func TestConsoleRecentOrkaRunsOrdersAndCapsWithCountBeforeTruncation(t *testing.T) {
	var items []string
	for i := 0; i < 23; i++ {
		// Same timestamp tests the deterministic UID tie-break independently of list order.
		items = append(items, fmt.Sprintf(`{"kind":"Task","metadata":{"name":"task-%02d","namespace":"team","uid":"uid-%02d","creationTimestamp":"2025-01-01T00:00:00Z"},"spec":{"agentRef":{"name":"lead"}}}`, 22-i, 22-i))
	}
	items = append(items, `{"kind":"Task","metadata":{"name":"newest","namespace":"team","uid":"latest","creationTimestamp":"2025-02-01T00:00:00Z"},"spec":{"agentRef":{"name":"lead"}}}`)
	a, env, agent, _ := consoleRunsFixture(t, `{"items":[`+strings.Join(items, ",")+`]}`)
	got, err := a.consoleRecentOrkaRuns(t.Context(), env, agent)
	if err != nil || got.Missing != "" || got.Count != 24 || len(got.Roots) != 20 {
		t.Fatalf("roots=%+v err=%v", got, err)
	}
	if got.Roots[0].UID != "latest" || got.Roots[1].UID != "uid-00" || got.Roots[19].UID != "uid-18" {
		t.Fatalf("non-deterministic order: first=%+v last=%+v", got.Roots[0], got.Roots[19])
	}
}

func TestConsoleRecentOrkaRunsMalformedItemIsMissingNotEmpty(t *testing.T) {
	for name, item := range map[string]string{
		"null item":            `null`,
		"missing name":         `{"metadata":{"namespace":"team","uid":"x"},"spec":{"agentRef":{"name":"lead"}}}`,
		"invalid name":         `{"metadata":{"name":"../private","namespace":"team","uid":"x"},"spec":{"agentRef":{"name":"lead"},"prompt":"private-prompt"}}`,
		"missing namespace":    `{"metadata":{"name":"broken","uid":"x"},"spec":{"agentRef":{"name":"lead"}}}`,
		"missing UID":          `{"metadata":{"name":"broken","namespace":"team"},"spec":{"agentRef":{"name":"lead"}}}`,
		"wrong runtime kind":   `{"kind":"Pod","metadata":{"name":"broken","namespace":"team","uid":"x"},"spec":{"agentRef":{"name":"lead"}}}`,
		"invalid timestamp":    `{"metadata":{"name":"broken","namespace":"team","uid":"x","creationTimestamp":"yesterday"},"spec":{"agentRef":{"name":"lead"}}}`,
		"incomplete agent ref": `{"metadata":{"name":"broken","namespace":"team","uid":"x"},"spec":{"agentRef":{}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			valid := `{"metadata":{"name":"good","namespace":"team","uid":"good-uid"},"spec":{"agentRef":{"name":"lead"}}}`
			a, env, agent, _ := consoleRunsFixture(t, `{"items":[`+valid+`,`+item+`]}`)
			got, err := a.consoleRecentOrkaRuns(t.Context(), env, agent)
			if err != nil || got.Missing == "" || strings.Contains(got.Missing, "private-prompt") || got.Count != 1 || len(got.Roots) != 1 || got.Roots[0].UID != "good-uid" {
				t.Fatalf("malformed item silently changed inventory: %+v err=%v", got, err)
			}
		})
	}
}

func TestConsoleRecentOrkaRunsRejectsUnreadableOrMalformedListWithoutLeakingData(t *testing.T) {
	for name, body := range map[string]string{
		"null response": `null`,
		"missing items": `{"kind":"Status","message":"private-prompt"}`,
		"null items":    `{"items":null}`,
		"invalid JSON":  `{"items":[private-prompt]}`,
	} {
		t.Run(name, func(t *testing.T) {
			a, env, agent, _ := consoleRunsFixture(t, body)
			got, err := a.consoleRecentOrkaRuns(t.Context(), env, agent)
			if err == nil || strings.Contains(err.Error(), "private-prompt") || got.Count != 0 || len(got.Roots) != 0 {
				t.Fatalf("malformed list accepted/leaked: %+v, %v", got, err)
			}
		})
	}
	t.Run("forbidden", func(t *testing.T) {
		dir := t.TempDir()
		fakeTool(t, dir, "kubectl", `printf 'private-prompt: forbidden' >&2; exit 1`)
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		a := &App{Cfg: &config.Config{}, Run: &run.Runner{}}
		got, err := a.consoleRecentOrkaRuns(context.Background(), agentTUIEnvironment{Name: "selected"}, agentTUIAgent{Name: "lead", Namespace: "team"})
		if err == nil || strings.Contains(err.Error(), "private-prompt") || got.Count != 0 || len(got.Roots) != 0 {
			t.Fatalf("forbidden list accepted/leaked: %+v, %v", got, err)
		}
	})
}
