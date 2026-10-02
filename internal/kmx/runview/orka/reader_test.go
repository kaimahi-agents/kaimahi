package orka

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runview"
)

type fixtureSource struct {
	tasks      map[string]string
	children   map[string][]string
	events     map[string]func(int64) string
	denied     map[string]bool
	listDenied bool
	traces     map[string]int
}

func (f fixtureSource) Task(_ context.Context, _, name string) ([]byte, error) {
	if f.denied[name] {
		return nil, ErrDenied
	}
	if raw, ok := f.tasks[name]; ok {
		return []byte(raw), nil
	}
	return nil, errors.New("not found")
}
func (f fixtureSource) Children(_ context.Context, _, name string) ([][]byte, error) {
	if f.listDenied {
		return nil, ErrDenied
	}
	var out [][]byte
	for _, child := range f.children[name] {
		out = append(out, []byte(f.tasks[child]))
	}
	return out, nil
}
func (f fixtureSource) Events(_ context.Context, _, name string, after int64, _ int) ([]byte, int, error) {
	if f.denied[name] {
		return nil, 403, nil
	}
	if fn := f.events[name]; fn != nil {
		return []byte(fn(after)), 200, nil
	}
	return nil, 501, nil
}
func (f fixtureSource) Trace(_ context.Context, _, name string) (int, error) {
	if n := f.traces[name]; n != 0 {
		return n, nil
	}
	return 501, nil
}
func (f fixtureSource) Helpers(context.Context, string, string) (HelperPolicy, error) {
	return HelperPolicy{State: "enabled", Helpers: []runview.Agent{{Name: "helper"}, {Name: "idle"}}}, nil
}

func task(name, uid, agent, phase, parent, parentUID string) string {
	metadata := fmt.Sprintf(`"name":%q,"namespace":"team","uid":%q,"creationTimestamp":"2026-10-01T01:00:00Z"`, name, uid)
	if parent != "" {
		metadata += fmt.Sprintf(`,"labels":{"orka.ai/parent-task":%q,"orka.ai/delegated-agent":%q,"orka.ai/coordinator":"true"},"annotations":{"orka.ai/parent-task-name":%q},"ownerReferences":[{"kind":"Task","name":%q,"uid":%q}]`, parent, agent, parent, parent, parentUID)
	}
	return fmt.Sprintf(`{"kind":"Task","metadata":{%s},"spec":{"agentRef":{"name":%q}},"status":{"phase":%q,"completionTime":"2026-10-01T02:00:00Z"}}`, metadata, agent, phase)
}
func page(name string, latest, after int64, seqs ...int64) string {
	entries := []string{}
	for _, seq := range seqs {
		entries = append(entries, fmt.Sprintf(`{"seq":%d,"type":"TaskSucceeded","createdAt":"2026-10-01T01:01:00Z","summary":"PRIVATE MODEL TEXT","contentText":"PRIVATE PROMPT"}`, seq))
	}
	return fmt.Sprintf(`{"namespace":"team","streamType":"task","streamID":%q,"latestSeq":%d,"afterSeq":%d,"events":[%s]}`, name, latest, after, strings.Join(entries, ","))
}
func TestReadRunDeduplicatesOverlapAndRedactsPayload(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Succeeded", "", "")}, events: map[string]func(int64) string{"root": func(after int64) string {
		if after == 0 {
			return page("root", 3, 0, 1, 2)
		}
		return page("root", 3, after, 2, 3)
	}}, traces: map[string]int{"root": 200}}
	run, err := NewReader(f).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != "uid-root" || run.Status != "Succeeded" || len(run.Events) != 3 || run.Events[2].Seq != 3 {
		t.Fatalf("projection: %+v", run)
	}
	if run.TraceMissing != nil {
		t.Fatalf("trace missing: %+v", run.TraceMissing)
	}
	if strings.Contains(fmt.Sprintf("%+v", run), "PRIVATE") {
		t.Fatal("private event content escaped projection")
	}
	if len(run.DeclaredHelpers) != 2 || len(run.HandOffs) != 0 {
		t.Fatalf("relations: %+v", run)
	}
}
func TestReadRunIgnoresReusedNameAndScheduledChild(t *testing.T) {
	root := task("root", "uid-root", "lead", "Running", "", "")
	f := fixtureSource{tasks: map[string]string{"root": root, "replacement": task("replacement", "uid-new", "helper", "Running", "root", "uid-old"), "scheduled": strings.Replace(task("scheduled", "uid-scheduled", "helper", "Scheduled", "root", "uid-root"), `"orka.ai/delegated-agent":"helper",`, "", 1)}, children: map[string][]string{"root": {"replacement", "scheduled"}}}
	run, err := NewReader(f).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Tasks) != 1 || len(run.HandOffs) != 0 {
		t.Fatalf("invalid child treated as delegation: %+v", run)
	}
}
func TestReadRunDoesNotAssignParentToScheduledRoot(t *testing.T) {
	root := strings.Replace(task("root", "uid-root", "lead", "Scheduled", "", ""), `"uid":"uid-root"`, `"uid":"uid-root","ownerReferences":[{"kind":"Task","name":"template","uid":"template-uid"}]`, 1)
	f := fixtureSource{tasks: map[string]string{"root": root}}
	run, err := NewReader(f).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.Tasks[0].ParentTask != "" || len(run.HandOffs) != 0 {
		t.Fatalf("scheduled root acquired delegation parent: %+v", run)
	}
}
func TestReadRunChildFailureDoesNotEndRootAndChildTerminalAfterRoot(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Succeeded", "", ""), "failed": task("failed", "uid-f", "helper", "Failed", "root", "uid-root"), "late": strings.Replace(task("late", "uid-l", "helper", "Succeeded", "root", "uid-root"), "02:00:00Z", "03:00:00Z", 1)}, children: map[string][]string{"root": {"failed", "late"}}}
	run, err := NewReader(f).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "Succeeded" || len(run.HandOffs) != 2 || len(run.Tasks) != 3 {
		t.Fatalf("child terminal changed run: %+v", run)
	}
	if run.Tasks[1].Status != "Failed" || run.Tasks[2].Status != "Succeeded" || !run.Tasks[2].FinishedAt.After(run.FinishedAt) {
		t.Fatalf("child status lost: %+v", run.Tasks)
	}
}
func TestReadRunMarksMalformedListedTaskAsIncompleteDiscovery(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Running", "", ""), "broken": `{"kind":"Task","metadata":{"name":"broken","namespace":"team"}}`}, children: map[string][]string{"root": {"broken"}}}
	run, err := NewReader(f).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Tasks) != 1 || run.DiscoveryMissing == nil || run.DiscoveryMissing.Reason != "invalid Task list item" {
		t.Fatalf("malformed list item: %+v", run)
	}
}
func TestReadRunDeduplicatesChildListedTwice(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Running", "", ""), "child": task("child", "uid-c", "helper", "Running", "root", "uid-root")}, children: map[string][]string{"root": {"child", "child"}}}
	run, err := NewReader(f).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Tasks) != 2 || len(run.HandOffs) != 1 {
		t.Fatalf("duplicate child: %+v", run)
	}
}
func TestReadRunReportsMissingHistoryAndPermission(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Running", "", ""), "hidden": task("hidden", "uid-h", "helper", "Running", "root", "uid-root")}, children: map[string][]string{"root": {"hidden"}}, denied: map[string]bool{"hidden": true}, listDenied: true}
	// Without list permission and without a native child name, discovery must not be claimed complete.
	run, err := NewReader(f).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.DiscoveryMissing == nil || run.DiscoveryMissing.Reason != "permission denied" || run.EventsMissing == nil {
		t.Fatalf("missing evidence not shown: %+v", run)
	}
	if run.Status != "Running" {
		t.Fatalf("connection/history error changed root status: %s", run.Status)
	}
	f.listDenied = false
	run, err = NewReader(f).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Tasks) != 1 || run.DiscoveryMissing == nil {
		t.Fatalf("denied child treated as visible: %+v", run)
	}
}
func TestParentSelectorTrimsPunctuationAtHashBoundary(t *testing.T) {
	name := strings.Repeat("a", 49) + "-" + strings.Repeat("b", 20)
	if got := ParentSelector(name); got != strings.Repeat("a", 49)+"-0517873f1a55" {
		t.Fatalf("label selector=%q", got)
	}
}
func TestParentSelectorHandlesLongTaskNames(t *testing.T) {
	if ParentSelector("root") != "root" || ParentSelector(strings.Repeat("a", 70)) != strings.Repeat("a", 50)+"-6bd5e5034855" {
		t.Fatal("parent selector differs from Orka label")
	}
}
func TestReadRunRejectsWrongParentLabelEvenWithOwnerUID(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Running", "", ""), "spoof": strings.Replace(task("spoof", "uid-s", "helper", "Running", "root", "uid-root"), `"orka.ai/parent-task":"root"`, `"orka.ai/parent-task":"other"`, 1)}, children: map[string][]string{"root": {"spoof"}}}
	run, err := NewReader(f).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.HandOffs) != 0 {
		t.Fatalf("wrong label admitted: %+v", run.HandOffs)
	}
}
func TestReadRunMarksTaskSpecificMissingEvidenceAndRedactsReason(t *testing.T) {
	root := task("root", "uid-root", "lead", "Succeeded", "", "")
	child := strings.Replace(task("child", "uid-child", "helper", "Failed", "root", "uid-root"), `"phase":"Failed"`, `"phase":"Failed","conditions":[{"type":"Ready","reason":"PRIVATEPROMPT","status":"False"}]`, 1)
	f := fixtureSource{tasks: map[string]string{"root": root, "child": child}, children: map[string][]string{"root": {"child"}}, events: map[string]func(int64) string{"root": func(after int64) string { return page("root", 1, after, 1) }}}
	run, err := NewReader(f).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.Tasks[1].EventsMissing == nil || run.Tasks[0].EventsMissing != nil || run.Tasks[1].FailureReason != "" || run.Tasks[1].FailureMissing == nil {
		t.Fatalf("task evidence: %+v", run.Tasks)
	}
	if !run.Tasks[1].FinishedAt.After(run.Tasks[0].StartedAt) {
		t.Fatal("child times lost")
	}
	if run.HandOffs[0].ObservedAt.IsZero() {
		t.Fatal("observed time lost")
	}
}
func TestReadRunMarksConnectionLossStaleNotFailed(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Running", "", "")}}
	run, err := NewReader(lostEventSource{f}).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "Running" || run.FreshnessMissing == nil || run.FreshnessMissing.Reason != "connection lost" {
		t.Fatalf("lost connection: %+v", run)
	}
}

type lostEventSource struct{ fixtureSource }

func (lostEventSource) Events(context.Context, string, string, int64, int) ([]byte, int, error) {
	return nil, 0, ErrConnectionLost
}

func TestReadRunReadsCoordinatorPolicyFromReferencedAgentNamespace(t *testing.T) {
	root := strings.Replace(task("root", "uid-root", "lead", "Running", "", ""), `"agentRef":{"name":"lead"}`, `"agentRef":{"name":"lead","namespace":"shared"}`, 1)
	var calls []string
	f := policyRecordingSource{fixtureSource: fixtureSource{tasks: map[string]string{"root": root}}, calls: &calls}
	run, err := NewReader(f).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "shared/lead" || run.Agents[0].Namespace != "shared" || run.DeclaredState != "enabled" || len(run.DeclaredHelpers) != 2 || run.DeclaredHelpers[0].To.Namespace != "team" || run.DeclaredHelpers[1].To.Namespace != "partners" {
		t.Fatalf("policy namespace: calls=%v run=%+v", calls, run)
	}
}

type policyRecordingSource struct {
	fixtureSource
	calls *[]string
}

func (s policyRecordingSource) Helpers(_ context.Context, ns, name string) (HelperPolicy, error) {
	*s.calls = append(*s.calls, ns+"/"+name)
	return HelperPolicy{State: "enabled", Helpers: []runview.Agent{{Name: "helper"}, {Name: "remote", Namespace: "partners"}}}, nil
}

func TestReadRunMarksUnavailablePolicyWithoutErasingObservedEdges(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Succeeded", "", ""), "child": task("child", "uid-child", "helper", "Failed", "root", "uid-root")}, children: map[string][]string{"root": {"child"}}}
	run, err := NewReader(deniedHelpersSource{f}).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.DeclaredMissing == nil || run.DeclaredMissing.Reason != "permission denied" || len(run.DeclaredHelpers) != 0 || len(run.HandOffs) != 1 {
		t.Fatalf("policy/observations conflated: %+v", run)
	}
}

type deniedHelpersSource struct{ fixtureSource }

func (deniedHelpersSource) Helpers(context.Context, string, string) (HelperPolicy, error) {
	return HelperPolicy{}, ErrDenied
}

func TestReadRunSeparatesDisabledPolicyFromUnavailablePolicy(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Running", "", "")}}
	run, err := NewReader(inactiveHelpersSource{f}).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.DeclaredState != "disabled" || run.DeclaredMissing != nil || len(run.DeclaredHelpers) != 0 {
		t.Fatalf("disabled policy: %+v", run)
	}
	run, err = NewReader(deniedHelpersSource{f}).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.DeclaredState != "unknown" || run.DeclaredMissing == nil {
		t.Fatalf("unavailable policy: %+v", run)
	}
}

type inactiveHelpersSource struct{ fixtureSource }

func (inactiveHelpersSource) Helpers(context.Context, string, string) (HelperPolicy, error) {
	return HelperPolicy{State: "disabled"}, nil
}

func TestReadRunDoesNotCallUnboundedPolicyAnEmptyHelperGroup(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Running", "", "")}}
	run, err := NewReader(unboundedHelpersSource{f}).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.DeclaredState != "unbounded" || run.DeclaredMissing == nil || run.DeclaredMissing.Reason != "unbounded helper policy" || len(run.DeclaredHelpers) != 0 {
		t.Fatalf("unbounded policy: %+v", run)
	}
}

type unboundedHelpersSource struct{ fixtureSource }

func (unboundedHelpersSource) Helpers(context.Context, string, string) (HelperPolicy, error) {
	return HelperPolicy{State: "unbounded"}, nil
}

func TestReadRunTreatsOversizeTraceAsMissingNotDisconnect(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Succeeded", "", "")}}
	run, err := NewReader(oversizeTraceSource{fixtureSource: f}).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.TraceMissing == nil || run.TraceMissing.Reason != "history too large" || run.FreshnessMissing != nil {
		t.Fatalf("trace size: %+v", run)
	}
}

type oversizeTraceSource struct{ fixtureSource }

func (oversizeTraceSource) Trace(context.Context, string, string) (int, error) {
	return 0, ErrPageTooLarge
}

func TestReadRunNamesMissingHTTPHistoryNotFound(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Succeeded", "", "")}, traces: map[string]int{"root": 404}}
	run, err := NewReader(notFoundEventsSource{f}).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.EventsMissing == nil || run.EventsMissing.Reason != "not found" || run.TraceMissing == nil || run.TraceMissing.Reason != "not found" || run.FreshnessMissing != nil {
		t.Fatalf("missing history: %+v", run)
	}
}

type notFoundEventsSource struct{ fixtureSource }

func (notFoundEventsSource) Events(context.Context, string, string, int64, int) ([]byte, int, error) {
	return nil, 404, nil
}

func TestReadRunReportsEventSequenceGap(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Succeeded", "", "")}, events: map[string]func(int64) string{"root": func(after int64) string {
		if after == 0 {
			return page("root", 3, 0, 1)
		}
		return page("root", 3, after, 3)
	}}}
	run, err := NewReader(f).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.Tasks[0].EventsMissing == nil || run.Tasks[0].EventsMissing.Reason != "history gap" || len(run.Events) != 1 {
		t.Fatalf("gap: %+v", run)
	}
}
func TestReadRunRejectsEventsFromReplacedTaskName(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Running", "", "")}, events: map[string]func(int64) string{"root": func(after int64) string { return page("root", 1, after, 1) }}}
	run, err := NewReader(&replacedDuringEvents{fixtureSource: f}).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Events) != 0 || run.Tasks[0].EventsMissing == nil || run.Tasks[0].EventsMissing.Reason != "identity changed" {
		t.Fatalf("name reuse: %+v", run)
	}
}

type replacedDuringEvents struct {
	fixtureSource
	calls int
}

func (f *replacedDuringEvents) Task(ctx context.Context, ns, name string) ([]byte, error) {
	f.calls++
	if f.calls >= 3 {
		return []byte(task(name, "new-uid", "lead", "Running", "", "")), nil
	}
	return f.fixtureSource.Task(ctx, ns, name)
}

func TestReadRunShrinksOversizeEventPages(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Succeeded", "", "")}}
	run, err := NewReader(oversizePageSource{fixtureSource: f}).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Events) != 1 || run.Tasks[0].EventsMissing != nil {
		t.Fatalf("oversize page recovery: %+v", run)
	}
}

type oversizePageSource struct{ fixtureSource }

func (s oversizePageSource) Events(_ context.Context, _, name string, after int64, limit int) ([]byte, int, error) {
	if limit > 1 {
		return nil, 0, ErrPageTooLarge
	}
	return []byte(page(name, 1, after, 1)), 200, nil
}

func TestReadRunAdvancesWhenEventsRequireSingleRecordPages(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "uid-root", "lead", "Succeeded", "", "")}}
	run, err := NewReader(singleEventPageSource{fixtureSource: f}).Read(context.Background(), "cluster", "team", "root", "uid-root")
	if err != nil {
		t.Fatal(err)
	}
	if run.Tasks[0].EventsMissing != nil || len(run.Events) != 3 || run.Events[2].Seq != 3 {
		t.Fatalf("single-record paging: %+v", run)
	}
}

type singleEventPageSource struct{ fixtureSource }

func (singleEventPageSource) Events(_ context.Context, _, name string, after int64, limit int) ([]byte, int, error) {
	if limit > 1 {
		return nil, 0, ErrPageTooLarge
	}
	return []byte(page(name, 3, after, after+1)), 200, nil
}

func TestReadRunRejectsReplacedRoot(t *testing.T) {
	f := fixtureSource{tasks: map[string]string{"root": task("root", "new-uid", "lead", "Succeeded", "", "")}}
	if _, err := NewReader(f).Read(context.Background(), "cluster", "team", "root", "old-uid"); err == nil {
		t.Fatal("reused root name accepted")
	}
}
