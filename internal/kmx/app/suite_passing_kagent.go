package app

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// A closed private branch: no Orka subject/resource or Kubernetes Task aliases.
// Concrete Orka fields stay available to the existing release-lift consumer.
type suiteKagentPassingProof struct {
	Observation    suiteKagentObservation `json:"observation"`
	SourceCommit   string                 `json:"sourceCommit"`
	RequiredImages []string               `json:"requiredImages"`
}
type suiteKagentCaseExecution struct {
	RPCID             string `json:"rpcID"`
	RequestMessageID  string `json:"requestMessageID"`
	TaskID            string `json:"taskID"`
	ContextID         string `json:"contextID"`
	State             string `json:"state"`
	UserTurns         int    `json:"userTurns"`
	AnswerAttribution string `json:"answerAttribution"`
}

const suiteKagentAnswerAttribution = "kmx.kagent.final-artifact-single-user-turn/v1"

// This is only the wire projection of the common receipt, not another envelope.
type suitePassingWire struct {
	SchemaVersion     int                        `json:"schemaVersion"`
	Domain            string                     `json:"domain"`
	ExecutionMode     string                     `json:"executionMode"`
	RunID             string                     `json:"runID"`
	CandidateCleanup  string                     `json:"candidateCleanup,omitempty"`
	SourceFingerprint string                     `json:"sourceFingerprint"`
	Subject           *suiteNativeSubject        `json:"subject,omitempty"`
	Provider          *suiteNativeOwnedResource  `json:"provider,omitempty"`
	Agent             *suiteNativeOwnedResource  `json:"agent,omitempty"`
	Kagent            *suiteKagentPassingProof   `json:"kagent,omitempty"`
	Sessions          *suiteSessionsPassingProof `json:"agentsessions,omitempty"`
	CasesDigest       string                     `json:"casesDigest"`
	Result            string                     `json:"result"`
	Cases             []suitePassingCase         `json:"cases"`
}

func (r suitePassingReceipt) MarshalJSON() ([]byte, error) {
	w := suitePassingWire{SchemaVersion: r.SchemaVersion, Domain: r.Domain, ExecutionMode: r.ExecutionMode, RunID: r.RunID, CandidateCleanup: r.CandidateCleanup, SourceFingerprint: r.SourceFingerprint, CasesDigest: r.CasesDigest, Result: r.Result, Cases: r.Cases, Kagent: r.Kagent, Sessions: r.Sessions}
	if r.ExecutionMode == "native-orka" {
		w.Subject, w.Provider, w.Agent = &r.Subject, &r.Provider, &r.Agent
	}
	return json.Marshal(w)
}
func (r *suitePassingReceipt) suiteDigest() string {
	switch r.ExecutionMode {
	case "native-kagent":
		if r.Kagent != nil {
			return r.Kagent.Observation.Subject.SuiteDigest
		}
	case "native-agentsessions":
		if r.Sessions != nil {
			return r.Sessions.Subject.SuiteDigest
		}
	case "native-orka":
		return r.Subject.SuiteDigest
	}
	return ""
}
func parseSuitePassingWire(raw []byte) (suitePassingReceipt, error) {
	var w suitePassingWire
	if suitecore.Unmarshal(raw, &w) != nil {
		return suitePassingReceipt{}, errSuitePassingReceipt
	}
	r := suitePassingReceipt{SchemaVersion: w.SchemaVersion, Domain: w.Domain, ExecutionMode: w.ExecutionMode, RunID: w.RunID, CandidateCleanup: w.CandidateCleanup, SourceFingerprint: w.SourceFingerprint, Kagent: w.Kagent, Sessions: w.Sessions, CasesDigest: w.CasesDigest, Result: w.Result, Cases: w.Cases}
	switch w.ExecutionMode {
	case "native-orka":
		if w.Subject == nil || w.Provider == nil || w.Agent == nil || w.Kagent != nil || w.Sessions != nil {
			return r, errSuitePassingReceipt
		}
		r.Subject, r.Provider, r.Agent = *w.Subject, *w.Provider, *w.Agent
	case "native-kagent":
		if w.Kagent == nil || w.Subject != nil || w.Provider != nil || w.Agent != nil || w.Sessions != nil {
			return r, errSuitePassingReceipt
		}
	case "native-agentsessions":
		if w.Sessions == nil || w.Kagent != nil || w.Subject != nil || w.Provider != nil || w.Agent != nil {
			return r, errSuitePassingReceipt
		}
	default:
		return r, errSuitePassingReceipt
	}
	return r, nil
}

func validSuiteKagentPassingProof(p *suiteKagentPassingProof) bool {
	if p == nil || p.RequiredImages == nil || len(p.RequiredImages) != 0 {
		return false
	}
	o := p.Observation
	s := o.Subject
	t := s.Target
	b := s.Binding
	r := s.Runtime
	secret := s.Secret
	profile := kagentLiftProfile()
	if p.SourceCommit != profile.Commit || s.Profile != (workspace.RuntimeProfile{ID: "kagent", Version: "v0.10.3", Engine: "go"}) || s.Renderer != "kmx.native-kagent/v2" || s.ExecutionProfile != "kmx.kagent-v0.10.3-go-linux-amd64-tool-free-openai/v1" {
		return false
	}
	for _, d := range []string{s.SuiteDigest, s.GraphDigest, s.MemberDigest, s.InstructionsDigest, s.CompositionDigest, s.BuildProfileDigest} {
		if !suitePrefixedDigest(d) {
			return false
		}
	}
	for _, d := range []string{s.BindingDigest, s.PayloadDigest, s.RenderedDigest} {
		if !verificationDigest(d) {
			return false
		}
	}
	for _, id := range []string{s.Suite, s.Member, t.Context, t.ClusterUID, t.NamespaceUID, r.ServiceUID, r.DeploymentUID, r.ConfigUID, secret.UID, secret.ResourceVersion, r.ControllerAccount} {
		if !verificationIdentity(id) {
			return false
		}
	}
	if scaffold.ValidateObjectName(s.Suite) != nil || scaffold.ValidateObjectName(s.Member) != nil || scaffold.ValidateNamespace(t.Namespace) != nil || b.Namespace != t.Namespace || agentruntime.ValidateKagentBindings(b) != nil || b.ModelConfig.Provider != "openai" || len(b.ModelConfig.BaseURL) > maxSuitePassingEndpointBytes || scaffold.ValidateSingleLineText(s.Model) != nil || strings.TrimSpace(s.Model) == "" || secret.Kind != "Secret" || secret.Name != b.ModelConfig.SecretRef.Name || secret.Name == s.Member || secret.Namespace != t.Namespace || secret.Key != b.ModelConfig.SecretRef.Key || secret.Generation != 0 || secret.SpecDigest != "" {
		return false
	}
	if r.AuthMode != "unsecure" || r.DeploymentGeneration < 1 || scaffold.ValidateNamespace(r.Namespace) != nil || scaffold.ValidateObjectName(r.ServiceName) != nil || r.ServiceName != r.DeploymentName || r.ServiceName != r.ConfigName || r.GoRegistry != "ghcr.io" || r.GoRepository != "kagent-dev/kagent/golang-adk" || !profile.runtimeTagMatches("go", r.GoTag) || validateKagentWatchNamespaces(r.WatchNamespaces, t.Namespace) != nil || !profile.imageMatches(r.ControllerImage, profile.ControllerImage, kagentControllerImageRepo, profile.ControllerDigests[:]) || len(r.Selector) == 0 {
		return false
	}
	for k, v := range r.Selector {
		if !verificationIdentity(k) || !verificationIdentity(v) {
			return false
		}
	}
	imagesValid := func(images []kagentEvaluationImage, pins [3]string) bool {
		if !suiteKagentAMD64(images) {
			return false
		}
		for _, i := range images {
			if !qualifiedKagentEvaluationImage(i, pins) {
				return false
			}
		}
		return true
	}
	if !imagesValid(r.ControllerImages, profile.ControllerDigests) || !imagesValid(o.Images, profile.GoDigests) || len(o.Resources) != 2 || len(o.Children) != 4 {
		return false
	}
	seen := map[string]bool{t.ClusterUID: true, t.NamespaceUID: true}
	check := func(p agentruntime.ResourceEvidence, kind, name, ns string, generation bool, spec bool) bool {
		if !kagentEvaluationResource(p) || p.Kind != kind || p.Name != name || p.Namespace != ns || seen[p.UID] || p.Key != "" || p.Generation < 0 || generation && p.Generation < 1 || !generation && p.Generation != 0 || spec && !verificationDigest(p.SpecDigest) || !spec && p.SpecDigest != "" {
			return false
		}
		seen[p.UID] = true
		return true
	}
	if seen[secret.UID] {
		return false
	}
	seen[secret.UID] = true
	for _, id := range []string{r.ServiceUID, r.DeploymentUID, r.ConfigUID} {
		if seen[id] {
			return false
		}
		seen[id] = true
	}
	for i, kind := range []string{"ModelConfig", "Agent"} {
		if !check(o.Resources[i], kind, s.Member, t.Namespace, true, true) {
			return false
		}
	}
	for i, kind := range []string{"Deployment", "Service", "ServiceAccount", "Secret"} {
		p := o.Children[i]
		if p.ResourceVersion == "" || !check(p, kind, s.Member, t.Namespace, kind == "Deployment", kind == "Deployment") {
			return false
		}
	}
	return true
}

func coherentSuiteKagentPassingProof(p *suiteKagentPassingProof, suite *workspace.NativeSuite) error {
	if !validSuiteKagentPassingProof(p) || suite == nil {
		return errSuitePassingReceipt
	}
	s := p.Observation.Subject
	if suite.Runtime != s.Profile || s.Suite != suite.Name || s.SuiteDigest != suite.Digest || s.GraphDigest != suite.GraphDigest || s.Member != suite.MemberID || s.MemberDigest != suite.MemberDigest || s.InstructionsDigest != suite.InstructionsDigest || s.CompositionDigest != suite.CompositionDigest || s.BuildProfileDigest != suite.BuildProfileDigest || s.Model != suite.Model || s.Renderer != suite.RendererVersion || s.ExecutionProfile != suite.ExecutionProfile || !reflect.DeepEqual(p.RequiredImages, suite.RequiredImages) {
		return errSuitePassingReceipt
	}
	binding, err := suiteNativeDigest("kmx.native-kagent.binding/v1", struct {
		Model   string
		Binding agentruntime.KagentBindings
		Secret  agentruntime.ResourceEvidence
		Runtime suiteKagentRuntime
	}{s.Model, s.Binding, s.Secret, s.Runtime})
	if err != nil || binding != s.BindingDigest {
		return errSuitePassingReceipt
	}
	b := s.Binding
	bundle, err := scaffold.GenerateSuiteKagent(scaffold.KagentSpec{Name: s.Member, Namespace: b.Namespace, Description: suite.Description, Runtime: "go", Instructions: string(suite.Instructions), ProviderType: b.ModelConfig.Provider, Model: s.Model, BaseURL: b.ModelConfig.BaseURL, SecretName: b.ModelConfig.SecretRef.Name, SecretKey: b.ModelConfig.SecretRef.Key})
	if err != nil {
		return errSuitePassingReceipt
	}
	var raw [][]byte
	for i, doc := range []map[string]any{bundle.ModelConfig, bundle.Agent} {
		body, err := json.Marshal(doc)
		if err != nil {
			return errSuitePassingReceipt
		}
		raw = append(raw, body)
		proof, err := commonKagentObjectProof(doc, b.Namespace)
		if err != nil {
			return errSuitePassingReceipt
		}
		digest, err := kagentSpecFingerprint(proof.Spec)
		if err != nil || digest != p.Observation.Resources[i].SpecDigest {
			return errSuitePassingReceipt
		}
	}
	payload, err := suiteNativeDigest("kmx.native-kagent.payload/v1", raw)
	if err != nil || payload != s.PayloadDigest {
		return errSuitePassingReceipt
	}
	expected := s.RenderedDigest
	s.RenderedDigest = ""
	revision, err := suiteNativeDigest("kmx.native-kagent.revision/v1", s)
	if err != nil || revision != expected {
		return errSuitePassingReceipt
	}
	return nil
}

func newSuiteKagentPassingReceipt(ctx context.Context, p *PreparedSuiteWorkspaceEvaluation, o *suiteKagentObservation, observations []suitePassingObservation) (*suitePassingReceipt, error) {
	if p != nil {
		if err := p.suite.RequireUnprofiled(); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if o == nil || len(observations) != len(p.inventory) {
		return nil, errSuitePassingReceipt
	}
	_, casesDigest, err := suitePassingInventory(p.suite)
	if err != nil {
		return nil, err
	}
	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	r := &suitePassingReceipt{SchemaVersion: 1, Domain: "kmx.native-suite.passing/v1", ExecutionMode: "native-kagent", RunID: id, SourceFingerprint: p.prepared.Fingerprint, Kagent: &suiteKagentPassingProof{Observation: *o, SourceCommit: kagentLiftProfile().Commit, RequiredImages: []string{}}, CasesDigest: casesDigest, Result: "pass", Cases: make([]suitePassingCase, 0, len(observations))}
	for i, observed := range observations {
		row := p.inventory[i].Metadata
		if observed.CaseID != row.ID || observed.CaseDigest != row.CaseDigest || observed.InputDigest != row.InputDigest || observed.Detail != "" || observed.Execution == nil || observed.Stop || (observed.Verdict != agentruntime.EvaluationPass && (row.Run == 0 || observed.Verdict != agentruntime.EvaluationFail)) {
			return nil, errSuitePassingReceipt
		}
		e := observed.Execution
		row.KagentExecution = &suiteKagentCaseExecution{e.RPCID, e.RequestMessageID, e.TaskID, e.ContextID, e.State, 1, suiteKagentAnswerAttribution}
		row.AnswerDigest = observed.AnswerSHA256
		row.Verdict = string(observed.Verdict)
		row.Assertions = make([]suitePassingAssertion, 0, len(observed.Assertions))
		for _, a := range observed.Assertions {
			row.Assertions = append(row.Assertions, suitePassingAssertionResult(a))
		}
		r.Cases = append(r.Cases, row)
	}
	if _, err := canonicalSuitePassingReceipt(ctx, r, p.suite); err != nil {
		return nil, err
	}
	return r, nil
}
