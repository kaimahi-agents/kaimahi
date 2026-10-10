// Typed evaluation assertions keep authored operands separate from the
// payload-free results persisted by callers.
package runtime

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// EvaluationAssertion is an authored definition, never a receipt row.
type EvaluationAssertion struct {
	ID      string `yaml:"id" json:"id"`
	Type    string `yaml:"type" json:"type"`
	Value   string `yaml:"value,omitempty" json:"value,omitempty"`
	Pattern string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Tool    string `yaml:"tool,omitempty" json:"tool,omitempty"`
}

// EvaluationAssertionResult carries identity and outcome, but no operands.
type EvaluationAssertionResult struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"`
	DefinitionDigest string            `json:"definitionDigest"`
	Verdict          EvaluationVerdict `json:"verdict"`
	Reason           string            `json:"reason"`
}

// EvaluationEvidence exists only in memory. Availability is independent of
// content: a known empty answer is evidence, an unread answer is not. Tool
// capability must be declared by the runtime, never inferred from ToolCalls.
type EvaluationEvidence struct {
	Answer               string
	AnswerAvailable      bool
	ToolSupported        bool
	ToolEvidenceComplete bool
	ToolCalls            map[string]int
}

// UnmarshalYAML enforces field presence as well as values: even an empty or
// null operand for another assertion type must not be silently discarded.
func (a *EvaluationAssertion) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("evaluation assertion must be a mapping")
	}
	fields := make(map[string]bool, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		switch key {
		case "id", "type", "value", "pattern", "tool":
		default:
			return fmt.Errorf("evaluation assertion: field %s not found", key)
		}
		if fields[key] {
			return fmt.Errorf("evaluation assertion: duplicate key %s", key)
		}
		fields[key] = true
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return fmt.Errorf("evaluation assertion field %s must be a string", key)
		}
	}
	type plain EvaluationAssertion
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	operand := evaluationAssertionOperandField(decoded.Type)
	if operand == "" {
		return fmt.Errorf("unsupported assertion type")
	}
	for _, field := range []string{"value", "pattern", "tool"} {
		if field != operand && fields[field] {
			return fmt.Errorf("evaluation assertion: field %s is not allowed for this type", field)
		}
	}
	*a = EvaluationAssertion(decoded)
	return nil
}

// EvaluationCaseAssertions expands legacy expectations before explicit assertions.
// Each ordinal remains distinct even when authored expectations repeat.
func EvaluationCaseAssertions(c EvaluationCase) []EvaluationAssertion {
	assertions := make([]EvaluationAssertion, 0, len(c.ExpectContains)+len(c.Assertions))
	for i, value := range c.ExpectContains {
		assertions = append(assertions, EvaluationAssertion{
			ID: fmt.Sprintf("expectContains.%d", i), Type: "contains", Value: value,
		})
	}
	return append(assertions, c.Assertions...)
}

// EvaluateAssertions evaluates validated definitions and ephemeral evidence
// into payload-free rows. Callers combine these with execution status; a known
// execution failure must never be replaced by an assertion pass.
func EvaluateAssertions(c EvaluationCase, evidence EvaluationEvidence) []EvaluationAssertionResult {
	assertions := EvaluationCaseAssertions(c)
	rows := make([]EvaluationAssertionResult, 0, len(assertions))
	for _, a := range assertions {
		row := EvaluationAssertionResult{
			ID: a.ID, Type: a.Type, DefinitionDigest: EvaluationAssertionDigest(a),
			Verdict: EvaluationUnknown, Reason: "unsupported assertion type",
		}
		var matched bool
		switch a.Type {
		case "contains", "notContains", "regex":
			if !evidence.AnswerAvailable {
				row.Reason = "answer unavailable"
				rows = append(rows, row)
				continue
			}
			switch a.Type {
			case "contains":
				matched = strings.Contains(evidence.Answer, a.Value)
			case "notContains":
				matched = !strings.Contains(evidence.Answer, a.Value)
			case "regex":
				re, err := regexp.Compile(a.Pattern)
				if err != nil {
					row.Reason = "invalid assertion definition"
					rows = append(rows, row)
					continue
				}
				matched = re.MatchString(evidence.Answer)
			}
		case "toolCalled", "toolNotCalled":
			if !evidence.ToolSupported {
				row.Reason = "runtime does not support tools"
				rows = append(rows, row)
				continue
			}
			if !evidence.ToolEvidenceComplete || evidence.ToolCalls == nil {
				row.Reason = "tool evidence unavailable"
				rows = append(rows, row)
				continue
			}
			matched = evidence.ToolCalls[a.Tool] > 0
			if a.Type == "toolNotCalled" {
				matched = !matched
			}
		default:
			rows = append(rows, row)
			continue
		}
		row.Verdict, row.Reason = EvaluationFail, "assertion not satisfied"
		if matched {
			row.Verdict, row.Reason = EvaluationPass, "assertion satisfied"
		}
		rows = append(rows, row)
	}
	return rows
}

// EvaluationAssertionsVerdict combines assertion outcomes without a vacuous pass.
func EvaluationAssertionsVerdict(rows []EvaluationAssertionResult) EvaluationVerdict {
	verdict := EvaluationPass
	if len(rows) == 0 {
		return EvaluationUnknown
	}
	for _, row := range rows {
		if row.Verdict == EvaluationFail {
			return EvaluationFail
		}
		if row.Verdict != EvaluationPass {
			verdict = EvaluationUnknown
		}
	}
	return verdict
}

// EvaluationAssertionDigest identifies a definition independently of its ID.
// Type and operand are length-framed separately to avoid ambiguous boundaries.
func EvaluationAssertionDigest(a EvaluationAssertion) string {
	var operand string
	switch evaluationAssertionOperandField(a.Type) {
	case "value":
		operand = a.Value
	case "pattern":
		operand = a.Pattern
	case "tool":
		operand = a.Tool
	}
	frames := frameEntry("type", []byte(a.Type))
	frames = append(frames, frameEntry("operand", []byte(operand))...)
	return sha256Hex(frames)
}

// EvaluationInputDigest hashes an input's exact bytes.
func EvaluationInputDigest(input string) string { return sha256Hex([]byte(input)) }

// EvaluationCaseDigest hashes an authored case's exact bytes, not reserialized YAML.
func EvaluationCaseDigest(data []byte) string { return sha256Hex(data) }

func evaluationAssertionOperandField(kind string) string {
	switch kind {
	case "contains", "notContains":
		return "value"
	case "regex":
		return "pattern"
	case "toolCalled", "toolNotCalled":
		return "tool"
	default:
		return ""
	}
}

// ValidateEvaluationAssertions validates explicit definitions without requiring
// a full case, for adapters receiving EvaluationRequest.Assertions. An empty
// slice is valid here; the request/case boundary requires at least one explicit
// assertion or legacy expectation.
func ValidateEvaluationAssertions(assertions []EvaluationAssertion) error {
	seen := make(map[string]bool, len(assertions))
	for _, a := range assertions {
		if !evaluationCaseIDRE.MatchString(a.ID) {
			return fmt.Errorf("evaluation assertion id must be 1-63 safe characters, starting with a letter or digit")
		}
		if strings.HasPrefix(a.ID, "expectContains.") {
			return fmt.Errorf("evaluation assertion id uses the reserved expectContains.* namespace")
		}
		if seen[a.ID] {
			return fmt.Errorf("duplicate assertion id %s", a.ID)
		}
		seen[a.ID] = true
		if err := validateEvaluationAssertionOperand(a); err != nil {
			return err
		}
	}
	return nil
}

func validateEvaluationAssertionOperand(a EvaluationAssertion) error {
	for _, value := range []string{a.ID, a.Type, a.Value, a.Pattern, a.Tool} {
		if !utf8.ValidString(value) {
			return fmt.Errorf("evaluation assertion must be valid UTF-8")
		}
		if err := refuseEvaluationSecretShape(value); err != nil {
			return err
		}
	}
	switch a.Type {
	case "contains", "notContains":
		if a.Pattern != "" || a.Tool != "" {
			return fmt.Errorf("evaluation assertion only allows the value field")
		}
		if strings.TrimSpace(a.Value) == "" || strings.IndexFunc(a.Value, unicode.IsControl) >= 0 {
			return fmt.Errorf("evaluation assertion value must be a nonblank single-line string")
		}
	case "regex":
		if a.Value != "" || a.Tool != "" {
			return fmt.Errorf("evaluation assertion only allows the pattern field")
		}
		if strings.TrimSpace(a.Pattern) == "" || len(a.Pattern) > 1024 {
			return fmt.Errorf("evaluation assertion pattern must be nonblank and at most 1024 UTF-8 bytes")
		}
		if _, err := regexp.Compile(a.Pattern); err != nil {
			// regexp errors echo the pattern; keep authored operands out of diagnostics.
			return fmt.Errorf("evaluation assertion pattern must be valid Go RE2")
		}
	case "toolCalled", "toolNotCalled":
		if a.Value != "" || a.Pattern != "" {
			return fmt.Errorf("evaluation assertion only allows the tool field")
		}
		if strings.TrimSpace(a.Tool) == "" || strings.IndexFunc(a.Tool, unicode.IsControl) >= 0 {
			return fmt.Errorf("evaluation assertion tool must be a nonblank single-line string")
		}
	default:
		return fmt.Errorf("unsupported assertion type")
	}
	return nil
}
