package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"unicode/utf8"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// Kagent v2 is an offline evidence schema, not execution or promotion support.
// Native A2A identities are deliberately separate from Orka Kubernetes Tasks.
// Like other local receipts, this evidence is forgeable, not an attestation.
type kagentEvaluationReceipt struct {
	SchemaVersion           int                      `json:"schemaVersion"`
	RunID                   string                   `json:"runID"`
	Bundle                  string                   `json:"bundle"`
	PortableDigest          string                   `json:"portableDigest"`
	RenderedDigest          string                   `json:"renderedDigest"`
	DeploymentReceiptDigest string                   `json:"deploymentReceiptDigest"`
	CasesDigest             string                   `json:"casesDigest"`
	FullCaseSet             bool                     `json:"fullCaseSet"`
	GitCommit               string                   `json:"gitCommit"`
	Target                  kagentEvaluationTarget   `json:"target"`
	Result                  string                   `json:"result"`
	Cases                   []kagentEvaluationResult `json:"cases"`
}

type kagentEvaluationTarget struct {
	Runtime               agentruntime.ID          `json:"runtime"`
	RuntimeVersion        string                   `json:"runtimeVersion"`
	Engine                string                   `json:"engine"`
	Context               string                   `json:"context"`
	ClusterUID            string                   `json:"clusterUID"`
	Namespace             string                   `json:"namespace"`
	Agent                 string                   `json:"agent"`
	AgentUID              string                   `json:"agentUID"`
	AgentGeneration       int64                    `json:"agentGeneration"`
	ModelConfigUID        string                   `json:"modelConfigUID"`
	ModelConfigGeneration int64                    `json:"modelConfigGeneration"`
	Model                 string                   `json:"model"`
	Platform              kagentEvaluationPlatform `json:"platform"`
	Snapshot              kagentEvaluationSnapshot `json:"snapshot"`
}

type kagentEvaluationImage struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Digest       string `json:"digest"`
}

type kagentEvaluationPlatform struct {
	SourceCommit    string                `json:"sourceCommit"`
	ControllerImage kagentEvaluationImage `json:"controllerImage"`
	ADKImage        kagentEvaluationImage `json:"adkImage"`
	// Ordered Service, Deployment, ConfigMap metadata; no configuration bodies.
	Controller []agentruntime.ResourceEvidence `json:"controller"`
}

type kagentEvaluationSnapshot struct {
	// These retain the lift receipt's ResourceEvidence wire shape and order.
	Proofs       []agentruntime.ResourceEvidence `json:"proofs"`
	Workload     []agentruntime.ResourceEvidence `json:"workload"`
	Dependencies []agentruntime.ResourceEvidence `json:"dependencies"`
}

type kagentEvaluationResult struct {
	ID           string                                   `json:"id"`
	Verdict      string                                   `json:"verdict"`
	CaseDigest   string                                   `json:"caseDigest"`
	InputDigest  string                                   `json:"inputDigest"`
	Assertions   []agentruntime.EvaluationAssertionResult `json:"assertions"`
	Execution    kagentEvaluationExecution                `json:"execution"`
	AnswerSHA256 string                                   `json:"answerSHA256,omitempty"`
	Detail       string                                   `json:"detail,omitempty"`
}

type kagentEvaluationExecution struct {
	RPCID            string `json:"rpcID"`
	RequestMessageID string `json:"requestMessageID"`
	TaskID           string `json:"taskID,omitempty"`
	ContextID        string `json:"contextID,omitempty"`
	State            string `json:"state,omitempty"`
}

// Parse only bounded, unambiguous, payload-free evidence. A typed decoder alone
// accepts null scalars and duplicate/case-folded keys, so reuse the shared raw
// shape checks before decoding. The full-set marker must be explicit, even false.
func parseKagentEvaluation(raw []byte) (normalizedEvaluation, bool) {
	var empty normalizedEvaluation
	if len(raw) > maxSessionsReceiptBytes || !utf8.Valid(raw) || !unambiguousSessionsReceiptJSON(raw) {
		return empty, false
	}
	var r kagentEvaluationReceipt
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil {
		return empty, false
	}
	var extra any
	if d.Decode(&extra) != io.EOF || r.SchemaVersion != 2 || !evaluationReceiptJSONShape(raw, r.SchemaVersion, r.RunID, false) {
		return empty, false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return empty, false
	}
	if _, present := root["fullCaseSet"]; !present {
		return empty, false
	}
	return normalizeKagentEvaluation(r)
}

var kagentEvaluationID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,255}$`)

func safeKagentEvaluationID(value string) bool {
	return kagentEvaluationID.MatchString(value) && verificationIdentity(value)
}

func qualifiedKagentEvaluationImage(image kagentEvaluationImage, pins [3]string) bool {
	if image.OS != "linux" {
		return false
	}
	platform := 1
	switch image.Architecture {
	case "amd64":
	case "arm64":
		platform = 2
	default:
		return false
	}
	return image.Digest == pins[0] || image.Digest == pins[platform]
}

func kagentEvaluationResource(p agentruntime.ResourceEvidence) bool {
	return scaffold.ValidateObjectName(p.Name) == nil && scaffold.ValidateNamespace(p.Namespace) == nil && safeKagentEvaluationID(p.UID) && (p.ResourceVersion == "" || safeKagentEvaluationID(p.ResourceVersion)) && p.Generation >= 0
}

func validKagentEvaluationPlatform(p kagentEvaluationPlatform) bool {
	profile := kagentLiftProfile()
	if p.SourceCommit != profile.Commit || !qualifiedKagentEvaluationImage(p.ControllerImage, profile.ControllerDigests) || !qualifiedKagentEvaluationImage(p.ADKImage, profile.GoDigests) || len(p.Controller) != 3 {
		return false
	}
	namespace := p.Controller[0].Namespace
	uids := map[string]bool{}
	for i, kind := range []string{"Service", "Deployment", "ConfigMap"} {
		c := p.Controller[i]
		if !kagentEvaluationResource(c) || c.Kind != kind || c.Namespace != namespace || c.ResourceVersion == "" || c.Key != "" || c.SpecDigest != "" || uids[c.UID] {
			return false
		}
		uids[c.UID] = true
		if kind == "Deployment" {
			if c.Generation < 1 {
				return false
			}
		} else if c.Generation != 0 {
			return false
		}
	}
	return p.Controller[0].Name == p.Controller[1].Name && p.Controller[0].Name == p.Controller[2].Name
}

func validKagentEvaluationSnapshot(t kagentEvaluationTarget) bool {
	s := t.Snapshot
	if len(s.Proofs) != 2 || len(s.Workload) != 4 || len(s.Dependencies) == 0 || len(s.Dependencies) > 1000 {
		return false
	}
	type resourceKey struct{ kind, namespace, name string }
	seen := map[resourceKey]agentruntime.ResourceEvidence{}
	uids := map[string]resourceKey{}
	// A resource may appear in several sections, but every observation must
	// agree. Referenced keys differ; the underlying object identity does not.
	observe := func(p agentruntime.ResourceEvidence) bool {
		key := resourceKey{p.Kind, p.Namespace, p.Name}
		p.Key = ""
		if previous, ok := seen[key]; ok {
			return previous == p
		}
		if previous, ok := uids[p.UID]; ok && previous != key {
			return false
		}
		seen[key], uids[p.UID] = p, key
		return true
	}
	for _, p := range t.Platform.Controller {
		if !observe(p) {
			return false
		}
	}
	for i, kind := range []string{"ModelConfig", "Agent"} {
		p := s.Proofs[i]
		uid, generation := t.ModelConfigUID, t.ModelConfigGeneration
		if kind == "Agent" {
			uid, generation = t.AgentUID, t.AgentGeneration
		}
		if !kagentEvaluationResource(p) || p.Kind != kind || p.Name != t.Agent || p.Namespace != t.Namespace || p.UID != uid || p.Generation != generation || !verificationDigest(p.SpecDigest) || p.Key != "" || !observe(p) {
			return false
		}
	}
	for i, kind := range []string{"Deployment", "Service", "ServiceAccount", "Secret"} {
		p := s.Workload[i]
		if !kagentEvaluationResource(p) || p.Kind != kind || p.Name != t.Agent || p.Namespace != t.Namespace || p.ResourceVersion == "" || p.Key != "" || !observe(p) {
			return false
		}
		if kind == "Deployment" {
			if p.Generation < 1 || !verificationDigest(p.SpecDigest) {
				return false
			}
		} else if p.Generation != 0 || p.SpecDigest != "" {
			return false
		}
	}
	// A model credential is a reference/key, never a value or credential hash.
	if s.Dependencies[0].Kind != "Secret" || s.Dependencies[0].Key == "" {
		return false
	}
	for _, p := range s.Dependencies {
		if !kagentEvaluationResource(p) || p.Namespace != t.Namespace || p.ResourceVersion == "" || p.Key != "" && scaffold.ValidateKagentSecretKey(p.Key) != nil {
			return false
		}
		switch p.Kind {
		case "Secret", "ConfigMap":
			if p.Generation != 0 || p.SpecDigest != "" {
				return false
			}
		case "RemoteMCPServer":
			if p.Generation < 1 || !verificationDigest(p.SpecDigest) || p.Key != "" {
				return false
			}
		default:
			return false
		}
		if !observe(p) {
			return false
		}
	}
	return true
}

func validKagentEvaluationExecution(c kagentEvaluationResult) bool {
	e := c.Execution
	if !safeKagentEvaluationID(e.RPCID) || !safeKagentEvaluationID(e.RequestMessageID) || !diffOptionalDigest(c.AnswerSHA256) {
		return false
	}
	if e.TaskID == "" || e.ContextID == "" || e.State == "" {
		return e.TaskID == "" && e.ContextID == "" && e.State == "" && c.Detail == evaluationExecutionUnavailable && c.AnswerSHA256 == ""
	}
	if !safeKagentEvaluationID(e.TaskID) || !safeKagentEvaluationID(e.ContextID) {
		return false
	}
	if c.Detail != "" && c.AnswerSHA256 != "" {
		return false
	}
	switch e.State {
	case "completed":
		if c.Detail == "" && c.AnswerSHA256 != "" {
			// An attributable answer can leave tools/judge unknown, but cannot
			// also claim that the answer used for its assertions is unavailable.
			for _, row := range c.Assertions {
				if row.Reason == "answer unavailable" {
					return false
				}
			}
			return true
		}
		return c.Detail == evaluationResultUnavailable || c.Detail == evaluationExecutionUnavailable
	case "failed", "canceled", "rejected":
		return (c.Detail == evaluationExecutionFailed || c.Detail == evaluationExecutionUnavailable) && c.AnswerSHA256 == ""
	case "submitted", "working", "input-required", "auth-required", "unknown":
		return c.Detail == evaluationExecutionUnavailable && c.AnswerSHA256 == ""
	default:
		return false
	}
}

func normalizeKagentEvaluation(r kagentEvaluationReceipt) (normalizedEvaluation, bool) {
	n, ok := evaluationDiffMetadata(r.SchemaVersion, r.RunID, r.Bundle, r.PortableDigest, r.CasesDigest, r.GitCommit, r.Result, r.FullCaseSet)
	t := r.Target
	if !ok || r.SchemaVersion != 2 || !verificationDigest(r.RenderedDigest) || !verificationDigest(r.DeploymentReceiptDigest) || t.Runtime != agentruntime.Kagent || t.RuntimeVersion != "v0.10.3" || t.Engine != "go" || !verificationIdentity(t.Context) || !safeKagentEvaluationID(t.ClusterUID) || scaffold.ValidateNamespace(t.Namespace) != nil || scaffold.ValidateObjectName(t.Agent) != nil || t.Agent != r.Bundle || !safeKagentEvaluationID(t.AgentUID) || !safeKagentEvaluationID(t.ModelConfigUID) || t.AgentGeneration < 1 || t.ModelConfigGeneration < 1 || !verificationIdentity(t.Model) || !validKagentEvaluationPlatform(t.Platform) || !validKagentEvaluationSnapshot(t) {
		return n, false
	}
	n.Identity = evaluationDiffIdentity{Runtime: string(t.Runtime), RuntimeVersion: t.RuntimeVersion, Engine: t.Engine, ClusterUID: t.ClusterUID, Namespace: t.Namespace, Agent: t.Agent, AgentUID: t.AgentUID, ModelConfigUID: t.ModelConfigUID, Model: t.Model, SourceCommit: t.Platform.SourceCommit, ControllerImage: t.Platform.ControllerImage, ADKImage: t.Platform.ADKImage}
	n.IdentityComplete = true
	rpc, messages, tasks, contexts := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range r.Cases {
		if !validKagentEvaluationExecution(c) || !addEvaluationDiffCase(&n, c.ID, c.CaseDigest, c.InputDigest, c.Verdict, c.Detail, c.Assertions) {
			return n, false
		}
		entry := n.Cases[c.ID]
		entry.IdentityComplete = n.IdentityComplete
		n.Cases[c.ID] = entry
		for _, id := range []struct {
			value string
			seen  map[string]bool
		}{{c.Execution.RPCID, rpc}, {c.Execution.RequestMessageID, messages}, {c.Execution.TaskID, tasks}, {c.Execution.ContextID, contexts}} {
			if id.value != "" {
				if id.seen[id.value] {
					return n, false
				}
				id.seen[id.value] = true
			}
		}
	}
	return n, evaluationDiffAggregate(n)
}

func kagentEvaluationLatestPath(bundle, context, namespace, clusterUID string) string {
	key := sha256.Sum256([]byte("kagent\x00v0.10.3\x00" + context + "\x00" + namespace + "\x00" + clusterUID))
	return filepath.Join(bundle, "receipts", fmt.Sprintf("eval-kagent-v0.10.3-%x.json", key))
}

// Validate the complete evidence before any filesystem mutation. The shared
// writer enforces serialized bounds, private modes, immutable archive first,
// and no-link latest updates; this boundary cannot publish an invalid receipt.
func writeKagentEvaluationReceipt(bundle string, r kagentEvaluationReceipt) error {
	if _, ok := normalizeKagentEvaluation(r); !ok {
		return fmt.Errorf("invalid kagent evaluation receipt")
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("invalid kagent evaluation receipt")
	}
	return writeEvaluationRunAndLatest(bundle, r.SchemaVersion, r.RunID, kagentEvaluationLatestPath(bundle, r.Target.Context, r.Target.Namespace, r.Target.ClusterUID), append(raw, '\n'))
}
