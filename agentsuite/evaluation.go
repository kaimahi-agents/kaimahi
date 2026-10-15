package agentsuite

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const (
	maxEvaluationCases      = 128
	maxEvaluationAssertions = 1000
)

var evaluationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

func (v *validator) validateEvaluations() error {
	files := map[string]bool{}
	directories := map[string]bool{"evals": true}
	var errs []error
	for id, agent := range v.agents {
		if len(agent.Evals) > 1 {
			errs = append(errs, fmt.Errorf("agent %s evals permits at most one index", id))
			continue
		}
		if len(agent.Evals) == 0 {
			continue
		}
		prefix := "evals/" + id
		directories[prefix] = true
		directories[prefix+"/cases"] = true
		ref := agent.Evals[0]
		if ref.Path != prefix+"/index.json" {
			errs = append(errs, fmt.Errorf("agent %s eval index must be %s/index.json", id, prefix))
			continue
		}
		files[ref.Path] = true
		if err := v.validateEvaluationIndex(id, ref, files); err != nil {
			errs = append(errs, fmt.Errorf("agent %s evaluations: %w", id, err))
		}
	}
	for name, entry := range v.content.entries {
		if name != "evals" && !strings.HasPrefix(name, "evals/") {
			continue
		}
		if entry.Type == "directory" && directories[name] {
			continue
		}
		if entry.Type != "file" || !files[name] {
			errs = append(errs, fmt.Errorf("eval path %s is orphaned, unreferenced, or not a regular evaluation file", name))
		}
	}
	return errors.Join(errs...)
}

func (v *validator) evaluationDocument(ref FileRef, expectedMediaType string, out any) ([]byte, error) {
	if !IsValidDigest(ref.Descriptor.Digest) {
		return nil, errors.New("evaluation reference digest is invalid")
	}
	if ref.Descriptor.MediaType != expectedMediaType {
		return nil, fmt.Errorf("evaluation reference descriptor mediaType must be %s", expectedMediaType)
	}
	data, err := v.content.data(ref.Path)
	if err != nil {
		return nil, err
	}
	// Reject a size/digest-mismatched document before parsing it: a document
	// that cannot possibly match its descriptor should not receive full
	// strict-JSON decoding.
	if !descriptorSizeAndDigestMatch(data, ref.Descriptor) {
		return nil, errors.New("evaluation document digest mismatch")
	}
	if err := decodeStrict(data, out); err != nil {
		return nil, fmt.Errorf("evaluation JSON: %w", err)
	}
	if !matchesCanonicalDescriptor(data, ref.Descriptor) {
		return nil, errors.New("evaluation document digest mismatch")
	}
	return data, nil
}

func (v *validator) validateEvaluationIndex(agent string, ref FileRef, files map[string]bool) error {
	var index EvaluationIndex
	_, err := v.evaluationDocument(ref, MediaTypeEvaluationIndex, &index)
	if err != nil {
		return err
	}
	if index.SchemaVersion != SpecVersion || index.MediaType != MediaTypeEvaluationIndex {
		return errors.New("unsupported evaluation index schemaVersion or mediaType")
	}
	if index.Agent != agent {
		return errors.New("evaluation index agent does not match owning agent")
	}
	if index.Limits.MaxCases < 1 || index.Limits.MaxCases > maxEvaluationCases ||
		index.Limits.MaxCaseBytes < 1 || index.Limits.MaxCaseBytes > maxJSONBytes {
		return errors.New("evaluation limits require maxCases in 1..128 and maxCaseBytes in 1..4194304")
	}
	if len(index.Cases) == 0 || len(index.Cases) > index.Limits.MaxCases {
		return errors.New("evaluation case count exceeds limits or is empty")
	}
	ids := map[string]bool{}
	for _, caseRef := range index.Cases {
		prefix := "evals/" + agent + "/cases/"
		if !strings.HasPrefix(caseRef.Path, prefix) || !strings.HasSuffix(caseRef.Path, ".json") {
			return errors.New("evaluation case path must be inside the owning agent's cases directory")
		}
		if files[caseRef.Path] {
			return errors.New("duplicate evaluation case reference")
		}
		files[caseRef.Path] = true
		var c EvaluationCase
		data, err := v.evaluationDocument(caseRef, MediaTypeEvaluationCase, &c)
		if err != nil {
			return err
		}
		if len(data) > index.Limits.MaxCaseBytes {
			return errors.New("evaluation case exceeds maxCaseBytes")
		}
		if c.SchemaVersion != SpecVersion || c.MediaType != MediaTypeEvaluationCase {
			return errors.New("unsupported evaluation case schemaVersion or mediaType")
		}
		if !evaluationIDPattern.MatchString(c.ID) || ids[c.ID] {
			return errors.New("evaluation case id is invalid or duplicated")
		}
		ids[c.ID] = true
		if caseRef.Path != prefix+c.ID+".json" {
			return errors.New("evaluation case filename must match its id")
		}
		if err := validateEvaluationRunPolicy(c, data); err != nil {
			return err
		}
		if strings.TrimSpace(c.Input) == "" || len(c.Assertions) == 0 || len(c.Assertions) > maxEvaluationAssertions {
			return errors.New("evaluation case requires input and 1..1000 assertions")
		}
		var raw struct {
			Assertions []json.RawMessage `json:"assertions"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		assertionIDs := map[string]bool{}
		for i, assertion := range c.Assertions {
			if !evaluationIDPattern.MatchString(assertion.ID) || assertionIDs[assertion.ID] {
				return errors.New("evaluation assertion id is invalid or duplicated")
			}
			assertionIDs[assertion.ID] = true
			if err := validateEvaluationAssertion(assertion, raw.Assertions[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateEvaluationRunPolicy(c EvaluationCase, raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	runsRaw, hasRuns := fields["runs"]
	minPassesRaw, hasMinPasses := fields["minPasses"]
	if hasRuns && strings.TrimSpace(string(runsRaw)) == "null" ||
		hasMinPasses && strings.TrimSpace(string(minPassesRaw)) == "null" {
		return errors.New("evaluation runs and minPasses must not be null")
	}
	if !hasRuns {
		if hasMinPasses {
			return errors.New("evaluation minPasses requires runs")
		}
		return nil
	}
	if c.Runs == nil || *c.Runs < 1 {
		return errors.New("evaluation runs must be a positive integer")
	}
	if !hasMinPasses {
		return nil
	}
	if c.MinPasses == nil || *c.MinPasses < 1 || *c.MinPasses > *c.Runs {
		return errors.New("evaluation minPasses must be positive and no greater than runs")
	}
	return nil
}

func validateEvaluationAssertion(a EvaluationAssertion, raw []byte) error {
	required := []string{"id", "type"}
	allowed := map[string]bool{"id": true, "type": true, "critical": true}
	switch a.Type {
	case "contains", "notContains":
		required = append(required, "value")
		if !evaluationSingleLine(a.Value) {
			return errors.New("evaluation assertion value must be nonblank and single-line")
		}
	case "regex":
		required = append(required, "pattern")
		if strings.TrimSpace(a.Pattern) == "" || len(a.Pattern) > 1024 {
			return errors.New("evaluation regex must be nonblank and at most 1024 bytes")
		}
		if _, err := regexp.Compile(a.Pattern); err != nil {
			return errors.New("evaluation regex must use valid RE2 syntax")
		}
	case "toolCalled", "toolNotCalled":
		required = append(required, "tool")
		if !evaluationSingleLine(a.Tool) {
			return errors.New("evaluation tool must be nonblank and single-line")
		}
	case "llmJudge":
		required = append(required, "rubric", "minScore", "judgeModel", "judgeVersion")
		if strings.TrimSpace(a.Rubric) == "" || len(a.Rubric) > 4096 ||
			strings.IndexFunc(a.Rubric, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0 {
			return errors.New("evaluation rubric must be nonblank, at most 4096 bytes, and free of controls except newline and tab")
		}
		if a.MinScore == nil || *a.MinScore < 0 || *a.MinScore > 100 {
			return errors.New("evaluation minScore must be an integer in 0..100")
		}
		for _, value := range []string{a.JudgeModel, a.JudgeVersion} {
			if len(value) == 0 || len(value) > 256 || strings.TrimSpace(value) == "" ||
				strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r > 0x7e }) >= 0 {
				return errors.New("evaluation judge identity must be nonblank printable ASCII of at most 256 bytes")
			}
		}
	default:
		if !identifierPattern.MatchString(a.Type) || a.Critical == nil || *a.Critical {
			return errors.New("unknown evaluation assertion type requires explicit critical:false")
		}
		required = append(required, "value", "critical")
		if strings.TrimSpace(a.Value) == "" {
			return errors.New("non-critical evaluation assertion requires a nonblank value")
		}
	}
	for _, field := range required {
		allowed[field] = true
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return errors.New("evaluation assertion is missing a required field")
		}
	}
	for field, value := range fields {
		if !allowed[field] || strings.TrimSpace(string(value)) == "null" {
			return errors.New("evaluation assertion contains an inapplicable or null field")
		}
	}
	return nil
}

func evaluationSingleLine(value string) bool {
	return strings.TrimSpace(value) != "" && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	}) < 0
}
