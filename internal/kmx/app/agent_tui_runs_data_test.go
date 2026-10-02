package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
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
  *'get --raw /apis/core.orka.ai/v1alpha1/namespaces/team/tasks?limit=2'*) printf '%s' "$KMX_TASK_LIST" ;;
  *) exit 86 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_RUN_CALLS", log)
	t.Setenv("KMX_TASK_LIST", body)
	return &App{Cfg: &config.Config{KubeContext: "wrong-default"}, Run: &run.Runner{}},
		agentTUIEnvironment{Name: "selected", Kubeconfig: filepath.Join(dir, "selected-kubeconfig")},
		agentTUIAgent{Name: "lead", Namespace: "team", Runtime: "orka"}, log
}

// Pages are real kubectl stdout; each invocation gets a distinct Kubernetes cursor.
func consoleRunsPagesFixture(t *testing.T, pages ...string) (*App, agentTUIEnvironment, agentTUIAgent, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	for i, page := range pages {
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(i)), []byte(page), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fakeTool(t, dir, "kubectl", `printf '%s|%s\n' "$KUBECONFIG" "$*" >> "$KMX_RUN_CALLS"
case "$*" in
  *'get --raw /apis/core.orka.ai/v1alpha1/namespaces/team/tasks?limit=2') page=0 ;;
  *'get --raw /apis/core.orka.ai/v1alpha1/namespaces/team/tasks?limit=2&continue=next+%2F%2B') page=1 ;;
  *'get --raw /apis/core.orka.ai/v1alpha1/namespaces/team/tasks?limit=2&continue=cursor'*) for arg do case "$arg" in *'&continue=cursor'*) page=${arg##*continue=cursor} ;; esac; done ;;
  *) exit 86 ;;
esac
[ -f "$KMX_TASK_PAGES/$page" ] || { printf 'private-prompt forbidden' >&2; exit 1; }
dd if="$KMX_TASK_PAGES/$page" status=none`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_RUN_CALLS", log)
	t.Setenv("KMX_TASK_PAGES", dir)
	return &App{Cfg: &config.Config{KubeContext: "wrong-default"}, Run: &run.Runner{}},
		agentTUIEnvironment{Name: "selected", Kubeconfig: filepath.Join(dir, "selected-kubeconfig")},
		agentTUIAgent{Name: "lead", Namespace: "team", Runtime: "orka"}, log
}

func taskPage(cursor string, items ...string) string {
	return fmt.Sprintf(`{"metadata":{"continue":%q},"items":[%s]}`, cursor, strings.Join(items, ","))
}

func rootItem(name, timestamp, prompt string) string {
	return fmt.Sprintf(`{"kind":"Task","metadata":{"name":%q,"namespace":"team","uid":%q,"creationTimestamp":%q},"spec":{"agentRef":{"name":"lead"},"prompt":%q}}`, name, name+"-uid", timestamp, prompt)
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
	want := env.Kubeconfig + "|--context selected --request-timeout=10s get --raw /apis/core.orka.ai/v1alpha1/namespaces/team/tasks?limit=2\n"
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
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	var pages []string
	for i := 0; i < len(list.Items); i += 2 {
		cursor := ""
		if i == 0 {
			cursor = "next /+"
		} else if i+2 < len(list.Items) {
			cursor = fmt.Sprintf("cursor%d", i/2+1)
		}
		pages = append(pages, taskPage(cursor, string(list.Items[i]), string(list.Items[i+1])))
	}
	a, env, agent, _ := consoleRunsPagesFixture(t, pages...)
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
	var pages []string
	for i := 0; i < len(items); i += 2 {
		cursor := ""
		if i+2 < len(items) {
			cursor = fmt.Sprintf("cursor%d", i/2+1)
			if i == 0 {
				cursor = "next /+"
			}
		}
		pages = append(pages, taskPage(cursor, items[i:i+2]...))
	}
	a, env, agent, _ := consoleRunsPagesFixture(t, pages...)
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

func TestConsoleRecentOrkaRunsUsesRealKubectlRawPagination(t *testing.T) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl not installed")
	}
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/apis/core.orka.ai/v1alpha1/namespaces/team/tasks" || r.URL.Query().Get("limit") != "2" {
			t.Errorf("unexpected Kubernetes request: %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		queries = append(queries, r.URL.RawQuery)
		switch r.URL.RawQuery {
		case "limit=2&timeout=10s":
			fmt.Fprint(w, taskPage("next /+", rootItem("older", "2025-01-01T00:00:00Z", "private-prompt")))
		case "continue=next+%2F%2B&limit=2&timeout=10s":
			fmt.Fprint(w, taskPage("", rootItem("newest", "2025-02-01T00:00:00Z", "private-prompt")))
		default:
			t.Errorf("incorrect raw cursor: %q", r.URL.RawQuery)
			http.Error(w, "incorrect cursor", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	kubeconfig := filepath.Join(t.TempDir(), "config")
	content := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: local\n  cluster:\n    server: %s\ncontexts:\n- name: selected\n  context:\n    cluster: local\n    user: local\ncurrent-context: selected\nusers:\n- name: local\n  user: {}\n", server.URL)
	if err := os.WriteFile(kubeconfig, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &App{Cfg: &config.Config{KubeContext: "wrong-default"}, Run: &run.Runner{}}
	got, err := a.consoleRecentOrkaRuns(t.Context(), agentTUIEnvironment{Name: "selected", Kubeconfig: kubeconfig}, agentTUIAgent{Name: "lead", Namespace: "team"})
	if err != nil || got.Missing != "" || got.Count != 2 || got.Roots[0].Name != "newest" || strings.Contains(fmt.Sprintf("%+v", got), "private-prompt") || !reflect.DeepEqual(queries, []string{"limit=2&timeout=10s", "continue=next+%2F%2B&limit=2&timeout=10s"}) {
		t.Fatalf("real kubectl raw pagination: %+v err=%v queries=%q", got, err, queries)
	}
}

func TestConsoleRecentOrkaRunsScansLargePagesAndSortsGlobally(t *testing.T) {
	pages := []string{
		taskPage("next /+", rootItem("middle", "2025-01-02T00:00:00Z", strings.Repeat("p", 1<<20))),
		taskPage("cursor2", rootItem("older", "2025-01-01T00:00:00Z", strings.Repeat("p", 1<<20))),
		taskPage("cursor3", rootItem("middle-two", "2025-01-03T00:00:00Z", strings.Repeat("p", 1<<20))),
		taskPage("cursor4", rootItem("middle-three", "2025-01-04T00:00:00Z", strings.Repeat("p", 1<<20))),
		taskPage("", rootItem("newest", "2025-01-05T00:00:00Z", strings.Repeat("p", 1<<20))),
	}
	a, env, agent, log := consoleRunsPagesFixture(t, pages...)
	got, err := a.consoleRecentOrkaRuns(t.Context(), env, agent)
	if err != nil || got.Missing != "" || got.Count != 5 || len(got.Roots) != 5 {
		t.Fatalf("large multi-page list: %+v err=%v", got, err)
	}
	want := []string{"newest", "middle-three", "middle-two", "middle", "older"}
	for i, name := range want {
		if got.Roots[i].Name != name {
			t.Fatalf("root %d = %q, want %q", i, got.Roots[i].Name, name)
		}
	}
	if strings.Contains(fmt.Sprintf("%+v", got), "pppp") {
		t.Fatal("Task prompt leaked into references")
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "--context selected --request-timeout=10s get --raw /apis/core.orka.ai/v1alpha1/namespaces/team/tasks?limit=2") != 5 || strings.Contains(string(calls), " create ") {
		t.Fatalf("wrong context, path or writes: %q", calls)
	}
}

func TestConsoleRecentOrkaRunsStopsAtScanCapWithPartialResults(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	for i := 0; i < 100; i++ {
		items := []string{rootItem(fmt.Sprintf("run-%03d", i*2), "2025-01-01T00:00:00Z", ""), rootItem(fmt.Sprintf("run-%03d", i*2+1), "2025-01-01T00:00:00Z", "")}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("c%d", i)), []byte(taskPage(fmt.Sprintf("c%d", i+1), items...)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fakeTool(t, dir, "kubectl", `printf '%s\n' "$*" >> "$KMX_RUN_CALLS"
case "$*" in
  *'get --raw /apis/core.orka.ai/v1alpha1/namespaces/team/tasks?limit=2') page=c0 ;;
  *'get --raw /apis/core.orka.ai/v1alpha1/namespaces/team/tasks?limit=2&continue=c'*) for arg do case "$arg" in *'&continue=c'*) page=c${arg##*continue=c} ;; esac; done ;;
  *) exit 86 ;;
esac
[ -f "$KMX_TASK_PAGES/$page" ] || exit 86
dd if="$KMX_TASK_PAGES/$page" status=none`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_RUN_CALLS", log)
	t.Setenv("KMX_TASK_PAGES", dir)
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{}}
	got, err := a.consoleRecentOrkaRuns(t.Context(), agentTUIEnvironment{Name: "selected"}, agentTUIAgent{Name: "lead", Namespace: "team"})
	if err != nil || got.Missing == "" || got.Count != 200 || len(got.Roots) != 20 {
		t.Fatalf("scan cap: %+v err=%v", got, err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(string(calls), "get --raw") != 100 {
		t.Fatalf("scan exceeded 200 tasks: %d pages", strings.Count(string(calls), "get --raw"))
	}
	// Uneven page sizes must not let the last page push the scan to 201.
	if err := os.WriteFile(filepath.Join(dir, "c0"), []byte(taskPage("c1", rootItem("run-000", "2025-01-01T00:00:00Z", ""))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c100"), []byte(taskPage("", rootItem("extra-one", "2025-01-01T00:00:00Z", ""), rootItem("extra-two", "2025-01-01T00:00:00Z", ""))), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = a.consoleRecentOrkaRuns(t.Context(), agentTUIEnvironment{Name: "selected"}, agentTUIAgent{Name: "lead", Namespace: "team"})
	if err != nil || got.Missing == "" || got.Count != 199 || len(got.Roots) != 20 {
		t.Fatalf("uneven page crossed scan cap: %+v err=%v", got, err)
	}
}

func TestConsoleRecentOrkaRunsLaterPageFailureRetainsSafeRoots(t *testing.T) {
	a, env, agent, _ := consoleRunsPagesFixture(t, taskPage("next /+", rootItem("safe", "2025-01-01T00:00:00Z", "private-prompt")))
	got, err := a.consoleRecentOrkaRuns(t.Context(), env, agent)
	if err != nil || got.Count != 1 || len(got.Roots) != 1 || got.Roots[0].Name != "safe" || got.Missing == "" || strings.Contains(got.Missing, "private-prompt") {
		t.Fatalf("lost safe partial results or leaked error: %+v err=%v", got, err)
	}
}

func TestConsoleRecentOrkaRunsCancellationAfterFirstPageKeepsRoots(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `case "$*" in
  *'get --raw /apis/core.orka.ai/v1alpha1/namespaces/team/tasks?limit=2') printf '%s' "$KMX_TASK_LIST" ;;
  *'get --raw /apis/core.orka.ai/v1alpha1/namespaces/team/tasks?limit=2&continue=next') sleep 2 ;;
  *) exit 86 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TASK_LIST", taskPage("next", rootItem("safe", "2025-01-01T00:00:00Z", "private-prompt")))
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{}}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	got, err := a.consoleRecentOrkaRuns(ctx, agentTUIEnvironment{Name: "selected"}, agentTUIAgent{Name: "lead", Namespace: "team"})
	if err != nil || got.Count != 1 || got.Roots[0].Name != "safe" || got.Missing == "" || strings.Contains(fmt.Sprintf("%+v", got), "private-prompt") {
		t.Fatalf("cancelled pagination lost safe roots: %+v err=%v", got, err)
	}
}

func TestConsoleRecentOrkaRunsRepeatedCursorIsPartial(t *testing.T) {
	a, env, agent, log := consoleRunsPagesFixture(t, taskPage("next /+", rootItem("safe", "2025-01-01T00:00:00Z", "")), taskPage("next /+", rootItem("second", "2025-01-02T00:00:00Z", "")))
	got, err := a.consoleRecentOrkaRuns(t.Context(), env, agent)
	if err != nil || got.Missing == "" || got.Count != 2 || len(got.Roots) != 2 {
		t.Fatalf("cursor cycle accepted: %+v err=%v", got, err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(string(calls), "get --raw") != 2 {
		t.Fatalf("cursor cycle continued: %q", calls)
	}
}

func TestConsoleRecentOrkaRunsRejectsOverLimitPageAndMalformedLaterPage(t *testing.T) {
	t.Run("over limit", func(t *testing.T) {
		a, env, agent, _ := consoleRunsPagesFixture(t, taskPage("next /+", rootItem("safe", "2025-01-01T00:00:00Z", "")), taskPage("", rootItem("one", "", ""), rootItem("two", "", ""), rootItem("three", "", "")))
		got, err := a.consoleRecentOrkaRuns(t.Context(), env, agent)
		if err != nil || got.Count != 1 || got.Missing == "" || got.Roots[0].Name != "safe" {
			t.Fatalf("accepted oversized page: %+v err=%v", got, err)
		}
	})
	for name, body := range map[string]string{
		"null items": `{"items":null}`, "non-array items": `{"items":{}}`, "null cursor": `{"metadata":{"continue":null},"items":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			a, env, agent, _ := consoleRunsPagesFixture(t, taskPage("next /+", rootItem("safe", "2025-01-01T00:00:00Z", "")), body)
			got, err := a.consoleRecentOrkaRuns(t.Context(), env, agent)
			if err != nil || got.Count != 1 || got.Missing == "" || got.Roots[0].Name != "safe" {
				t.Fatalf("malformed page erased safe roots: %+v err=%v", got, err)
			}
		})
	}
}

func TestConsoleRecentOrkaRunsDoesNotExposeUnexpectedStatusPayload(t *testing.T) {
	body := `{"items":[{"kind":"Task","metadata":{"name":"safe","namespace":"team","uid":"safe-uid"},"spec":{"agentRef":{"name":"lead"},"prompt":"private-prompt"},"status":{"phase":"private-prompt"}}]}`
	a, env, agent, _ := consoleRunsFixture(t, body)
	got, err := a.consoleRecentOrkaRuns(t.Context(), env, agent)
	if err != nil || got.Count != 1 || strings.Contains(fmt.Sprintf("%+v", got), "private-prompt") {
		t.Fatalf("unexpected status leaked Task payload: %+v err=%v", got, err)
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
