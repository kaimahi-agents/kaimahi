package app

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	rt "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Operator-owned closed JSON, never packaged provenance. Pins the exact receipt
// and both full logical subjects; changed paths enumerate explicitly reviewed
// differences. Derived digests are consequences, not transition permissions.
type suitePromotionPolicy struct {
	SchemaVersion          int                `json:"schemaVersion"`
	Domain                 string             `json:"domain"`
	ReceiptDigest          string             `json:"receiptDigest"`
	RunID                  string             `json:"runID"`
	Evaluated              suiteNativeSubject `json:"evaluated"`
	Destination            suiteNativeSubject `json:"destination"`
	AllowedTransitionPaths []string           `json:"allowedTransitionPaths"`
}
type suiteGateDecision struct {
	ReceiptDigest         string
	PolicyDigest          string
	AuthorizedTransitions []string
	// Permission to promote is never proof of destination execution, even when
	// source and destination logical subjects happen to be identical.
	DestinationEvaluated bool
}

func decodeSuitePromotionPolicy(raw []byte) (suitePromotionPolicy, error) {
	var p suitePromotionPolicy
	if err := decodeSuiteJSON(raw, &p); err != nil {
		return suitePromotionPolicy{}, err
	}
	if err := validateSuitePromotionPolicyShape(p); err != nil {
		return suitePromotionPolicy{}, err
	}
	return p, nil
}
func validateSuitePromotionPolicyShape(p suitePromotionPolicy) error {
	if p.SchemaVersion != 1 || p.Domain != "suite-promotion" || !evaluationRunID(p.RunID) || !strings.HasPrefix(p.ReceiptDigest, "sha256:") || !verificationDigest(strings.TrimPrefix(p.ReceiptDigest, "sha256:")) || p.AllowedTransitionPaths == nil {
		return suiteEvidenceError("unsupported promotion policy")
	}
	seen := map[string]bool{}
	for _, path := range p.AllowedTransitionPaths {
		if !suiteReviewedPath(path) || seen[path] {
			return suiteEvidenceError("invalid reviewed transition path")
		}
		seen[path] = true
	}
	return nil
}

// Only live installation/reference/target identities can transition. Fixed
// model, renderer, execution pins, required images and derived digests cannot.
func suiteReviewedPath(path string) bool {
	switch path {
	case "target.context", "target.clusterUID", "target.namespace", "target.namespaceUID",
		"binding.provider.name", "binding.provider.namespace", "binding.provider.uid", "binding.provider.generation", "binding.provider.resourceVersion", "binding.provider.baseURL", "binding.provider.secretName", "binding.provider.secretKey",
		"binding.secret.name", "binding.secret.namespace", "binding.secret.uid", "binding.secret.resourceVersion", "binding.secret.key",
		"runtime.namespace", "runtime.controllerName", "runtime.controllerUID", "runtime.controllerGeneration", "runtime.controllerServiceAccount", "runtime.watchNamespace", "runtime.helm.instance", "runtime.workerAccount", "runtime.workerAccountUID":
		return true
	}
	return false
}
func gateSuitePromotion(s *workspace.NativeSuite, evaluated, destination suiteNativeSubject, r suiteEvaluationReceipt, p suitePromotionPolicy) (suiteGateDecision, error) {
	var refused suiteGateDecision
	raw, err := spec.Marshal(p)
	var shape suitePromotionPolicy
	if err != nil || decodeSuiteJSON(raw, &shape) != nil {
		return refused, suiteEvidenceError("promotion policy shape")
	}
	if err := validateSuitePromotionPolicyShape(p); err != nil {
		return refused, err
	}
	if err := validateSuiteEvidenceSubject(s, destination); err != nil {
		return refused, err
	}
	_, digest, err := canonicalSuiteEvaluationReceipt(s, evaluated, r)
	if err != nil {
		return refused, err
	}
	if !r.FullCaseSet || !r.PromotionEligible || r.Result != rt.EvaluationPass {
		return refused, suiteEvidenceError("receipt does not qualify")
	}
	if p.RunID != r.RunID || p.ReceiptDigest != digest {
		return refused, suiteEvidenceError("receipt pin mismatch")
	}
	if !reflect.DeepEqual(p.Evaluated, evaluated) || !reflect.DeepEqual(p.Destination, destination) {
		return refused, suiteEvidenceError("policy subject pin mismatch")
	}
	sourceLeaves := suiteTransitionLeaves(evaluated)
	destLeaves := suiteTransitionLeaves(destination)
	var changes []string
	for path, value := range sourceLeaves {
		if destLeaves[path] != value {
			if !suiteReviewedPath(path) {
				return refused, suiteEvidenceError("non-transitionable subject field")
			}
			changes = append(changes, path)
		}
	}
	slices.Sort(changes)
	allowed := append([]string{}, p.AllowedTransitionPaths...)
	slices.Sort(allowed)
	if !slices.Equal(changes, allowed) {
		return refused, suiteEvidenceError("unreviewed or unused transition")
	}
	policyDigest, err := spec.Digest(raw)
	if err != nil {
		return refused, suiteEvidenceError("policy digest")
	}
	return suiteGateDecision{ReceiptDigest: digest, PolicyDigest: policyDigest, AuthorizedTransitions: append([]string{}, changes...), DestinationEvaluated: false}, nil
}
func suiteTransitionLeaves(s suiteNativeSubject) map[string]string {
	leaves := map[string]string{}
	var scan func(string, any)
	scan = func(prefix string, value any) {
		if object, ok := value.(map[string]any); ok {
			for key, v := range object {
				scan(prefix+"."+key, v)
			}
			return
		}
		raw, _ := json.Marshal(value)
		leaves[prefix] = string(raw)
	}
	for key, value := range map[string]any{"target": s.Target, "binding": s.Binding, "runtime": s.Runtime} {
		raw, _ := json.Marshal(value)
		var object map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		_ = decoder.Decode(&object)
		scan(key, object)
	}
	return leaves
}
