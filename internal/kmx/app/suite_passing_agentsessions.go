package app

import (
	"context"
	"reflect"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const suiteSessionsPassingDomain = "kmx.agentsessions-suite.passing/v1"

// These are explicit operator declarations, not authenticated host attestations.
// Access locators and payloads deliberately stay outside the exported subject.
type suiteSessionsHostObservation struct {
	Mode                string `json:"mode"`
	HostID              string `json:"hostID"`
	JournalID           string `json:"journalID"`
	RuntimeVersion      string `json:"runtimeVersion"`
	RuntimeBinaryDigest string `json:"runtimeBinaryDigest"`
	RuntimeConfigDigest string `json:"runtimeConfigDigest"`
	InferenceID         string `json:"inferenceID"`
	InferenceRevision   string `json:"inferenceRevision"`
	Model               string `json:"model"`
	Harness             string `json:"harness"`
	HarnessVersion      string `json:"harnessVersion"`
	Retention           string `json:"retention"`
}

type suiteSessionsSubject struct {
	Suite              string                       `json:"suite"`
	SuiteDigest        string                       `json:"suiteDigest"`
	GraphDigest        string                       `json:"graphDigest"`
	Member             string                       `json:"member"`
	MemberDigest       string                       `json:"memberDigest"`
	InstructionsDigest string                       `json:"instructionsDigest"`
	CompositionDigest  string                       `json:"compositionDigest"`
	BuildProfileDigest string                       `json:"buildProfileDigest"`
	Model              string                       `json:"model"`
	Renderer           string                       `json:"renderer"`
	ExecutionProfile   string                       `json:"executionProfile"`
	ConfigDigest       string                       `json:"configDigest"`
	Profile            workspace.RuntimeProfile     `json:"profile"`
	Host               suiteSessionsHostObservation `json:"host"`
}

type suiteSessionsPassingProof struct {
	Subject        suiteSessionsSubject `json:"subject"`
	RequiredImages []string             `json:"requiredImages"`
}

type suiteSessionsCaseExecution struct {
	SessionUID     string `json:"sessionUID"`
	ExecutionID    string `json:"executionID"`
	Harness        string `json:"harness"`
	HarnessVersion string `json:"harnessVersion"`
	HeadHash       string `json:"headHash"`
	HeadSeq        int64  `json:"headSeq"`
}

type suiteSessionsObservation struct {
	Result     agentsessions.CaseResult
	Assertions []agentruntime.EvaluationAssertionResult
}

func newSuiteSessionsSubject(suite *workspace.NativeSuite, host suiteSessionsHostObservation) (suiteSessionsSubject, error) {
	var s suiteSessionsSubject
	if suite == nil || suite.Runtime != (workspace.RuntimeProfile{ID: "agentsessions", Version: workspace.AgentSessionsVersion, Engine: "chat"}) || suite.RendererVersion != "kmx.native-agentsessions/v2" || suite.ExecutionProfile != "kmx.agentsessions-chat-system-prompt-tool-free-openai/v1" || suite.RequiredImages == nil || len(suite.RequiredImages) != 0 {
		return s, errSuitePassingReceipt
	}
	config, err := agentsessions.ConfigDigest(string(suite.Instructions))
	if err != nil {
		return s, errSuitePassingReceipt
	}
	s = suiteSessionsSubject{Suite: suite.Name, SuiteDigest: suite.Digest, GraphDigest: suite.GraphDigest, Member: suite.MemberID, MemberDigest: suite.MemberDigest, InstructionsDigest: suite.InstructionsDigest, CompositionDigest: suite.CompositionDigest, BuildProfileDigest: suite.BuildProfileDigest, Model: suite.Model, Renderer: suite.RendererVersion, ExecutionProfile: suite.ExecutionProfile, ConfigDigest: config, Profile: suite.Runtime, Host: host}
	if !validSuiteSessionsPassingProof(&suiteSessionsPassingProof{Subject: s, RequiredImages: []string{}}) {
		return suiteSessionsSubject{}, errSuitePassingReceipt
	}
	return s, nil
}

func validSuiteSessionsPassingProof(p *suiteSessionsPassingProof) bool {
	if p == nil || p.RequiredImages == nil || len(p.RequiredImages) != 0 {
		return false
	}
	s, h := p.Subject, p.Subject.Host
	if s.Profile != (workspace.RuntimeProfile{ID: "agentsessions", Version: workspace.AgentSessionsVersion, Engine: "chat"}) || s.Renderer != "kmx.native-agentsessions/v2" || s.ExecutionProfile != "kmx.agentsessions-chat-system-prompt-tool-free-openai/v1" || h.Mode != "operator-observed-development" || h.RuntimeVersion != workspace.AgentSessionsVersion || h.Harness != "chat" || h.HarnessVersion != "1" || h.Retention != "operator-managed" || h.Model != s.Model {
		return false
	}
	for _, d := range []string{s.SuiteDigest, s.GraphDigest, s.MemberDigest, s.InstructionsDigest, s.CompositionDigest, s.BuildProfileDigest, s.ConfigDigest, h.RuntimeBinaryDigest, h.RuntimeConfigDigest, h.InferenceRevision} {
		if !suitePrefixedDigest(d) {
			return false
		}
	}
	for _, id := range []string{s.Suite, s.Member, h.HostID, h.JournalID, h.InferenceID} {
		if !verificationIdentity(id) || scaffold.RefuseKeyShapes(id) != nil {
			return false
		}
	}
	return scaffold.ValidateObjectName(s.Suite) == nil && scaffold.ValidateObjectName(s.Member) == nil && verificationIdentity(s.Model) && scaffold.RefuseKeyShapes(s.Model) == nil
}

func coherentSuiteSessionsPassingProof(p *suiteSessionsPassingProof, suite *workspace.NativeSuite) error {
	if !validSuiteSessionsPassingProof(p) {
		return errSuitePassingReceipt
	}
	expected, err := newSuiteSessionsSubject(suite, p.Subject.Host)
	if err != nil || !reflect.DeepEqual(expected, p.Subject) {
		return errSuitePassingReceipt
	}
	return nil
}

func validSuiteSessionsExecution(e *suiteSessionsCaseExecution) bool {
	return e != nil && verificationIdentity(e.SessionUID) && verificationIdentity(e.ExecutionID) && e.Harness == "chat" && e.HarnessVersion == "1" && e.HeadSeq == 5 && verificationDigest(e.HeadHash)
}

// Only fresh successful candidate results can enter this factory. Replay and
// recorded reference envelopes confer no construction authority here.
func newSuiteSessionsPassingReceipt(ctx context.Context, p *PreparedSuiteWorkspaceEvaluation, subject suiteSessionsSubject, observations []suiteSessionsObservation) (*suitePassingReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p != nil {
		if err := p.suite.RequireUnprofiled(); err != nil {
			return nil, err
		}
	}
	if p == nil || p.prepared == nil || p.suite == nil {
		return nil, errSuitePassingReceipt
	}
	proof := &suiteSessionsPassingProof{Subject: subject, RequiredImages: []string{}}
	if err := coherentSuiteSessionsPassingProof(proof, p.suite); err != nil {
		return nil, err
	}
	inventory, casesDigest, err := suitePassingInventory(p.suite)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(inventory, p.inventory) || len(observations) != len(inventory) {
		return nil, errSuitePassingReceipt
	}
	r := &suitePassingReceipt{SchemaVersion: 1, Domain: suiteSessionsPassingDomain, ExecutionMode: "native-agentsessions", SourceFingerprint: p.prepared.Fingerprint, Sessions: proof, CasesDigest: casesDigest, Result: "pass", Cases: make([]suitePassingCase, 0, len(inventory))}
	for i, o := range observations {
		result := o.Result
		if result.ExecutionFailed || result.ModelMixed || result.Model != subject.Model || result.ConfigDigest != subject.ConfigDigest || result.Harness != subject.Host.Harness || result.HarnessVersion != subject.Host.HarnessVersion || result.Output == "" || len(result.ToolCalls) != 0 {
			return nil, errSuitePassingReceipt
		}
		row := inventory[i].Metadata
		row.SessionsExecution = &suiteSessionsCaseExecution{SessionUID: result.SessionUID, ExecutionID: result.ExecutionID, Harness: result.Harness, HarnessVersion: result.HarnessVersion, HeadHash: result.Head.Hash, HeadSeq: result.Head.Seq}
		row.AnswerDigest = agentruntime.EvaluationAnswerDigest(result.Output)
		row.Verdict = string(suiteRunVerdict(o.Assertions))
		row.Assertions = make([]suitePassingAssertion, 0, len(o.Assertions))
		for _, a := range o.Assertions {
			row.Assertions = append(row.Assertions, suitePassingAssertionResult(a))
		}
		r.Cases = append(r.Cases, row)
	}
	r.RunID, err = randomHex(16)
	if err != nil {
		return nil, err
	}
	if _, err := canonicalSuitePassingInventory(ctx, r, p.suite, inventory, casesDigest); err != nil {
		return nil, err
	}
	return r, nil
}
