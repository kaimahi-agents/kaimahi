// Evaluation cases. A case is a small authored file that tests a revision; it
// never defines one, so case bytes are deliberately outside the portable
// digest. What a set of cases was is identified separately, by
// EvaluationCasesDigest, which frames each file the same way the portable
// digest frames agent.yaml.
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

// EvaluationCaseDir is the bundle-relative directory cases live in, and the
// logical path prefix each case file is framed under in the cases digest.
const EvaluationCaseDir = "eval"

// EvaluationCase is one authored case file: exactly an id, an input and at
// least one expected substring. Unknown fields are refused, so a misspelled
// assertion can never silently stop being checked.
type EvaluationCase struct {
	ID             string   `yaml:"id"`
	Input          string   `yaml:"input"`
	ExpectContains []string `yaml:"expectContains"`
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
// Like ParsePortableAgent, the raw bytes are scanned for credential shapes
// before any gate can quote them back, and aliases, merge keys and repeated
// keys are refused rather than resolved.
func ParseEvaluationCase(data []byte) (EvaluationCase, error) {
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
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	var c EvaluationCase
	if err := strict.Decode(&c); err != nil {
		return EvaluationCase{}, fmt.Errorf("evaluation case: %w", err)
	}
	if !evaluationCaseIDRE.MatchString(c.ID) {
		return EvaluationCase{}, fmt.Errorf("evaluation case id must be 1-63 letters, digits, '.', '_' or '-', starting with a letter or digit")
	}
	if strings.TrimSpace(c.Input) == "" {
		return EvaluationCase{}, fmt.Errorf("evaluation case %s: input is required", c.ID)
	}
	if len(c.ExpectContains) == 0 {
		return EvaluationCase{}, fmt.Errorf("evaluation case %s: expectContains needs at least one string", c.ID)
	}
	for i, expected := range c.ExpectContains {
		if strings.TrimSpace(expected) == "" || strings.IndexFunc(expected, unicode.IsControl) >= 0 {
			return EvaluationCase{}, fmt.Errorf("evaluation case %s: expectContains[%d] must be a nonblank single-line string", c.ID, i)
		}
	}
	return c, nil
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
