package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

// A gate identity is independent of the operator's kubeconfig context label.
type bundleGateTarget struct {
	ClusterUID string `yaml:"clusterUID"`
	Namespace  string `yaml:"namespace"`
}

type bundleGateRule struct {
	Destination bundleGateTarget `yaml:"destination"`
	Evaluated   bundleGateTarget `yaml:"evaluated"`
}

type bundleLiftPolicy struct {
	Rules []bundleGateRule `yaml:"rules"`
}

func loadBundleLiftPolicy(bundle string) (bundleLiftPolicy, error) {
	path := filepath.Join(bundle, "lift-policy.yaml")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return bundleLiftPolicy{}, nil
	}
	if err != nil {
		return bundleLiftPolicy{}, fmt.Errorf("inspect lift policy: %w", err)
	}
	if !info.Mode().IsRegular() {
		return bundleLiftPolicy{}, fmt.Errorf("lift-policy.yaml must be a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return bundleLiftPolicy{}, fmt.Errorf("read lift policy: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var policy bundleLiftPolicy
	if err := decoder.Decode(&policy); err != nil {
		return bundleLiftPolicy{}, fmt.Errorf("invalid lift-policy.yaml: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return bundleLiftPolicy{}, fmt.Errorf("lift-policy.yaml must have exactly one document")
	}
	if len(policy.Rules) == 0 {
		return bundleLiftPolicy{}, fmt.Errorf("lift-policy.yaml requires at least one rule")
	}
	seen := map[bundleGateTarget]bool{}
	for _, rule := range policy.Rules {
		for _, target := range []bundleGateTarget{rule.Destination, rule.Evaluated} {
			if target.ClusterUID == "" || target.Namespace == "" {
				return bundleLiftPolicy{}, fmt.Errorf("lift policy targets require clusterUID and namespace")
			}
			if scaffold.ValidateNamespace(target.Namespace) != nil {
				return bundleLiftPolicy{}, fmt.Errorf("invalid lift policy namespace %q", target.Namespace)
			}
		}
		if rule.Destination == rule.Evaluated {
			return bundleLiftPolicy{}, fmt.Errorf("lift policy destination and evaluated target must not be the same target")
		}
		if seen[rule.Destination] {
			return bundleLiftPolicy{}, fmt.Errorf("duplicate lift policy destination cluster UID %s namespace %s", rule.Destination.ClusterUID, rule.Destination.Namespace)
		}
		seen[rule.Destination] = true
	}
	return policy, nil
}

type bundleGateEvidence struct {
	Receipt bundleEvaluationReceipt
	File    string
}

// Read local evidence by the identities inside the receipts, never by their
// context-derived filenames. Old receipts without a cluster UID cannot pass.
func readBundleGateEvidence(bundle string) ([]bundleGateEvidence, error) {
	entries, err := os.ReadDir(filepath.Join(bundle, "receipts"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read evaluation receipts: %w", err)
	}
	var evidence []bundleGateEvidence
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "eval-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("evaluation receipt %s must be a regular file", entry.Name())
		}
		raw, err := os.ReadFile(filepath.Join(bundle, "receipts", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read evaluation receipt %s: %w", entry.Name(), err)
		}
		var receipt bundleEvaluationReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			return nil, fmt.Errorf("invalid evaluation receipt %s: %w", entry.Name(), err)
		}
		evidence = append(evidence, bundleGateEvidence{Receipt: receipt, File: entry.Name()})
	}
	return evidence, nil
}

// bundleGateCondition returns an empty condition only when every receipt for
// the selected identity agrees on the current revision and records a pass.
func bundleGateCondition(evidence []bundleGateEvidence, target bundleGateTarget, name, digest, casesDigest string, cases []bundleEvaluationCase) string {
	var matching []bundleGateEvidence
	for _, item := range evidence {
		r := item.Receipt
		if r.Target.ClusterUID == target.ClusterUID && r.Target.Namespace == target.Namespace {
			matching = append(matching, item)
		}
	}
	if len(matching) == 0 {
		return fmt.Sprintf("no evaluation receipt for cluster UID %s namespace %s", target.ClusterUID, target.Namespace)
	}
	if len(matching) > 1 {
		first := matching[0].Receipt
		for _, item := range matching[1:] {
			if item.Receipt.PortableDigest != first.PortableDigest || item.Receipt.CasesDigest != first.CasesDigest || item.Receipt.Target.AgentUID != first.Target.AgentUID {
				var labels []string
				for _, match := range matching {
					labels = append(labels, fmt.Sprintf("%s (%s)", match.Receipt.Target.Context, match.File))
				}
				slices.Sort(labels)
				return "evaluation receipts disagree on portable digest, case-set digest or Agent UID: " + strings.Join(labels, ", ")
			}
		}
	}
	for _, item := range matching {
		r := item.Receipt
		switch {
		case r.Target.Runtime != "orka" || r.Bundle != name || r.Target.Agent != name || r.Target.AgentUID == "":
			return fmt.Sprintf("evaluation receipt %s has invalid Agent identity", item.File)
		case r.PortableDigest != digest:
			return fmt.Sprintf("evaluation receipt %s has stale portable digest", item.File)
		case r.CasesDigest != casesDigest:
			return fmt.Sprintf("evaluation receipt %s has different case-set digest", item.File)
		case !r.FullCaseSet:
			return fmt.Sprintf("evaluation receipt %s is a single-case run, not the full case set", item.File)
		case r.Result != "pass":
			return fmt.Sprintf("evaluation receipt %s result is %s, not pass", item.File, r.Result)
		case !bundleCaseResultsPass(r.Cases, cases):
			return fmt.Sprintf("evaluation receipt %s has incomplete or non-passing case results", item.File)
		}
	}
	return ""
}

func bundleCaseResultsPass(results []bundleEvaluationResult, cases []bundleEvaluationCase) bool {
	if len(results) != len(cases) || len(cases) == 0 {
		return false
	}
	expected := make(map[string]bool, len(cases))
	for _, c := range cases {
		expected[c.Case.ID] = true
	}
	for _, result := range results {
		if !expected[result.ID] || result.Verdict != string(agentruntime.EvaluationPass) {
			return false
		}
		delete(expected, result.ID)
	}
	return len(expected) == 0
}

func evaluateBundleLiftGate(bundle, name, digest string, destination bundleGateTarget, sourceContext string) (required bool, condition string) {
	policy, err := loadBundleLiftPolicy(bundle)
	if err != nil {
		return true, err.Error()
	}
	var sources []bundleGateTarget
	for _, rule := range policy.Rules {
		if rule.Destination == destination {
			sources = append(sources, rule.Evaluated)
		}
	}
	if sourceContext == "" && len(sources) == 0 {
		return false, ""
	}
	evidence, err := readBundleGateEvidence(bundle)
	if err != nil {
		return true, err.Error()
	}
	if sourceContext != "" {
		matches := map[bundleGateTarget]bool{}
		for _, item := range evidence {
			if item.Receipt.Target.Context == sourceContext && item.Receipt.Target.ClusterUID != "" && item.Receipt.Target.Namespace != "" {
				matches[bundleGateTarget{ClusterUID: item.Receipt.Target.ClusterUID, Namespace: item.Receipt.Target.Namespace}] = true
			}
		}
		if len(matches) == 0 {
			return true, fmt.Sprintf("no evaluation receipt for context label %s with a recorded cluster UID", sourceContext)
		}
		if len(matches) != 1 {
			return true, fmt.Sprintf("context label %s matches multiple evaluation target identities", sourceContext)
		}
		for source := range matches {
			sources = append(sources, source)
		}
	}
	cases, files, err := loadBundleEvaluationCases(bundle)
	if err != nil {
		return true, fmt.Sprintf("cannot read the full eval/ case set: %v", err)
	}
	casesDigest := agentruntime.EvaluationCasesDigest(files)
	for _, source := range sources {
		if condition := bundleGateCondition(evidence, source, name, digest, casesDigest, cases); condition != "" {
			return true, condition
		}
	}
	return true, ""
}
