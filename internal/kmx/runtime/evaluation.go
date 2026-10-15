// Evaluation cases bind authored input and assertions through stable byte framing.
package runtime

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

// EvaluationCaseDir is the reference-export directory cases live in, and the
// logical path prefix each case file is framed under in the cases digest.
const EvaluationCaseDir = "eval"

// EvaluationMaxAssertions bounds the combined legacy and explicit case evidence.
const EvaluationMaxAssertions = 1000

// EvaluationCase is one authored case file: an id, an input and at least one
// legacy expected substring or explicit assertion. Unknown fields are refused,
// so a misspelled assertion can never silently stop being checked.
type EvaluationCase struct {
	ID             string                `yaml:"id"`
	Input          string                `yaml:"input"`
	ExpectContains []string              `yaml:"expectContains"`
	Assertions     []EvaluationAssertion `yaml:"assertions"`
	Runs           *int                  `yaml:"runs,omitempty"`
	MinPasses      *int                  `yaml:"minPasses,omitempty"`
}

// EvaluationMaxRuns bounds authored execution cost independently of receipt size.
const EvaluationMaxRuns = 100

// RunPolicy defaults to one run, or all declared runs passing when no threshold is authored.
func (c EvaluationCase) RunPolicy() (runs, minPasses int) {
	runs = 1
	if c.Runs != nil {
		runs = *c.Runs
	}
	minPasses = runs
	if c.MinPasses != nil {
		minPasses = *c.MinPasses
	}
	return
}

// EvaluationCaseFile is one case file's name inside EvaluationCaseDir and its
// exact bytes, as the cases digest frames them.
type EvaluationCaseFile struct {
	Name  string
	Bytes []byte
}

// evaluationCaseIDRE keeps an id safe to print and to use as a selector: it
// starts with a letter or digit and is at most 63 characters.
var evaluationCaseIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// ParseEvaluationCase strictly decodes exactly one YAML mapping into a case.
// Raw bytes are scanned for credential shapes
// before any gate can quote them back, and aliases, merge keys and repeated
// keys are refused rather than resolved.
func ParseEvaluationCase(data []byte) (EvaluationCase, error) {
	return parseEvaluationCase(data, false)
}

func parseEvaluationCase(data []byte, reference bool) (EvaluationCase, error) {
	if err := refuseEvaluationSecretShape(string(data)); err != nil {
		return EvaluationCase{}, err
	}
	if !utf8.Valid(data) {
		return EvaluationCase{}, fmt.Errorf("evaluation case must be valid UTF-8")
	}
	var root yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&root); err != nil {
		return EvaluationCase{}, fmt.Errorf("evaluation case is empty or not valid YAML: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return EvaluationCase{}, fmt.Errorf("evaluation case must contain exactly one YAML document")
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return EvaluationCase{}, fmt.Errorf("evaluation case must be a single YAML mapping")
	}
	if err := refusePortableDecodedSecretShapes(root.Content[0]); err != nil {
		return EvaluationCase{}, err
	}
	if err := rejectPortableKeyHazards(root.Content[0], ""); err != nil {
		return EvaluationCase{}, fmt.Errorf("evaluation case: %w", err)
	}
	// yaml.v3 skips null sequence entries before invoking element unmarshallers.
	// Refuse them explicitly so a malformed authored assertion cannot disappear.
	mapping := root.Content[0]
	if reference {
		for i := 0; i < len(mapping.Content); i += 2 {
			key, value := mapping.Content[i].Value, mapping.Content[i+1]
			switch key {
			case "id":
				if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
					return EvaluationCase{}, fmt.Errorf("reference assertions id must be a string")
				}
			case "expectContains", "assertions":
				if value.Kind != yaml.SequenceNode {
					return EvaluationCase{}, fmt.Errorf("reference assertions must be sequences")
				}
				if key == "expectContains" {
					for _, entry := range value.Content {
						if entry.Kind != yaml.ScalarNode || entry.Tag != "!!str" {
							return EvaluationCase{}, fmt.Errorf("reference expectation must be a string")
						}
					}
				}
			default:
				return EvaluationCase{}, fmt.Errorf("reference assertions contain an unsupported field")
			}
		}
	}
	for i := 0; i < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i].Value, mapping.Content[i+1]
		if (key == "runs" || key == "minPasses") && (value.Kind != yaml.ScalarNode || value.Tag != "!!int") {
			return EvaluationCase{}, fmt.Errorf("evaluation case field %s must be an integer", key)
		}
		if mapping.Content[i].Value != "assertions" || mapping.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		for _, assertion := range mapping.Content[i+1].Content {
			if assertion.Kind != yaml.MappingNode {
				return EvaluationCase{}, fmt.Errorf("evaluation assertion must be a mapping")
			}
		}
	}
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	var c EvaluationCase
	if err := strict.Decode(&c); err != nil {
		return EvaluationCase{}, fmt.Errorf("evaluation case: %w", err)
	}
	if err := validateEvaluationCase(c, reference); err != nil {
		return EvaluationCase{}, err
	}
	return c, nil
}

// ValidateEvaluationCase checks a case that did not come from YAML.
func ValidateEvaluationCase(c EvaluationCase) error {
	for _, value := range append([]string{c.ID, c.Input}, c.ExpectContains...) {
		if !utf8.ValidString(value) {
			return fmt.Errorf("evaluation case must be valid UTF-8")
		}
		if err := refuseEvaluationSecretShape(value); err != nil {
			return err
		}
	}
	return validateEvaluationCase(c, false)
}

func validateEvaluationCase(c EvaluationCase, reference bool) error {
	if c.MinPasses != nil && c.Runs == nil {
		return fmt.Errorf("evaluation minPasses requires runs")
	}
	runs, passes := c.RunPolicy()
	if runs < 1 || runs > EvaluationMaxRuns || passes < 1 || passes > runs {
		return fmt.Errorf("evaluation policy requires 1 <= minPasses <= runs <= %d", EvaluationMaxRuns)
	}
	if !evaluationCaseIDRE.MatchString(c.ID) {
		return fmt.Errorf("evaluation case id must be 1-63 letters, digits, '.', '_' or '-', starting with a letter or digit")
	}
	if !reference && strings.TrimSpace(c.Input) == "" {
		return fmt.Errorf("evaluation case %s: input is required", c.ID)
	}
	if len(c.ExpectContains) == 0 && len(c.Assertions) == 0 {
		return fmt.Errorf("evaluation case %s: at least one expectContains string or assertion is required", c.ID)
	}
	if len(c.ExpectContains)+len(c.Assertions) > EvaluationMaxAssertions {
		return fmt.Errorf("evaluation case %s: at most %d assertions are allowed", c.ID, EvaluationMaxAssertions)
	}
	for i, expected := range c.ExpectContains {
		if strings.TrimSpace(expected) == "" || strings.IndexFunc(expected, unicode.IsControl) >= 0 {
			return fmt.Errorf("evaluation case %s: expectContains[%d] must be a nonblank single-line string", c.ID, i)
		}
	}
	if err := ValidateEvaluationAssertions(c.Assertions); err != nil {
		return fmt.Errorf("evaluation case %s: %w", c.ID, err)
	}
	return nil
}

// EvaluationCasesDigest identifies a set of case files: each file's exact
// bytes framed under eval/<name>, in name order, hashed once. Adding,
// removing, renaming or editing any case changes it; the order files were
// listed in does not.
func EvaluationCasesDigest(files []EvaluationCaseFile) string {
	sorted := append([]EvaluationCaseFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var frames bytes.Buffer
	for _, file := range sorted {
		frames.Write(frameEntry(EvaluationCaseDir+"/"+file.Name, file.Bytes))
	}
	return sha256Hex(frames.Bytes())
}

// MatchExpectations partitions expected into the strings that occur in
// answer and those that do not. Matching is exact and case-sensitive: an
// evaluation asserts what the answer literally contains.
func MatchExpectations(answer string, expected []string) (matched, missing []string) {
	for _, want := range expected {
		if strings.Contains(answer, want) {
			matched = append(matched, want)
		} else {
			missing = append(missing, want)
		}
	}
	return matched, missing
}

// EvaluationAnswerDigest is the SHA-256 of an answer's exact bytes: the only
// record of an answer a receipt keeps.
func EvaluationAnswerDigest(answer string) string {
	return sha256Hex([]byte(answer))
}

func refuseEvaluationSecretShape(value string) error {
	if shape := secretshapes.Match(value); shape != nil {
		return fmt.Errorf("refusing evaluation case containing something shaped like %s; cases are committed beside the bundle and must never carry a credential", shape.What)
	}
	return nil
}
