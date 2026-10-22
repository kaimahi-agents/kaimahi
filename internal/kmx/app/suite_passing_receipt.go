package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"strings"
	"unicode/utf8"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const maxSuitePassingReceiptBytes = 1 << 20
const maxSuitePassingEndpointBytes = 16 << 10
const suitePassingEvaluatorVersion = "kmx.text-assertions/v1"
const suitePassingJudgeVersion = "kmx.llm-judge/v2"

var errSuitePassingReceipt = errors.New("invalid native passing receipt")

// errSuiteSessionsReceiptV1 classifies an AgentSessions passing receipt v1,
// which does not record the AgentSessionsBinding digest. It is never eligible
// proof; it only gets a clearer error.
var errSuiteSessionsReceiptV1 = fmt.Errorf("%w: AgentSessions passing receipt v1 does not record an AgentSessionsBinding; evaluate again with --development-host FILE --bindings FILE", errSuitePassingReceipt)

// errPreviousSuiteReceipt marks receipts for the old case layout.
var errPreviousSuiteReceipt = fmt.Errorf("%w: recorded against cases under metadata/kmx/eval by an earlier kmx; rerun kmx eval and kmx push with this kmx", errSuitePassingReceipt)

type suitePassingReceipt struct {
	SchemaVersion     int                        `json:"schemaVersion"`
	Domain            string                     `json:"domain"`
	ExecutionMode     string                     `json:"executionMode"`
	RunID             string                     `json:"runID"`
	CandidateCleanup  string                     `json:"candidateCleanup,omitempty"`
	SourceFingerprint string                     `json:"sourceFingerprint"`
	Subject           suiteNativeSubject         `json:"subject"`
	Provider          suiteNativeOwnedResource   `json:"provider"`
	Agent             suiteNativeOwnedResource   `json:"agent"`
	CasesDigest       string                     `json:"casesDigest"`
	Result            string                     `json:"result"`
	Cases             []suitePassingCase         `json:"cases"`
	Kagent            *suiteKagentPassingProof   `json:"kagent,omitempty"`
	Sessions          *suiteSessionsPassingProof `json:"agentsessions,omitempty"`
}
type suitePassingCase struct {
	Runs              int                         `json:"runs,omitempty"`
	MinPasses         int                         `json:"minPasses,omitempty"`
	Run               int                         `json:"run,omitempty"`
	ID                string                      `json:"id"`
	Path              string                      `json:"path"`
	CaseDigest        string                      `json:"caseDigest"`
	InputDigest       string                      `json:"inputDigest"`
	TaskName          string                      `json:"taskName,omitempty"`
	TaskUID           string                      `json:"taskUID,omitempty"`
	AnswerDigest      string                      `json:"answerDigest"`
	Verdict           string                      `json:"verdict"`
	Assertions        []suitePassingAssertion     `json:"assertions"`
	KagentExecution   *suiteKagentCaseExecution   `json:"kagentExecution,omitempty"`
	SessionsExecution *suiteSessionsCaseExecution `json:"agentsessionsExecution,omitempty"`
}
type suitePassingAssertion struct {
	ID               string                                `json:"id"`
	Type             string                                `json:"type"`
	DefinitionDigest string                                `json:"definitionDigest"`
	EvaluatorVersion string                                `json:"evaluatorVersion"`
	Verdict          string                                `json:"verdict"`
	Judge            *agentruntime.EvaluationJudgeEvidence `json:"judge,omitempty"`
}
type suitePassingObservation struct {
	failure                 *suiteFailureDetail
	CaseDigest, InputDigest string
	agentruntime.EvaluationReceipt
}

type suitePassingInventoryCase struct {
	Case     agentruntime.EvaluationCase
	Metadata suitePassingCase
}

// One ordered parse feeds receipt validation and later native execution. The
// projection budget reserves maximum accepted Task identities before selection.
func suitePassingInventory(suite *workspace.NativeSuite) ([]suitePassingInventoryCase, string, error) {
	if suite != nil {
		if err := suite.RequirePolicyFree(); err != nil {
			return nil, "", err
		}
	}
	if suite == nil || len(suite.Cases) == 0 {
		return nil, "", errSuitePassingReceipt
	}
	inventory := make([]suitePassingInventoryCase, 0, len(suite.Cases))
	files := make([]agentruntime.EvaluationCaseFile, 0, len(suite.Cases))
	// Identity fields accept at most 256 printable ASCII bytes. Reserve their
	// escaped representation plus the bounded endpoint; model size is known.
	modelJSON, err := suitecore.Marshal(suite.Model)
	if err != nil {
		return nil, "", errSuitePassingReceipt
	}
	budget := 32768 + 6*maxSuitePassingEndpointBytes + len(modelJSON)
	if suite.Runtime.ID == "kagent" {
		budget += 32768
	}
	seen := map[string]bool{}
	for _, file := range suite.Cases {
		c, err := workspace.DecodePackagedCase(file.Bytes)
		if err != nil {
			return nil, "", errors.New("invalid native evaluation case")
		}
		if seen[c.ID] || !suiteCasePath(file.Path) || file.Path != "evals/"+suite.MemberID+"/cases/"+c.ID+".json" || file.Digest != "sha256:"+agentruntime.EvaluationCaseDigest(file.Bytes) {
			return nil, "", errSuitePassingReceipt
		}
		seen[c.ID] = true
		runs, minPasses := c.RunPolicy()
		metadata := suitePassingCase{ID: c.ID, Path: file.Path, CaseDigest: agentruntime.EvaluationCaseDigest(file.Bytes), InputDigest: agentruntime.EvaluationInputDigest(c.Input), Verdict: "pass", Assertions: []suitePassingAssertion{}}
		for _, a := range agentruntime.EvaluationCaseAssertions(c) {
			if a.Type != "contains" && a.Type != "notContains" && a.Type != "regex" && (a.Type != "llmJudge" || (suite.Runtime.ID != "orka" && suite.Runtime.ID != "kagent" && suite.Runtime.ID != "agentsessions")) {
				return nil, "", errors.New("unsupported native suite evaluator")
			}
			metadata.Assertions = append(metadata.Assertions, suitePassingAssertionDefinition(a))
		}
		if runs > 1 {
			metadata.Runs, metadata.MinPasses, metadata.Run = runs, minPasses, runs
		}
		projected := metadata
		projected.TaskName, projected.TaskUID, projected.AnswerDigest = strings.Repeat("x", 256), strings.Repeat("\\", 256), strings.Repeat("0", 64)
		if suite.Runtime.ID == "kagent" {
			projected.TaskName, projected.TaskUID = "", ""
			projected.KagentExecution = &suiteKagentCaseExecution{strings.Repeat("x", 36), strings.Repeat("x", 40), strings.Repeat("x", 256), strings.Repeat("x", 256), "completed", 1, suiteKagentAnswerAttribution}
		}
		if suite.Runtime.ID == "agentsessions" {
			projected.TaskName, projected.TaskUID = "", ""
			projected.SessionsExecution = &suiteSessionsCaseExecution{SessionUID: strings.Repeat("x", 256), ExecutionID: strings.Repeat("x", 256), Harness: "chat", HarnessVersion: "1", HeadHash: strings.Repeat("0", 64), HeadSeq: 5}
		}
		wire, err := suitecore.Marshal(projected)
		if err != nil {
			return nil, "", errSuitePassingReceipt
		}
		perRun := len(wire) + 1
		for _, a := range c.Assertions {
			if a.Type == "llmJudge" {
				// Escaped 256-byte model/version identities, fixed digests and two scores.
				perRun += 4096
			}
		}
		if perRun > (maxSuitePassingReceiptBytes-budget)/runs {
			return nil, "", errors.New("native suite receipt exceeds projection budget")
		}
		budget += runs * perRun
		for run := 1; run <= runs; run++ {
			if runs > 1 {
				metadata.Run = run
			}
			inventory = append(inventory, suitePassingInventoryCase{c, metadata})
		}
		// Member-relative names keep cases distinct across members.
		files = append(files, agentruntime.EvaluationCaseFile{Name: strings.TrimPrefix(file.Path, "evals/"), Bytes: file.Bytes})
	}
	return inventory, agentruntime.EvaluationCasesDigest(files), nil
}

// The caller must bracket actual execution with verifySuiteNative. Unsigned
// observations cannot prove drift absence, admission, or authentication.
func newSuitePassingReceipt(ctx context.Context, prepared *workspace.Prepared, placement *suiteNativePlacement, observations []suitePassingObservation) (*suitePassingReceipt, error) {
	if placement != nil && (placement.Subject.PhysicalMember != "" || placement.Subject.EvaluationRun != "") {
		return nil, errSuitePassingReceipt
	}
	return newSuitePassingReceiptRun(ctx, prepared, placement, observations, "")
}

// The caller invokes this only after proving candidate cleanup complete. This
// constructor binds that declaration to the run allocated before native effects.
func newSuitePassingCandidateReceipt(ctx context.Context, prepared *workspace.Prepared, placement *suiteNativePlacement, observations []suitePassingObservation, runID string) (*suitePassingReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if placement == nil || placement.Subject.FlowCandidate != nil || !evaluationRunID(runID) || placement.Subject.EvaluationRun != runID || !placement.Subject.validPhysicalMember() {
		return nil, errSuitePassingReceipt
	}
	return newSuitePassingReceiptRun(ctx, prepared, placement, observations, runID)
}

func newSuitePassingReceiptRun(ctx context.Context, prepared *workspace.Prepared, placement *suiteNativePlacement, observations []suitePassingObservation, runID string) (*suitePassingReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if prepared == nil || placement == nil {
		return nil, errSuitePassingReceipt
	}
	suite, err := workspace.Load(ctx, prepared)
	if err != nil {
		return nil, err
	}
	if !suiteNativeOrkaInput(suite) {
		return nil, errSuitePassingReceipt
	}
	inventory, casesDigest, err := suitePassingInventory(suite)
	if err != nil {
		return nil, err
	}
	if len(observations) != len(inventory) {
		return nil, errSuitePassingReceipt
	}
	if runID == "" {
		runID, err = randomHex(16)
		if err != nil {
			return nil, err
		}
	}
	r := &suitePassingReceipt{SchemaVersion: 1, Domain: "kmx.native-suite.passing/v1", ExecutionMode: "native-orka", RunID: runID, SourceFingerprint: prepared.Fingerprint, Subject: placement.Subject, Provider: placement.Provider, Agent: placement.Agent, CasesDigest: casesDigest, Result: "pass", Cases: make([]suitePassingCase, 0, len(inventory))}
	if placement.Subject.EvaluationRun != "" {
		r.CandidateCleanup = "complete"
	}
	for i, observed := range observations {
		row := inventory[i].Metadata
		row.TaskName, row.TaskUID, row.AnswerDigest = observed.TaskName, observed.TaskUID, observed.AnswerSHA256
		row.Verdict = string(observed.Verdict)
		row.Assertions = make([]suitePassingAssertion, 0, len(observed.Assertions))
		for _, a := range observed.Assertions {
			row.Assertions = append(row.Assertions, suitePassingAssertionResult(a))
		}
		if observed.CaseID != row.ID || observed.CaseDigest != row.CaseDigest || observed.InputDigest != row.InputDigest || observed.Detail != "" {
			return nil, errSuitePassingReceipt
		}
		r.Cases = append(r.Cases, row)
	}
	if _, err := canonicalSuitePassingInventory(ctx, r, suite, inventory, casesDigest); err != nil {
		return nil, err
	}
	return r, nil
}

func parseSuitePassingReceipt(raw []byte) (*suitePassingReceipt, error) {
	r, sessionsV1, err := classifySuitePassingReceipt(raw)
	if sessionsV1 {
		return nil, errSuiteSessionsReceiptV1
	}
	return r, err
}

// classifySuitePassingReceipt parses raw strictly. When raw is an otherwise
// complete AgentSessions passing receipt v1, it reports sessionsV1 with the
// receipt parsed as v2 under a placeholder binding digest, so a caller can
// check its identity before classifying the refusal. That receipt is never
// eligible proof.
func classifySuitePassingReceipt(raw []byte) (r *suitePassingReceipt, sessionsV1 bool, err error) {
	r, err = parseSuitePassingReceiptStrict(raw)
	if err == nil {
		return r, false, nil
	}
	if old := suiteSessionsReceiptV1(raw); old != nil {
		return old, true, err
	}
	return nil, false, err
}

// suiteSessionsReceiptV1 returns raw parsed as v2 with a placeholder
// agentsessions.subject.bindingDigest when raw is an AgentSessions passing
// receipt v1 that would otherwise parse, and nil otherwise. It accepts
// nothing: the placeholder only classifies the refusal.
func suiteSessionsReceiptV1(raw []byte) *suitePassingReceipt {
	if len(raw) > maxSuitePassingReceiptBytes {
		return nil
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&document) != nil || document["executionMode"] != "native-agentsessions" || document["domain"] != suiteSessionsPassingDomainV1 {
		return nil
	}
	sessions, _ := document["agentsessions"].(map[string]any)
	subject, _ := sessions["subject"].(map[string]any)
	if subject == nil {
		return nil
	}
	document["domain"] = suiteSessionsPassingDomain
	if _, ok := subject["bindingDigest"]; !ok {
		subject["bindingDigest"] = "sha256:" + strings.Repeat("0", 64)
	}
	placeholder, err := suitecore.Marshal(document)
	if err != nil {
		return nil
	}
	r, err := parseSuitePassingReceiptStrict(placeholder)
	if err != nil {
		return nil
	}
	return r
}

func parseSuitePassingReceiptStrict(raw []byte) (*suitePassingReceipt, error) {
	r, parseErr := parseSuitePassingWire(raw)
	if len(raw) > maxSuitePassingReceiptBytes || parseErr != nil {
		return nil, errSuitePassingReceipt
	}
	// Canonical raw versus the closed typed projection detects absent fields and
	// scalar null without maintaining a second schema. Arrays are checked below.
	var document any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&document) != nil {
		return nil, errSuitePassingReceipt
	}
	original, err := suitecore.Marshal(document)
	if err != nil {
		return nil, errSuitePassingReceipt
	}
	typed, err := suitecore.Marshal(r)
	if err != nil || !bytes.Equal(original, typed) {
		return nil, errSuitePassingReceipt
	}
	if err := basicSuitePassingReceipt(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

func basicSuitePassingReceipt(r *suitePassingReceipt) error {
	if r == nil || r.SchemaVersion != 1 || !evaluationRunID(r.RunID) || r.Result != "pass" || r.Cases == nil {
		return errSuitePassingReceipt
	}
	for _, c := range r.Cases {
		if strings.HasPrefix(c.Path, "metadata/kmx/eval/") {
			return errPreviousSuiteReceipt
		}
	}
	switch r.ExecutionMode {
	case "native-orka", "native-kagent":
		if r.Domain != "kmx.native-suite.passing/v1" {
			return errSuitePassingReceipt
		}
	case "native-agentsessions":
		if r.Domain != suiteSessionsPassingDomain {
			return errSuitePassingReceipt
		}
	default:
		return errSuitePassingReceipt
	}
	if r.ExecutionMode == "native-orka" {
		if r.Subject.EvaluationRun != "" {
			if r.Subject.EvaluationRun != r.RunID || r.CandidateCleanup != "complete" {
				return errSuitePassingReceipt
			}
		} else if r.CandidateCleanup != "" {
			return errSuitePassingReceipt
		}
		if r.Kagent != nil || r.Sessions != nil || !validSuitePassingSubject(r.Subject) || r.Provider.UID == r.Agent.UID {
			return errSuitePassingReceipt
		}
	} else {
		if r.CandidateCleanup != "" || !reflect.DeepEqual(r.Subject, suiteNativeSubject{}) || r.Provider != (suiteNativeOwnedResource{}) || r.Agent != (suiteNativeOwnedResource{}) {
			return errSuitePassingReceipt
		}
		switch r.ExecutionMode {
		case "native-kagent":
			if r.Sessions != nil || !validSuiteKagentPassingProof(r.Kagent) {
				return errSuitePassingReceipt
			}
		case "native-agentsessions":
			if r.Kagent != nil || !validSuiteSessionsPassingProof(r.Sessions) {
				return errSuitePassingReceipt
			}
		}
	}
	if !suitePrefixedDigest(r.SourceFingerprint) || !verificationDigest(r.CasesDigest) || len(r.Cases) == 0 {
		return errSuitePassingReceipt
	}
	ownedResources := []suiteNativeOwnedResource{}
	if r.ExecutionMode == "native-orka" {
		ownedResources = []suiteNativeOwnedResource{r.Provider, r.Agent}
	}
	for _, owned := range ownedResources {
		if !verificationIdentity(owned.UID) || owned.Generation < 1 || !verificationDigest(owned.SpecDigest) {
			return errSuitePassingReceipt
		}
	}
	if err := validateSuiteRunGroups(r.Cases); err != nil {
		return err
	}
	tasks, uids := map[string]bool{}, map[string]bool{}
	for _, c := range r.Cases {
		if !evaluationEvidenceID.MatchString(c.ID) || !verificationIdentity(c.ID) || !suiteCasePath(c.Path) || !verificationDigest(c.CaseDigest) || !verificationDigest(c.InputDigest) || !verificationDigest(c.AnswerDigest) || c.AnswerDigest == agentruntime.EvaluationAnswerDigest("") || (c.Verdict != "pass" && (c.Run == 0 || c.Verdict != "fail")) || len(c.Assertions) == 0 {
			return errSuitePassingReceipt
		}
		if r.ExecutionMode == "native-orka" {
			if c.KagentExecution != nil || c.SessionsExecution != nil || scaffold.ValidateObjectName(c.TaskName) != nil || !verificationIdentity(c.TaskName) || !verificationIdentity(c.TaskUID) || tasks[c.TaskName] || uids[c.TaskUID] {
				return errSuitePassingReceipt
			}
			tasks[c.TaskName], uids[c.TaskUID] = true, true
		} else if r.ExecutionMode == "native-kagent" {
			e := c.KagentExecution
			if c.SessionsExecution != nil || c.TaskName != "" || c.TaskUID != "" || e == nil || !suiteKagentRequestID(e.RPCID, "rpc-") || !suiteKagentRequestID(e.RequestMessageID, "message-") || !safeKagentEvaluationID(e.TaskID) || !safeKagentEvaluationID(e.ContextID) || e.State != "completed" || e.UserTurns != 1 || e.AnswerAttribution != suiteKagentAnswerAttribution || tasks[e.TaskID] || uids[e.ContextID] || tasks[e.RPCID] || uids[e.RequestMessageID] {
				return errSuitePassingReceipt
			}
			tasks[e.TaskID], uids[e.ContextID], tasks[e.RPCID], uids[e.RequestMessageID] = true, true, true, true
		} else {
			e := c.SessionsExecution
			if c.KagentExecution != nil || c.TaskName != "" || c.TaskUID != "" || !validSuiteSessionsExecution(e) || tasks[e.ExecutionID] || uids[e.SessionUID] {
				return errSuitePassingReceipt
			}
			tasks[e.ExecutionID], uids[e.SessionUID] = true, true
		}
		assertions := map[string]bool{}
		verdict := "pass"
		for _, a := range c.Assertions {
			if !evaluationEvidenceID.MatchString(a.ID) || !verificationIdentity(a.ID) || assertions[a.ID] || (a.Type != "contains" && a.Type != "notContains" && a.Type != "regex" && a.Type != "llmJudge") || !verificationDigest(a.DefinitionDigest) || a.EvaluatorVersion != suiteAssertionEvaluatorVersion(a.Type) || (a.Verdict != "pass" && (c.Run == 0 || a.Verdict != "fail")) || !agentruntime.EvaluationJudgeEvidenceValid(agentruntime.EvaluationAssertionResult{Type: a.Type, Verdict: agentruntime.EvaluationVerdict(a.Verdict), Judge: a.Judge}) {
				return errSuitePassingReceipt
			}
			assertions[a.ID] = true
			if a.Verdict == "fail" {
				verdict = "fail"
			}
		}
		if c.Verdict != verdict {
			return errSuitePassingReceipt
		}
	}
	masked := *r
	masked.RunID = "run"
	if masked.Subject.EvaluationRun != "" {
		// basic validation above proved the exact projection and receipt run.
		masked.Subject.EvaluationRun = "run"
	}
	raw, err := suitecore.Marshal(masked)
	if err != nil || scaffold.RefuseKeyShapes(string(raw)) != nil {
		return errors.New("refusing credential-shaped native receipt metadata")
	}
	return nil
}

func canonicalSuitePassingReceipt(ctx context.Context, r *suitePassingReceipt, suite *workspace.NativeSuite) ([]byte, error) {
	view, err := suiteReceiptDeclarationView(suite)
	if err != nil {
		return nil, err
	}
	suite = view
	inventory, casesDigest, err := suitePassingInventory(suite)
	if err != nil {
		return nil, err
	}
	return canonicalSuitePassingInventory(ctx, r, suite, inventory, casesDigest)
}

func canonicalSuitePassingInventory(ctx context.Context, r *suitePassingReceipt, suite *workspace.NativeSuite, inventory []suitePassingInventoryCase, casesDigest string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := basicSuitePassingReceipt(r); err != nil {
		return nil, err
	}
	if r.ExecutionMode == "native-kagent" {
		if err := coherentSuiteKagentPassingProof(r.Kagent, suite); err != nil {
			return nil, err
		}
	} else if r.ExecutionMode == "native-agentsessions" {
		if err := coherentSuiteSessionsPassingProof(r.Sessions, suite); err != nil {
			return nil, err
		}
	} else if err := coherentSuitePassingSubject(r.Subject, suite); err != nil {
		return nil, err
	}
	if r.CasesDigest != casesDigest || len(r.Cases) != len(inventory) {
		return nil, errSuitePassingReceipt
	}
	for i, expected := range inventory {
		row := suitePassingDefinition(r.Cases[i], true)
		if !reflect.DeepEqual(row, expected.Metadata) {
			return nil, errSuitePassingReceipt
		}
	}
	raw, err := suitecore.Marshal(r)
	if err != nil || len(raw) > maxSuitePassingReceiptBytes {
		return nil, errSuitePassingReceipt
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return raw, nil
}

func validSuitePassingSubject(s suiteNativeSubject) bool {
	if s.FlowCandidate != nil || !s.validPhysicalMember() {
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
	t, p, secret, r := s.Target, s.Binding.Provider, s.Binding.Secret, s.Runtime
	for _, identity := range []string{s.Suite, s.Member, s.Renderer, t.Context, t.ClusterUID, t.NamespaceUID, p.Name, p.Namespace, p.UID, p.ResourceVersion, p.Type, p.SecretName, p.SecretKey, secret.Name, secret.Namespace, secret.UID, secret.ResourceVersion, secret.Key, r.ControllerName, r.ControllerUID, r.ControllerServiceAccount, r.ControllerImage, r.Mode, r.WatchNamespace, r.AIWorkerImage, r.WorkerAccount, r.WorkerAccountUID, r.Helm.Chart, r.Helm.Instance, r.Helm.App, r.Helm.ManagedBy} {
		if !verificationIdentity(identity) {
			return false
		}
	}
	if !utf8.ValidString(s.Model) || !utf8.ValidString(p.BaseURL) || len(s.Model) > maxSuitePassingReceiptBytes || strings.TrimSpace(s.Model) != s.Model || s.Model == "" || scaffold.ValidateSingleLineText(s.Model) != nil || len(p.BaseURL) > maxSuitePassingEndpointBytes || scaffold.ValidateSingleLineText(p.BaseURL) != nil {
		return false
	}
	if s.RequiredImages == nil || len(s.RequiredImages) != 0 || scaffold.ValidateObjectName(s.Member) != nil || scaffold.ValidateNamespace(t.Namespace) != nil || p.Namespace != t.Namespace || p.Name == s.physicalMember() || scaffold.ValidateObjectName(p.Name) != nil || p.Generation < 1 || p.Type != "openai" || secret.Namespace != t.Namespace || secret.Name != p.SecretName || secret.Key != p.SecretKey || !secret.KeyPresent || scaffold.ValidateObjectName(secret.Name) != nil || scaffold.ValidateOrkaSecretKey(secret.Key) != nil || scaffold.ValidateOrkaProvider(p.Type, s.Model, p.BaseURL, "", "") != nil || r.ControllerGeneration < 1 || r.ControllerReplicas < 1 || scaffold.ValidateObjectName(r.ControllerName) != nil || !suiteNativePinnedRuntime(r, t.Namespace) {
		return false
	}
	return true
}

func coherentSuitePassingSubject(s suiteNativeSubject, suite *workspace.NativeSuite) error {
	if s.FlowCandidate != nil || !s.validPhysicalMember() || !suiteNativeOrkaInput(suite) || s.Suite != suite.Name || s.SuiteDigest != suite.Digest || s.GraphDigest != suite.GraphDigest || s.Member != suite.MemberID || s.MemberDigest != suite.MemberDigest || s.InstructionsDigest != suite.InstructionsDigest || s.CompositionDigest != suite.CompositionDigest || s.BuildProfileDigest != suite.BuildProfileDigest || s.Model != suite.Model || s.Renderer != suite.RendererVersion || !reflect.DeepEqual(s.RequiredImages, suite.RequiredImages) {
		return errSuitePassingReceipt
	}
	binding, err := suiteNativeDigest("kmx.native-orka.binding/v1", struct {
		Model          string
		RequiredImages []string
		Binding        suiteNativeBinding
		Runtime        suiteNativeRuntime
	}{s.Model, s.RequiredImages, s.Binding, s.Runtime})
	if err != nil || binding != s.BindingDigest {
		return errSuitePassingReceipt
	}
	p := s.Binding.Provider
	bundle, err := scaffold.GenerateOrka(scaffold.OrkaSpec{Name: s.physicalMember(), Namespace: s.Target.Namespace, Description: suite.Description, Model: suite.Model, Instructions: string(suite.Instructions), ProviderType: p.Type, BaseURL: p.BaseURL, SecretName: p.SecretName, SecretKey: p.SecretKey})
	if err != nil {
		return errSuitePassingReceipt
	}
	rendered, err := orkaRenderedDocuments(bundle)
	if err != nil {
		return errSuitePassingReceipt
	}
	documents := make([][]byte, 0, len(rendered))
	for _, doc := range rendered {
		documents = append(documents, doc.Bytes())
	}
	if s.PayloadDigest != agentruntime.RenderedBundleDigest(documents) {
		return errSuitePassingReceipt
	}
	expected := s.RenderedDigest
	s.RenderedDigest = ""
	revision, err := suiteNativeDigest("kmx.native-orka.revision/v1", s)
	if err != nil || expected != revision {
		return errSuitePassingReceipt
	}
	return nil
}

func suitePrefixedDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && verificationDigest(strings.TrimPrefix(value, "sha256:"))
}

// suiteCasePath accepts evals/<member>/cases/<id>.json.
func suiteCasePath(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 4 && parts[0] == "evals" && parts[1] != "" && parts[2] == "cases" && len(parts[3]) > len(".json") && strings.HasSuffix(parts[3], ".json") && path.Clean(value) == value && verificationIdentity(value)
}
