package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

type suiteFake struct {
	prepared, inspected, deployed []string
	badMember                     string
	failDeploy                    string
	stamp                         string
}

func (*suiteFake) ID() ID                  { return "test-runtime" }
func (*suiteFake) SupportsReconcile() bool { return true }
func (f *suiteFake) Prepare(_ context.Context, m agentsuite.DeploymentMember, b SuiteMemberBinding, _ SuiteDeployRequest) (SuiteMemberPlan, error) {
	f.prepared = append(f.prepared, m.Agent.ID)
	if f.badMember == m.Agent.ID {
		return SuiteMemberPlan{}, errors.New("unsupported member")
	}
	return SuiteMemberPlan{Agent: m.Agent.ID, Name: b.Name, Digest: m.CompositionDigest, Resources: []string{"Agent/" + b.Name}}, nil
}
func (f *suiteFake) Inspect(_ context.Context, p SuiteMemberPlan) (string, error) {
	f.inspected = append(f.inspected, p.Agent)
	return "observed-" + f.stamp, nil
}
func (f *suiteFake) Deploy(_ context.Context, p SuiteMemberPlan) (SuiteMemberOutcome, error) {
	f.deployed = append(f.deployed, p.Agent)
	if p.Agent == f.failDeploy {
		return SuiteMemberOutcome{}, errors.New("write interrupted")
	}
	return SuiteMemberOutcome{Agent: p.Agent, State: "ready"}, nil
}

func suiteRequest(t *testing.T) SuiteDeployRequest {
	t.Helper()
	suite, err := agentsuite.ResolveDeploymentSuite(filepath.Join("..", "agentsuite", "testdata", "coordinator-workers"), agentsuite.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]SuiteMemberBinding{}
	for _, member := range suite.Members() {
		bindings[member.Agent.ID] = SuiteMemberBinding{Name: member.Agent.ID}
	}
	return SuiteDeployRequest{Suite: suite, Instance: "team", Target: SuiteTarget{Context: "kind-dev", ClusterUID: "uid", Namespace: "agents", Runtime: "test-runtime"}, Bindings: bindings, Reconcile: true}
}

func TestSuitePreparesEveryMemberBeforeInspectionAndWrites(t *testing.T) {
	r := suiteRequest(t)
	f := &suiteFake{badMember: "writer"}
	if _, err := PrepareSuiteDeployment(t.Context(), r, f); err == nil {
		t.Fatal("unsupported final member accepted")
	}
	if len(f.deployed) != 0 || len(f.inspected) != 0 {
		t.Fatal("partial suite performed target work")
	}
	delete(r.Bindings, "writer")
	if _, err := PrepareSuiteDeployment(t.Context(), r, f); err == nil {
		t.Fatal("missing member accepted")
	}
}

func TestSuiteDeploymentRecordsUnattemptedMembersAfterFailure(t *testing.T) {
	r := suiteRequest(t)
	f := &suiteFake{failDeploy: "reviewer"}
	p, err := PrepareSuiteDeployment(t.Context(), r, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Inspect(t.Context()); err != nil {
		t.Fatal(err)
	}
	receipt, err := p.Deploy(t.Context(), func(SuitePlanSummary) error { return nil })
	if err == nil || receipt.State != "partial" || len(f.deployed) != 2 || receipt.Members[2].State != "not-attempted" {
		t.Fatalf("receipt=%+v error=%v", receipt, err)
	}
}

func TestSuiteRechecksWholePlanAfterReview(t *testing.T) {
	f := &suiteFake{}
	p, err := PrepareSuiteDeployment(t.Context(), suiteRequest(t), f)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Inspect(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, err = p.Deploy(t.Context(), func(SuitePlanSummary) error { f.stamp = "changed"; return nil })
	if err == nil || len(f.deployed) != 0 {
		t.Fatal("stale reviewed plan deployed")
	}
}

func TestSuiteSnapshotCopiesRetainAllAgentsAndEdges(t *testing.T) {
	r := suiteRequest(t)
	members := r.Suite.Members()
	if len(members) != 3 || len(members[0].Agent.Invokes) != 2 {
		t.Fatal("lost suite members or edges")
	}
	members[0].Agent.Invokes = nil
	if len(r.Suite.Members()[0].Agent.Invokes) != 2 {
		t.Fatal("snapshot mutation escaped")
	}
}
