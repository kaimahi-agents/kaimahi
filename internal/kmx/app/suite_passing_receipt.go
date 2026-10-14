package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path"
	"reflect"
	"strings"
	"unicode/utf8"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const maxSuitePassingReceiptBytes = 1 << 20
const maxSuitePassingEndpointBytes = 16 << 10
const suitePassingEvaluatorVersion = "kmx.text-assertions/v1"

var errSuitePassingReceipt = errors.New("invalid native passing receipt")

type suitePassingReceipt struct {
	SchemaVersion     int                        `json:"schemaVersion"`
	Domain            string                     `json:"domain"`
	ExecutionMode     string                     `json:"executionMode"`
	RunID             string                     `json:"runID"`
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
	ID               string `json:"id"`
	Type             string `json:"type"`
	DefinitionDigest string `json:"definitionDigest"`
	EvaluatorVersion string `json:"evaluatorVersion"`
	Verdict          string `json:"verdict"`
}
type suitePassingObservation struct {
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
	if suite == nil || len(suite.Cases) == 0 {
		return nil, "", errSuitePassingReceipt
	}
	inventory := make([]suitePassingInventoryCase, 0, len(suite.Cases))
	files := make([]agentruntime.EvaluationCaseFile, 0, len(suite.Cases))
	// Identity fields accept at most 256 printable ASCII bytes. Reserve their
	// escaped representation plus the bounded endpoint; model size is known.
	modelJSON, err := spec.Marshal(suite.Model)
	if err != nil {
		return nil, "", errSuitePassingReceipt
	}
	budget := 32768 + 6*maxSuitePassingEndpointBytes + len(modelJSON)
	if suite.Runtime.ID == "kagent" {
		budget += 32768
	}
	seen := map[string]bool{}
	for _, file := range suite.Cases {
		c, err := agentruntime.ParseEvaluationCase(file.Bytes)
		if err != nil {
			return nil, "", errors.New("invalid native evaluation case")
		}
		if seen[c.ID] || !suiteCasePath(file.Path) || file.Digest != "sha256:"+agentruntime.EvaluationCaseDigest(file.Bytes) {
			return nil, "", errSuitePassingReceipt
		}
		seen[c.ID] = true
		metadata := suitePassingCase{ID: c.ID, Path: file.Path, CaseDigest: agentruntime.EvaluationCaseDigest(file.Bytes), InputDigest: agentruntime.EvaluationInputDigest(c.Input), Verdict: "pass", Assertions: []suitePassingAssertion{}}
		for _, a := range agentruntime.EvaluationCaseAssertions(c) {
			if a.Type != "contains" && a.Type != "notContains" && a.Type != "regex" {
				return nil, "", errors.New("unsupported native suite evaluator")
			}
			metadata.Assertions = append(metadata.Assertions, suitePassingAssertion{a.ID, a.Type, agentruntime.EvaluationAssertionDigest(a), suitePassingEvaluatorVersion, "pass"})
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
		wire, err := spec.Marshal(projected)
		if err != nil {
			return nil, "", errSuitePassingReceipt
		}
		budget += len(wire) + 1
		if budget > maxSuitePassingReceiptBytes {
			return nil, "", errors.New("native suite receipt exceeds projection budget")
		}
		inventory = append(inventory, suitePassingInventoryCase{c, metadata})
		files = append(files, agentruntime.EvaluationCaseFile{Name: path.Base(file.Path), Bytes: file.Bytes})
	}
	return inventory, agentruntime.EvaluationCasesDigest(files), nil
}

// The caller must bracket actual execution with verifySuiteNative. Unsigned
// observations cannot prove drift absence, admission, or authentication.
func newSuitePassingReceipt(ctx context.Context, prepared *workspace.Prepared, placement *suiteNativePlacement, observations []suitePassingObservation) (*suitePassingReceipt, error) {
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
	runID, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	r := &suitePassingReceipt{SchemaVersion: 1, Domain: "kmx.native-suite.passing/v1", ExecutionMode: "native-orka", RunID: runID, SourceFingerprint: prepared.Fingerprint, Subject: placement.Subject, Provider: placement.Provider, Agent: placement.Agent, CasesDigest: casesDigest, Result: "pass", Cases: make([]suitePassingCase, 0, len(inventory))}
	for i, observed := range observations {
		row := inventory[i].Metadata
		row.TaskName, row.TaskUID, row.AnswerDigest = observed.TaskName, observed.TaskUID, observed.AnswerSHA256
		row.Verdict = string(observed.Verdict)
		row.Assertions = make([]suitePassingAssertion, 0, len(observed.Assertions))
		for _, a := range observed.Assertions {
			row.Assertions = append(row.Assertions, suitePassingAssertion{a.ID, a.Type, a.DefinitionDigest, suitePassingEvaluatorVersion, string(a.Verdict)})
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
	original, err := spec.Marshal(document)
	if err != nil {
		return nil, errSuitePassingReceipt
	}
	typed, err := spec.Marshal(r)
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
		if r.Kagent != nil || r.Sessions != nil || !validSuitePassingSubject(r.Subject) || r.Provider.UID == r.Agent.UID {
			return errSuitePassingReceipt
		}
	} else {
		if !reflect.DeepEqual(r.Subject, suiteNativeSubject{}) || r.Provider != (suiteNativeOwnedResource{}) || r.Agent != (suiteNativeOwnedResource{}) {
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
	ids, paths, tasks, uids := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range r.Cases {
		if !evaluationEvidenceID.MatchString(c.ID) || !verificationIdentity(c.ID) || !suiteCasePath(c.Path) || !verificationDigest(c.CaseDigest) || !verificationDigest(c.InputDigest) || !verificationDigest(c.AnswerDigest) || c.AnswerDigest == agentruntime.EvaluationAnswerDigest("") || c.Verdict != "pass" || len(c.Assertions) == 0 || ids[c.ID] || paths[c.Path] {
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
		ids[c.ID], paths[c.Path] = true, true
		assertions := map[string]bool{}
		for _, a := range c.Assertions {
			if !evaluationEvidenceID.MatchString(a.ID) || !verificationIdentity(a.ID) || assertions[a.ID] || (a.Type != "contains" && a.Type != "notContains" && a.Type != "regex") || !verificationDigest(a.DefinitionDigest) || a.EvaluatorVersion != suitePassingEvaluatorVersion || a.Verdict != "pass" {
				return errSuitePassingReceipt
			}
			assertions[a.ID] = true
		}
	}
	masked := *r
	masked.RunID = "run"
	raw, err := spec.Marshal(masked)
	if err != nil || scaffold.RefuseKeyShapes(string(raw)) != nil {
		return errors.New("refusing credential-shaped native receipt metadata")
	}
	return nil
}

func canonicalSuitePassingReceipt(ctx context.Context, r *suitePassingReceipt, suite *workspace.NativeSuite) ([]byte, error) {
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
		row := r.Cases[i]
		row.TaskName, row.TaskUID, row.AnswerDigest = "", "", ""
		row.KagentExecution = nil
		row.SessionsExecution = nil
		if !reflect.DeepEqual(row, expected.Metadata) {
			return nil, errSuitePassingReceipt
		}
	}
	raw, err := spec.Marshal(r)
	if err != nil || len(raw) > maxSuitePassingReceiptBytes {
		return nil, errSuitePassingReceipt
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return raw, nil
}

func validSuitePassingSubject(s suiteNativeSubject) bool {
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
	if s.RequiredImages == nil || len(s.RequiredImages) != 0 || scaffold.ValidateObjectName(s.Member) != nil || scaffold.ValidateNamespace(t.Namespace) != nil || p.Namespace != t.Namespace || p.Name == s.Member || scaffold.ValidateObjectName(p.Name) != nil || p.Generation < 1 || p.Type != "openai" || secret.Namespace != t.Namespace || secret.Name != p.SecretName || secret.Key != p.SecretKey || !secret.KeyPresent || scaffold.ValidateObjectName(secret.Name) != nil || scaffold.ValidateOrkaSecretKey(secret.Key) != nil || scaffold.ValidateOrkaProvider(p.Type, s.Model, p.BaseURL, "", "") != nil || r.ControllerGeneration < 1 || r.ControllerReplicas < 1 || scaffold.ValidateObjectName(r.ControllerName) != nil || !suiteNativePinnedRuntime(r, t.Namespace) {
		return false
	}
	return true
}

func coherentSuitePassingSubject(s suiteNativeSubject, suite *workspace.NativeSuite) error {
	if !suiteNativeOrkaInput(suite) || s.Suite != suite.Name || s.SuiteDigest != suite.Digest || s.GraphDigest != suite.GraphDigest || s.Member != suite.MemberID || s.MemberDigest != suite.MemberDigest || s.InstructionsDigest != suite.InstructionsDigest || s.CompositionDigest != suite.CompositionDigest || s.BuildProfileDigest != suite.BuildProfileDigest || s.Model != suite.Model || s.Renderer != suite.RendererVersion || !reflect.DeepEqual(s.RequiredImages, suite.RequiredImages) {
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
	bundle, err := scaffold.GenerateOrka(scaffold.OrkaSpec{Name: s.Member, Namespace: s.Target.Namespace, Description: suite.Description, Model: suite.Model, Instructions: string(suite.Instructions), ProviderType: p.Type, BaseURL: p.BaseURL, SecretName: p.SecretName, SecretKey: p.SecretKey})
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
func suiteCasePath(value string) bool {
	return strings.HasPrefix(value, "metadata/kmx/eval/") && path.Clean(value) == value && verificationIdentity(value)
}
