package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	rt "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

const suiteEvidenceMaxBytes = 1 << 20

// Private v1 evidence. No authored operands, answers, or arbitrary diagnostics
// have a field here. The canonical receipt digest lives outside the body.
type suiteEvaluationReceipt struct {
	SchemaVersion     int                    `json:"schemaVersion"`
	Domain            string                 `json:"domain"`
	ExecutionMode     string                 `json:"executionMode"`
	RunID             string                 `json:"runID"`
	Subject           suiteNativeSubject     `json:"subject"`
	Placement         suiteEvidencePlacement `json:"placement"`
	CasesDigest       string                 `json:"casesDigest"`
	FullCaseSet       bool                   `json:"fullCaseSet"`
	PromotionEligible bool                   `json:"promotionEligible"`
	Result            rt.EvaluationVerdict   `json:"result"`
	Detail            string                 `json:"detail"`
	Cases             []suiteEvidenceCase    `json:"cases"`
}
type suiteEvidencePlacement struct {
	Before    []suiteNativePairObservation `json:"before"`
	Resources []suiteNativeResource        `json:"resources"`
	Complete  bool                         `json:"complete"`
	// Intended markers; only Complete with attributable resources proves them.
	Ownership orkaSuiteOwnership `json:"ownership"`
}
type suiteEvidenceCase struct {
	ID           string                   `json:"id"`
	Path         string                   `json:"path"`
	CaseDigest   string                   `json:"caseDigest"`
	InputDigest  string                   `json:"inputDigest"`
	TaskName     string                   `json:"taskName"`
	TaskUID      string                   `json:"taskUID"`
	AnswerSHA256 string                   `json:"answerSHA256"`
	Verdict      rt.EvaluationVerdict     `json:"verdict"`
	Detail       string                   `json:"detail"`
	Assertions   []suiteEvidenceAssertion `json:"assertions"`
}
type suiteEvidenceAssertion struct {
	ID               string                      `json:"id"`
	Type             string                      `json:"type"`
	DefinitionDigest string                      `json:"definitionDigest"`
	EvaluatorVersion string                      `json:"evaluatorVersion"`
	Verdict          rt.EvaluationVerdict        `json:"verdict"`
	Reason           string                      `json:"reason"`
	Judge            *rt.EvaluationJudgeEvidence `json:"judge,omitempty"`
}

func suiteEvidenceError(code string) error { return errors.New("native suite evidence: " + code) }

// Initialize every selected case before execution. Judge transport credentials
// are never accepted here: only the explicitly configured base URL is consumed,
// normalized and hashed separately with each authored judge model/version.
// Missing configuration cannot become an invented qualified evaluator.
func unknownSuiteEvidenceCase(p workspace.PackagedCase, judgeBaseURL string) (suiteEvidenceCase, error) {
	c, err := rt.ParseEvaluationCase(p.Bytes)
	if err != nil || p.Digest != "sha256:"+rt.EvaluationCaseDigest(p.Bytes) {
		return suiteEvidenceCase{}, suiteEvidenceError("packaged case metadata")
	}
	result := suiteNativeCaseResult{CaseDigest: rt.EvaluationCaseDigest(p.Bytes), InputDigest: rt.EvaluationInputDigest(c.Input), EvaluationReceipt: rt.EvaluationReceipt{CaseID: c.ID, Verdict: rt.EvaluationUnknown, Detail: evaluationExecutionUnavailable, Assertions: rt.EvaluateAssertions(c, rt.EvaluationEvidence{})}}
	for i, d := range rt.EvaluationCaseAssertions(c) {
		if d.Type != "llmJudge" {
			continue
		}
		endpoint, err := canonicalEvaluationJudgeURL(judgeBaseURL)
		if err != nil {
			return suiteEvidenceCase{}, suiteEvidenceError("judge configuration required")
		}
		result.Assertions[i].Judge = &rt.EvaluationJudgeEvidence{Model: d.JudgeModel, ModelVersion: d.JudgeVersion, Provenance: "user-declared", EvaluatorDigest: rt.EvaluationJudgeEvaluatorDigest(), ConfigurationDigest: rt.EvaluationJudgeConfigurationDigest(endpoint, d.JudgeModel, d.JudgeVersion), MinScore: *d.MinScore}
	}
	return suiteEvidenceCaseFromNative(p, result)
}

// Map only operand-free native metadata. Matched/Missing deliberately have no
// destination fields. Validate against exact bytes before retaining evidence.
func suiteEvidenceCaseFromNative(p workspace.PackagedCase, native suiteNativeCaseResult) (suiteEvidenceCase, error) {
	c, err := rt.ParseEvaluationCase(p.Bytes)
	if err != nil || p.Digest != "sha256:"+rt.EvaluationCaseDigest(p.Bytes) || native.CaseID != c.ID || native.CaseDigest != rt.EvaluationCaseDigest(p.Bytes) || native.InputDigest != rt.EvaluationInputDigest(c.Input) {
		return suiteEvidenceCase{}, suiteEvidenceError("native packaged case mismatch")
	}
	result := suiteEvidenceCase{ID: c.ID, Path: p.Path, CaseDigest: native.CaseDigest, InputDigest: native.InputDigest, TaskName: native.TaskName, TaskUID: native.TaskUID, AnswerSHA256: native.AnswerSHA256, Verdict: native.Verdict, Detail: native.Detail, Assertions: []suiteEvidenceAssertion{}}
	for _, a := range native.Assertions {
		result.Assertions = append(result.Assertions, suiteEvidenceAssertion{ID: a.ID, Type: a.Type, DefinitionDigest: a.DefinitionDigest, EvaluatorVersion: suiteAssertionVersion(a.Type), Verdict: a.Verdict, Reason: a.Reason, Judge: a.Judge})
	}
	if err := validateSuiteEvidenceCase(result, rt.EvaluationCaseAssertions(c)); err != nil {
		return suiteEvidenceCase{}, err
	}
	raw, err := spec.Marshal(result)
	var owned suiteEvidenceCase
	if err != nil || decodeSuiteJSON(raw, &owned) != nil {
		return suiteEvidenceCase{}, suiteEvidenceError("native case shape")
	}
	return owned, nil
}

// The caller supplies the entire preinitialized selected inventory, replacing
// each row once with observed native evidence. diagnostic is selection intent,
// not inferred from counts (a one-case --case run remains diagnostic).
func newSuiteEvaluationReceipt(s *workspace.NativeSuite, placement *suiteNativePlacement, runID string, diagnostic bool, cases []suiteEvidenceCase, detail string) (suiteEvaluationReceipt, error) {
	if placement == nil {
		return suiteEvaluationReceipt{}, suiteEvidenceError("placement subject required")
	}
	_, digest, err := suiteEvidenceInventory(s)
	if err != nil {
		return suiteEvaluationReceipt{}, err
	}
	result := suiteEvidenceOverall(cases)
	if !placement.Complete || detail != "" {
		result = rt.EvaluationUnknown
	}
	r := suiteEvaluationReceipt{SchemaVersion: 1, Domain: "suite-evaluation", ExecutionMode: "native-orka", RunID: runID, Subject: placement.Subject, Placement: suiteEvidencePlacement{Before: placement.Before, Resources: placement.Resources, Complete: placement.Complete, Ownership: suiteEvidenceOwner(placement.Subject)}, CasesDigest: digest, FullCaseSet: !diagnostic, PromotionEligible: !diagnostic && placement.Complete && detail == "" && result == rt.EvaluationPass, Result: result, Detail: detail, Cases: cases}
	raw, _, err := canonicalSuiteEvaluationReceipt(s, placement.Subject, r)
	if err != nil {
		return suiteEvaluationReceipt{}, err
	}
	// Round-trip gives the immutable receipt owned arrays/pointers, separate from
	// live operational placement state and mutable per-case results.
	return decodeSuiteEvaluationReceipt(raw)
}

// The public format helpers already enforce bounded canonical JSON, exact keys,
// duplicate refusal and number equivalence. Mask their content-bearing errors.
// Additionally require every non-optional member, including false/empty values,
// and reject explicit null even on optional pointers/slices.
func decodeSuiteJSON(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > suiteEvidenceMaxBytes || !unambiguousSessionsReceiptJSON(raw) || spec.Unmarshal(raw, out) != nil {
		return suiteEvidenceError("invalid JSON")
	}
	if !suiteRequiredJSON(raw, reflect.TypeOf(out)) {
		return suiteEvidenceError("missing or null metadata")
	}
	canonical, err := spec.Marshal(out)
	// Run IDs intentionally use the existing 32-hex identity format, which is
	// also a credential shape. Exempt only that validated root identity, never
	// arbitrary metadata or nested values.
	var metadata map[string]json.RawMessage
	if err == nil && json.Unmarshal(canonical, &metadata) == nil {
		var id string
		if json.Unmarshal(metadata["runID"], &id) == nil && evaluationRunID(id) {
			metadata["runID"], _ = json.Marshal("run-" + id)
			canonical, err = spec.Marshal(metadata)
		}
	}
	if err != nil || scaffold.RefuseKeyShapes(string(canonical)) != nil {
		return suiteEvidenceError("unsafe metadata")
	}
	return nil
}
func suiteRequiredJSON(raw json.RawMessage, typ reflect.Type) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return false
		}
		for _, f := range reflect.VisibleFields(typ) {
			if !f.IsExported() {
				continue
			}
			tag := strings.Split(f.Tag.Get("json"), ",")
			name := tag[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			value, present := object[name]
			if !present {
				if len(tag) > 1 && tag[1] == "omitempty" {
					continue
				}
				return false
			}
			if !suiteRequiredJSON(value, f.Type) {
				return false
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, v := range values {
			if !suiteRequiredJSON(v, typ.Elem()) {
				return false
			}
		}
	}
	return true
}
func decodeSuiteEvaluationReceipt(raw []byte) (suiteEvaluationReceipt, error) {
	var r suiteEvaluationReceipt
	if err := decodeSuiteJSON(raw, &r); err != nil {
		return suiteEvaluationReceipt{}, err
	}
	if r.SchemaVersion != 1 || r.Domain != "suite-evaluation" || r.ExecutionMode != "native-orka" || !evaluationRunID(r.RunID) || r.Subject.RequiredImages == nil || len(r.Subject.RequiredImages) != 0 {
		return suiteEvaluationReceipt{}, suiteEvidenceError("unsupported receipt")
	}
	return r, nil
}
func canonicalSuiteEvaluationReceipt(s *workspace.NativeSuite, expected suiteNativeSubject, r suiteEvaluationReceipt) ([]byte, string, error) {
	if err := validateSuiteEvaluationReceipt(s, expected, r); err != nil {
		return nil, "", err
	}
	raw, err := spec.Marshal(r)
	if err != nil || len(raw) > suiteEvidenceMaxBytes {
		return nil, "", suiteEvidenceError("receipt size or encoding")
	}
	digest, err := spec.Digest(raw)
	if err != nil {
		return nil, "", suiteEvidenceError("receipt digest")
	}
	return raw, digest, nil
}

func validateSuiteEvaluationReceipt(s *workspace.NativeSuite, expected suiteNativeSubject, r suiteEvaluationReceipt) error {
	raw, err := spec.Marshal(r)
	var shape suiteEvaluationReceipt
	if err != nil || decodeSuiteJSON(raw, &shape) != nil {
		return suiteEvidenceError("receipt shape")
	}
	if r.SchemaVersion != 1 || r.Domain != "suite-evaluation" || r.ExecutionMode != "native-orka" || !evaluationRunID(r.RunID) {
		return suiteEvidenceError("unsupported receipt")
	}
	if err := validateSuiteEvidenceSubject(s, expected); err != nil {
		return err
	}
	if !reflect.DeepEqual(r.Subject, expected) {
		return suiteEvidenceError("evaluated subject mismatch")
	}
	inventory, digest, err := suiteEvidenceInventory(s)
	if err != nil {
		return err
	}
	if r.CasesDigest != digest || len(r.Cases) == 0 || len(r.Cases) > len(inventory) || r.FullCaseSet && len(r.Cases) != len(inventory) {
		return suiteEvidenceError("case inventory coverage")
	}
	if err := validateSuiteEvidencePlacement(r.Subject, r.Placement); err != nil {
		return err
	}
	names, uids, seen := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range r.Cases {
		def, ok := inventory[c.Path]
		if !ok || seen[c.Path] || c.ID != def.CaseID || c.CaseDigest != def.CaseDigest || c.InputDigest != def.InputDigest {
			return suiteEvidenceError("case identity")
		}
		seen[c.Path] = true
		if !r.Placement.Complete && (c.Verdict != rt.EvaluationUnknown || c.Detail != evaluationExecutionUnavailable || c.TaskName != "" || c.TaskUID != "" || c.AnswerSHA256 != "") {
			return suiteEvidenceError("incomplete placement cannot prove execution")
		}
		if err := validateSuiteEvidenceCase(c, def.Assertions); err != nil {
			return err
		}
		if c.TaskName != "" {
			if names[c.TaskName] {
				return suiteEvidenceError("repeated Task name")
			}
			names[c.TaskName] = true
		}
		if c.TaskUID != "" {
			if uids[c.TaskUID] {
				return suiteEvidenceError("repeated Task UID")
			}
			uids[c.TaskUID] = true
		}
	}
	if !suiteRunDetail(r.Detail) {
		return suiteEvidenceError("run detail")
	}
	result := suiteEvidenceOverall(r.Cases)
	if !r.Placement.Complete || r.Detail != "" {
		result = rt.EvaluationUnknown
	}
	eligible := r.FullCaseSet && r.Placement.Complete && r.Detail == "" && result == rt.EvaluationPass
	if r.Result != result || r.PromotionEligible != eligible {
		return suiteEvidenceError("inconsistent run verdict")
	}
	return nil
}

type suiteEvidenceDefinition struct {
	CaseID, CaseDigest, InputDigest string
	Assertions                      []rt.EvaluationAssertion
}

func suiteEvidenceInventory(s *workspace.NativeSuite) (map[string]suiteEvidenceDefinition, string, error) {
	if s == nil || len(s.Cases) == 0 || len(s.Cases) > 128 {
		return nil, "", suiteEvidenceError("packaged case inventory")
	}
	inventory := map[string]suiteEvidenceDefinition{}
	ids := map[string]bool{}
	files := []rt.EvaluationCaseFile{}
	for _, p := range s.Cases {
		name := strings.TrimPrefix(p.Path, "metadata/kmx/eval/")
		if name == p.Path || strings.ContainsAny(name, "/\\") || name == "" || !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil, "", suiteEvidenceError("packaged case path")
		}
		digest := rt.EvaluationCaseDigest(p.Bytes)
		c, err := rt.ParseEvaluationCase(p.Bytes)
		if err != nil || p.Digest != "sha256:"+digest || ids[c.ID] {
			return nil, "", suiteEvidenceError("packaged case metadata")
		}
		if _, ok := inventory[p.Path]; ok {
			return nil, "", suiteEvidenceError("packaged case duplication")
		}
		ids[c.ID] = true
		inventory[p.Path] = suiteEvidenceDefinition{c.ID, digest, rt.EvaluationInputDigest(c.Input), rt.EvaluationCaseAssertions(c)}
		files = append(files, rt.EvaluationCaseFile{Name: name, Bytes: p.Bytes})
	}
	return inventory, rt.EvaluationCasesDigest(files), nil
}
func suiteAssertionVersion(kind string) string {
	if kind == "llmJudge" {
		return "kmx.llm-judge/v1"
	}
	return "kmx.assertions/v1"
}
func validateSuiteEvidenceCase(c suiteEvidenceCase, defs []rt.EvaluationAssertion) error {
	if len(c.Assertions) != len(defs) {
		return suiteEvidenceError("assertion coverage")
	}
	expected := map[string]rt.EvaluationAssertion{}
	for _, d := range defs {
		expected[d.ID] = d
	}
	rows := make([]rt.EvaluationAssertionResult, 0, len(c.Assertions))
	for _, a := range c.Assertions {
		d, ok := expected[a.ID]
		if !ok || a.Type != d.Type || a.DefinitionDigest != rt.EvaluationAssertionDigest(d) || a.EvaluatorVersion != suiteAssertionVersion(d.Type) {
			return suiteEvidenceError("assertion or evaluator identity")
		}
		delete(expected, a.ID)
		if d.Type == "llmJudge" && (a.Judge == nil || a.Judge.Model != d.JudgeModel || a.Judge.ModelVersion != d.JudgeVersion || d.MinScore == nil || a.Judge.MinScore != *d.MinScore) {
			return suiteEvidenceError("judge identity")
		}
		rows = append(rows, rt.EvaluationAssertionResult{ID: a.ID, Type: a.Type, DefinitionDigest: a.DefinitionDigest, Verdict: a.Verdict, Reason: a.Reason, Judge: a.Judge})
	}
	if !evaluationCaseEvidence(c.CaseDigest, c.InputDigest, string(c.Verdict), c.Detail, rows) {
		return suiteEvidenceError("inconsistent case verdict")
	}
	if c.TaskName != "" && (!verificationIdentity(c.TaskName) || scaffold.ValidateObjectName(c.TaskName) != nil) || c.TaskUID != "" && (!verificationIdentity(c.TaskUID) || c.TaskName == "") || c.AnswerSHA256 != "" && (!verificationDigest(c.AnswerSHA256) || c.TaskUID == "") {
		return suiteEvidenceError("native result identity")
	}
	if c.Detail == "" && (c.TaskName == "" || c.TaskUID == "" || c.AnswerSHA256 == "") || c.Detail == evaluationExecutionFailed && (c.TaskName == "" || c.TaskUID == "") {
		return suiteEvidenceError("missing native result proof")
	}
	return nil
}
func suiteEvidenceOverall(cases []suiteEvidenceCase) rt.EvaluationVerdict {
	verdict := rt.EvaluationPass
	if len(cases) == 0 {
		return rt.EvaluationUnknown
	}
	for _, c := range cases {
		if c.Verdict == rt.EvaluationFail {
			return rt.EvaluationFail
		}
		if c.Verdict != rt.EvaluationPass {
			verdict = rt.EvaluationUnknown
		}
	}
	return verdict
}
func suiteRunDetail(detail string) bool {
	return detail == "" || detail == "placement unavailable" || detail == "subject unavailable" || detail == "run incomplete"
}
func suiteEvidenceOwner(s suiteNativeSubject) orkaSuiteOwnership {
	return orkaSuiteOwnership{Suite: s.Suite, Digest: s.SuiteDigest, Member: s.Member, Renderer: s.Renderer, BindingDigest: s.BindingDigest, RenderedDigest: s.RenderedDigest}
}
func validateSuiteEvidencePlacement(s suiteNativeSubject, p suiteEvidencePlacement) error {
	if p.Ownership != suiteEvidenceOwner(s) || len(p.Resources) > 2 || len(p.Before) != 2 || p.Complete && len(p.Resources) != 2 {
		return suiteEvidenceError("placement coverage or markers")
	}
	for i, b := range p.Before {
		if b.Kind != []string{"Provider", "Agent"}[i] || b.Name != s.Member || b.Namespace != s.Target.Namespace || b.Exists != (b.Identity != nil) {
			return suiteEvidenceError("before pair identity")
		}
		if b.Identity != nil && (!suiteOwnedEvidence(*b.Identity, b.Kind, b.Name, b.Namespace) || b.Identity.Ownership.Suite != s.Suite || b.Identity.Ownership.Member != s.Member || b.Identity.Ownership.Renderer != s.Renderer) {
			return suiteEvidenceError("before owned identity")
		}
	}
	ownedUIDs := map[string]bool{}
	for i, r := range p.Resources {
		before := p.Before[i]
		if (r.Outcome == rt.ResourceCreated) == before.Exists {
			return suiteEvidenceError("placement outcome contradicts before identity")
		}
		if r.UID != "" {
			if ownedUIDs[r.UID] {
				return suiteEvidenceError("repeated owned UID")
			}
			ownedUIDs[r.UID] = true
		}
		if before.Identity != nil && r.UID != "" && (r.UID != before.Identity.UID || r.Generation < before.Identity.Generation) {
			return suiteEvidenceError("owned identity changed during placement")
		}
		if r.Ready && r.Outcome == rt.ResourceReused && (r.Generation != before.Identity.Generation || r.SpecDigest != before.Identity.SpecDigest) {
			return suiteEvidenceError("reuse changed owned spec or generation")
		}
		if r.Kind != []string{"Provider", "Agent"}[i] || r.Name != s.Member || r.Namespace != s.Target.Namespace {
			return suiteEvidenceError("owned resource identity")
		}
		if r.Outcome != rt.ResourceCreated && r.Outcome != rt.ResourceReused && r.Outcome != rt.ResourceUpdated || r.Outcome != rt.ResourceReused && !r.AttemptedWrite || r.Ambiguous && (!r.AttemptedWrite || r.Applied || r.Ready) || r.Ready && !r.Applied {
			return suiteEvidenceError("owned resource outcome")
		}
		if r.UID != "" && (!verificationIdentity(r.UID) || r.Generation < 1) || r.UID == "" && r.Generation != 0 || r.SpecDigest != "" && !verificationDigest(r.SpecDigest) {
			return suiteEvidenceError("owned resource metadata")
		}
		if (p.Complete || r.Applied || r.Ready) && (r.UID == "" || r.Generation < 1) || (p.Complete || r.Ready) && (!r.Applied || !r.Ready || r.Ambiguous || !verificationDigest(r.SpecDigest)) {
			return suiteEvidenceError("unattributable placement")
		}
		if r.Observed != nil {
			o := *r.Observed
			if !suiteOwnedEvidence(o, r.Kind, r.Name, r.Namespace) {
				return suiteEvidenceError("residual identity")
			}
			if p.Complete && (o.UID != r.UID || o.Generation != r.Generation || o.SpecDigest != r.SpecDigest || o.Ownership != p.Ownership || !o.Ready) {
				return suiteEvidenceError("residual is not placement proof")
			}
		}
	}
	return nil
}
func suiteOwnedEvidence(o suiteNativeOwnedIdentity, kind, name, ns string) bool {
	return o.Kind == kind && o.Name == name && o.Namespace == ns && verificationIdentity(o.UID) && o.Generation > 0 && verificationDigest(o.SpecDigest) && o.Ownership.validate() == nil
}

// Source identities are compared with the verified loader, not with receipt
// claims. Re-render only in memory to check B/payload/D derivation; no live I/O.
func validateSuiteEvidenceSubject(s *workspace.NativeSuite, subject suiteNativeSubject) error {
	if s == nil || s.RequiredImages == nil || len(s.RequiredImages) != 0 || subject.RequiredImages == nil || len(subject.RequiredImages) != 0 {
		return suiteEvidenceError("required images")
	}
	if subject.Suite != s.Name || subject.SuiteDigest != s.Digest || subject.GraphDigest != s.GraphDigest || subject.Member != s.MemberID || subject.MemberDigest != s.MemberDigest || subject.InstructionsDigest != s.InstructionsDigest || subject.Model != s.Model || subject.CompositionDigest != s.CompositionDigest || subject.BuildProfileDigest != s.BuildProfileDigest || subject.Renderer != s.RendererVersion || subject.Renderer != "kmx.native-orka/v1" {
		return suiteEvidenceError("packaged subject mismatch")
	}
	for _, digest := range []string{subject.SuiteDigest, subject.GraphDigest, subject.MemberDigest, subject.InstructionsDigest, subject.CompositionDigest, subject.BuildProfileDigest} {
		if !strings.HasPrefix(digest, "sha256:") || !verificationDigest(strings.TrimPrefix(digest, "sha256:")) {
			return suiteEvidenceError("packaged digest")
		}
	}
	if subject.SuiteDigest == subject.GraphDigest || "sha256:"+rt.EvaluationInputDigest(string(s.Instructions)) != s.InstructionsDigest || strings.TrimSpace(string(s.Instructions)) == "" {
		return suiteEvidenceError("source identity")
	}
	t, b, r := subject.Target, subject.Binding, subject.Runtime
	if !suiteMetadataStrings(t) || !suiteMetadataStrings(b) || !suiteMetadataStrings(r) || scaffold.ValidateNamespace(t.Namespace) != nil || scaffold.ValidateObjectName(b.Provider.Name) != nil || b.Provider.Name == s.MemberID || b.Provider.Namespace != t.Namespace || b.Secret.Namespace != t.Namespace || b.Provider.Generation < 1 || b.Provider.Type != "openai" || b.Provider.SecretName != b.Secret.Name || b.Provider.SecretKey != b.Secret.Key || !b.Secret.KeyPresent || scaffold.ValidateObjectName(b.Secret.Name) != nil || scaffold.ValidateOrkaSecretKey(b.Secret.Key) != nil {
		return suiteEvidenceError("target or binding identity")
	}
	if r.ID != "orka" || r.Version != OrkaVersion || r.Namespace != OrkaNamespace || r.ControllerGeneration < 1 || r.Mode != "harness-v2" || r.WatchNamespace != t.Namespace || r.WorkerTrust != "ai" || r.ControllerImage != "ghcr.io/orka-agents/orka@"+orkaControllerDigest || r.AIWorkerImage != "ghcr.io/orka-agents/orka/ai-worker@"+orkaAIWorkerDigest || r.Helm.Chart != "orka-"+strings.TrimPrefix(OrkaVersion, "v") || r.Helm.App != "orka" || r.Helm.ManagedBy != "Helm" {
		return suiteEvidenceError("runtime identity")
	}
	binding, err := suiteNativeDigest("kmx.native-orka.binding/v1", struct {
		Model          string             `json:"model"`
		RequiredImages []string           `json:"requiredImages"`
		Binding        suiteNativeBinding `json:"binding"`
		Runtime        suiteNativeRuntime `json:"runtime"`
	}{subject.Model, subject.RequiredImages, b, r})
	if err != nil || binding != subject.BindingDigest {
		return suiteEvidenceError("binding digest")
	}
	bundle, err := scaffold.GenerateOrka(scaffold.OrkaSpec{Name: s.MemberID, Namespace: t.Namespace, Description: s.Description, Model: s.Model, Instructions: string(s.Instructions), ProviderType: b.Provider.Type, BaseURL: b.Provider.BaseURL, SecretName: b.Provider.SecretName, SecretKey: b.Provider.SecretKey})
	if err != nil {
		return suiteEvidenceError("render subject")
	}
	var documents [][]byte
	for _, doc := range bundle.Documents() {
		raw, err := yaml.Marshal(doc)
		if err != nil {
			return suiteEvidenceError("render subject")
		}
		documents = append(documents, raw)
	}
	if rt.RenderedBundleDigest(documents) != subject.PayloadDigest {
		return suiteEvidenceError("payload digest")
	}
	want := subject.RenderedDigest
	subject.RenderedDigest = ""
	revision, err := suiteNativeDigest("kmx.native-orka.revision/v1", subject)
	if err != nil || revision != want {
		return suiteEvidenceError("rendered digest")
	}
	return nil
}
func suiteMetadataStrings(value any) bool {
	var scan func(reflect.Value) bool
	scan = func(v reflect.Value) bool {
		switch v.Kind() {
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				// Only this typed empty endpoint selects the native Provider default;
				// its JSON member remains required and nonempty URLs are still checked.
				if v.Type() == reflect.TypeFor[suiteNativeProvider]() && v.Type().Field(i).Name == "BaseURL" && v.Field(i).String() == "" {
					continue
				}
				if !scan(v.Field(i)) {
					return false
				}
			}
		case reflect.String:
			s := v.String()
			if len(s) == 0 || len(s) > 2048 || strings.TrimSpace(s) == "" || strings.ContainsAny(s, "*?\r\n\t") || scaffold.RefuseKeyShapes(s) != nil {
				return false
			}
			for _, r := range s {
				if r < 32 || r > 126 {
					return false
				}
			}
		}
		return true
	}
	return scan(reflect.ValueOf(value))
}
