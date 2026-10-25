package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/kaimahi-agents/kaimahi/internal/kmx/app/kubectlfake"
)

func sessionsAgentFixture(t *testing.T) (*App, ReleaseLiftOptions, string) {
	t.Helper()
	opt, _, _ := sessionsLiftFixture(t)
	opt.HarnessImage, opt.Bindings = "", ""
	opt.ToContext, opt.ToNamespace, opt.ExpectedRuntime = "kind-test", "workloads", "agentsessions"
	dir := t.TempDir()
	t.Setenv("KMX_SESSIONS_TEST_DIR", dir)
	a := captureTestApp()
	a.Out, a.Err, a.Stdin = io.Discard, io.Discard, os.Stdin
	a.Cfg.KubeContext = "kind-test"
	a.Cfg.Confirm = "kind-test"
	a.waitTiming = &waitTiming{pollInterval: time.Millisecond, timeout: func(ctx context.Context, phase string, d time.Duration) (context.Context, context.CancelFunc) {
		if phase == "sessions-registration" {
			d = 30 * time.Millisecond
		}
		if d == 0 {
			return ctx, func() {}
		}
		return context.WithTimeout(ctx, d)
	}}
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		return protocol.AgentSessions(captureFixtureRequest(ctx, cmd))
	}
	return a, opt, dir
}

func TestSessionsAgentCreateAndNoop(t *testing.T) {
	a, opt, dir := sessionsAgentFixture(t)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Summary()
	if s.Runtime != "agentsessions" || len(s.Resources) != 1 || s.Resources[0].Action != "create" || s.Gate != "development-source" || s.SessionsAgent == nil {
		t.Fatalf("plan %+v", s)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.json")); !os.IsNotExist(err) {
		t.Fatal("plan mutated")
	}
	if _, err := a.ApplyReleaseLift(t.Context(), p, "wrong"); err == nil {
		t.Fatal("accepted wrong reviewed digest")
	}
	result, err := a.ApplyReleaseLift(t.Context(), p, s.PlanDigest)
	if err != nil || result.State != "registered" || result.Resources[0].Outcome != "created" || result.Resources[0].UID != "agent-uid" {
		t.Fatalf("result %+v: %v", result, err)
	}
	if _, err := a.ApplyReleaseLift(t.Context(), p, s.PlanDigest); err == nil {
		t.Fatal("reused consumed plan")
	}
	before, _ := os.ReadFile(filepath.Join(dir, "calls"))
	p, err = a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	result, err = a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if err != nil || result.State != "registered" || result.Resources[0].Outcome != "unchanged" {
		t.Fatalf("no-op %+v %v", result, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "calls"))
	delta := strings.TrimPrefix(string(after), string(before))
	if strings.Contains(delta, "create") || strings.Contains(delta, "config") {
		t.Fatalf("no-op not GET-only: %s", delta)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "agent.json"))
	var live map[string]any
	json.Unmarshal(raw, &live)
	live["spec"].(map[string]any)["member"] = "other"
	raw, _ = json.Marshal(live)
	os.WriteFile(filepath.Join(dir, "agent.json"), raw, 0600)
	_, err = a.PrepareReleaseLift(t.Context(), opt)
	if err == nil || !strings.Contains(err.Error(), s.SuiteDigest) || !strings.Contains(err.Error(), "--name") || strings.Contains(err.Error(), "\n") || !errors.Is(err, ErrReleaseLift) || !errors.Is(err, ErrReleaseLiftApply) {
		t.Fatalf("occupied refusal: %v", err)
	}
	opt.AgentName = "greeter-v2"
	p, err = a.PrepareReleaseLift(t.Context(), opt)
	if err != nil || p.Summary().Resources[0].Name != "greeter-v2" {
		t.Fatalf("new name: %v", err)
	}
}

func TestSessionsAgentPartialOutcomesNeverRetry(t *testing.T) {
	for _, scenario := range []string{"create-lost", "pending", "recreated", "mutated-create", "unknown-spec", "changed-binding", "unacknowledged"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, dir := sessionsAgentFixture(t)
			t.Setenv("KMX_SESSIONS_TEST_SCENARIO", scenario)
			p, err := a.PrepareReleaseLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
			if err == nil || result.State == "registered" {
				t.Fatalf("false success: %+v %v", result, err)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if strings.Count(string(calls), `"create"`) != 1 {
				t.Fatalf("mutations: %s", calls)
			}
			if scenario != "create-lost" && result.Resources[0].UID != "agent-uid" {
				t.Fatalf("lost acknowledged effect %+v", result)
			}
			if scenario == "pending" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost timeout: %v", err)
			}
		})
	}
}

func TestSessionsAgentRefusesSelectorsBeforeCluster(t *testing.T) {
	for _, change := range []func(*ReleaseLiftOptions){
		func(o *ReleaseLiftOptions) { o.Update = true }, func(o *ReleaseLiftOptions) { o.Inference = "provider:x" }, func(o *ReleaseLiftOptions) { o.Bindings = "binding.yaml" }, func(o *ReleaseLiftOptions) { o.AgentName = "INVALID" }, func(o *ReleaseLiftOptions) { o.ToNamespace = "" },
	} {
		a, opt, dir := sessionsAgentFixture(t)
		change(&opt)
		if _, err := a.PrepareReleaseLift(t.Context(), opt); err == nil {
			t.Fatal("accepted invalid selectors")
		}
		if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
			t.Fatal("invalid input touched cluster")
		}
	}
}

func TestSessionsAgentNoopRequiresAcknowledgedUID(t *testing.T) {
	a, opt, dir := sessionsAgentFixture(t)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "agent.json"))
	var doc map[string]any
	json.Unmarshal(raw, &doc)
	doc["status"].(map[string]any)["agentUID"] = "old"
	raw, _ = json.Marshal(doc)
	os.WriteFile(filepath.Join(dir, "agent.json"), raw, 0600)
	if _, err := a.PrepareReleaseLift(t.Context(), opt); err == nil {
		t.Fatal("adopted unacknowledged UID")
	}
}

func TestSessionsAgentConcurrentApplyIsSingleUse(t *testing.T) {
	a, opt, dir := sessionsAgentFixture(t)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); results <- err })
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if success != 1 || strings.Count(string(calls), `"create"`) != 1 {
		t.Fatalf("success=%d calls=%s", success, calls)
	}
}

func TestSessionsAgentConsentDenialPrecedesCreate(t *testing.T) {
	a, opt, dir := sessionsAgentFixture(t)
	a.Cfg.Confirm = "wrong-context"
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); err == nil {
		t.Fatal("unconfirmed remote mutation")
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Contains(string(calls), `"create"`) {
		t.Fatalf("create before consent: %s", calls)
	}
}

func TestSessionsAgentPendingNoopDoesNotWaitOrWrite(t *testing.T) {
	a, opt, dir := sessionsAgentFixture(t)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "agent.json"))
	var doc map[string]any
	json.Unmarshal(raw, &doc)
	delete(doc["status"].(map[string]any), "conditions")
	raw, _ = json.Marshal(doc)
	os.WriteFile(filepath.Join(dir, "agent.json"), raw, 0600)
	p, err = a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "calls"))
	result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	after, _ := os.ReadFile(filepath.Join(dir, "calls"))
	delta := strings.TrimPrefix(string(after), string(before))
	if !errors.Is(err, ErrDestinationStatusPending) || result.State != "pending" || result.Resources[0].Outcome != "unchanged" || strings.Count(delta, `"get"`) != 3 || strings.Contains(delta, `"create"`) {
		t.Fatalf("pending no-op %+v %v %s", result, err, delta)
	}
}

func TestSessionsAgentPlanApplyIdentityDrift(t *testing.T) {
	for _, field := range []string{"uid", "generation"} {
		t.Run(field, func(t *testing.T) {
			a, opt, dir := sessionsAgentFixture(t)
			p, err := a.PrepareReleaseLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); err != nil {
				t.Fatal(err)
			}
			p, err = a.PrepareReleaseLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(filepath.Join(dir, "agent.json"))
			var doc map[string]any
			json.Unmarshal(raw, &doc)
			if field == "uid" {
				doc["metadata"].(map[string]any)["uid"] = "replacement"
				doc["status"].(map[string]any)["agentUID"] = "replacement"
			} else {
				doc["metadata"].(map[string]any)["generation"] = 2
			}
			raw, _ = json.Marshal(doc)
			os.WriteFile(filepath.Join(dir, "agent.json"), raw, 0600)
			result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
			if err == nil || !strings.Contains(err.Error(), "changed since plan") || result.Resources[0].Outcome != "not-attempted" {
				t.Fatalf("drift outcome %+v %v", result, err)
			}
		})
	}
}

func TestSessionsAgentAppearsDuringConsent(t *testing.T) {
	a, opt, dir := sessionsAgentFixture(t)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	original := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		err := original(ctx, cmd)
		if err == nil && strings.Contains(strings.Join(cmd.Args, " "), "config view") {
			doc := p.Summary().SessionsAgent
			meta := doc["metadata"].(map[string]any)
			meta["uid"] = "other-writer"
			meta["generation"] = 1
			raw, _ := json.Marshal(doc)
			if e := os.WriteFile(filepath.Join(dir, "agent.json"), raw, 0600); e != nil {
				return e
			}
		}
		return err
	}
	result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if err == nil || !strings.Contains(err.Error(), "no longer absent") || result.State != "not-applied" || strings.Contains(string(calls), `"create"`) {
		t.Fatalf("adopted racing object %+v %v calls=%s", result, err, calls)
	}
}
