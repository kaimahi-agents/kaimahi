// Session references bind authored evidence to a source prefix without storing
// source inputs or answers. Stores are logical identities, never dial targets.
package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

const SessionReferencePolicy = "single-turn-text-chat-v1"

// SessionReferenceHarnessVersion is a compatibility label for the pinned chat
// semantics, not an attestation of the source host's implementation.
const SessionReferenceHarnessVersion = "v0.1.3-0.20261008190619-e680ea10d3b4"

const sessionReferenceMaxBytes = 1 << 20
const sessionReferenceMaxCases = 1000

var errSessionReference = errors.New("invalid session reference contract")

type SessionReference struct {
	Store          string `json:"store"`
	SessionUID     string `json:"sessionUID"`
	Harness        string `json:"harness"`
	HarnessVersion string `json:"harnessVersion"`
	Model          string `json:"model"`
	InputSeq       int64  `json:"inputSeq"`
	PrefixSeq      int64  `json:"prefixSeq"`
	PrefixHash     string `json:"prefixHash"`
	InputDigest    string `json:"inputDigest"`
	AnswerDigest   string `json:"answerDigest"`
	ConfigDigest   string `json:"configDigest"`
}

type SessionReferenceCase struct {
	ID               string                `json:"id"`
	Source           SessionReference      `json:"source"`
	ExpectContains   []string              `json:"expectContains,omitempty"`
	Assertions       []EvaluationAssertion `json:"assertions,omitempty"`
	AssertionsDigest string                `json:"assertionsDigest"`
}

type SessionReferenceManifest struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Policy        string                 `json:"policy"`
	Cases         []SessionReferenceCase `json:"cases"`
}

// ParseSessionReferenceAssertions applies the authored case parser's rules but
// refuses input even when empty or null. Diagnostics never quote authored text.
func ParseSessionReferenceAssertions(data []byte) (EvaluationCase, error) {
	if len(data) > sessionReferenceMaxBytes {
		return EvaluationCase{}, errSessionReference
	}
	c, err := parseEvaluationCase(data, true)
	if err != nil {
		return EvaluationCase{}, errSessionReference
	}
	return c, nil
}

// NewSessionReferenceCase makes batching and append derive identical case IDs.
// Assertion edits retain source identity but change the bound evidence digest.
func NewSessionReferenceCase(authored EvaluationCase, source SessionReference) (SessionReferenceCase, error) {
	if authored.Input != "" || !validSessionReference(source) {
		return SessionReferenceCase{}, errSessionReference
	}
	// Round-tripping through the strict authored parser also validates callers
	// constructing cases directly, including credentials and duplicate IDs.
	data, err := yaml.Marshal(struct {
		ID             string                `yaml:"id"`
		ExpectContains []string              `yaml:"expectContains,omitempty"`
		Assertions     []EvaluationAssertion `yaml:"assertions,omitempty"`
	}{authored.ID, authored.ExpectContains, authored.Assertions})
	if err != nil {
		return SessionReferenceCase{}, errSessionReference
	}
	checked, err := ParseSessionReferenceAssertions(data)
	if err != nil {
		return SessionReferenceCase{}, err
	}
	id := checked.ID
	if len(id) > 50 {
		id = id[:50]
	}
	// Length framing avoids collisions between store/UID boundary choices.
	sourceBytes := append(frameEntry("store", []byte(source.Store)), frameEntry("sessionUID", []byte(source.SessionUID))...)
	c := SessionReferenceCase{ID: id + "-" + sha256Hex(sourceBytes)[:12], Source: source, ExpectContains: checked.ExpectContains, Assertions: checked.Assertions}
	c.AssertionsDigest = sessionReferenceAssertionsDigest(c)
	return c, nil
}

func ParseSessionReferenceManifest(data []byte) (SessionReferenceManifest, error) {
	if len(data) == 0 || len(data) > sessionReferenceMaxBytes || !utf8.Valid(data) {
		return SessionReferenceManifest{}, errSessionReference
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if _, err := strictReferenceJSON(decoder, reflect.TypeFor[SessionReferenceManifest](), 0); err != nil {
		return SessionReferenceManifest{}, errSessionReference
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return SessionReferenceManifest{}, errSessionReference
	}
	var m SessionReferenceManifest
	if err := json.Unmarshal(data, &m); err != nil || !validSessionReferenceManifest(m) {
		return SessionReferenceManifest{}, errSessionReference
	}
	return m, nil
}

// MarshalSessionReferenceManifest returns compact deterministic struct JSON.
// Receipt case/set digests wrap each case with the manifest policy as
// deterministic JSON; they do not hash the case alone.
func MarshalSessionReferenceManifest(m SessionReferenceManifest) ([]byte, error) {
	if !validSessionReferenceManifest(m) {
		return nil, errSessionReference
	}
	data, err := json.Marshal(m)
	if err != nil || len(data) > sessionReferenceMaxBytes {
		return nil, errSessionReference
	}
	return data, nil
}

func validSessionReferenceManifest(m SessionReferenceManifest) bool {
	if m.SchemaVersion != 1 || m.Policy != SessionReferencePolicy || len(m.Cases) == 0 || len(m.Cases) > sessionReferenceMaxCases {
		return false
	}
	ids := make(map[string]bool, len(m.Cases))
	sources := make(map[[2]string]bool, len(m.Cases))
	for _, c := range m.Cases {
		key := [2]string{c.Source.Store, c.Source.SessionUID}
		if !evaluationCaseIDRE.MatchString(c.ID) || ids[c.ID] || sources[key] || !validSessionReference(c.Source) {
			return false
		}
		ids[c.ID], sources[key] = true, true
		authored := EvaluationCase{ID: c.ID, ExpectContains: c.ExpectContains, Assertions: c.Assertions}
		checked, err := NewSessionReferenceCase(authored, c.Source)
		if err != nil || c.AssertionsDigest != checked.AssertionsDigest {
			return false
		}
	}
	return true
}

func validSessionReference(s SessionReference) bool {
	for _, v := range []string{s.Store, s.SessionUID, s.Harness, s.HarnessVersion, s.Model} {
		if !sessionReferenceIdentity(v) {
			return false
		}
	}
	for _, v := range []string{s.PrefixHash, s.InputDigest, s.AnswerDigest, s.ConfigDigest} {
		if !sessionReferenceDigest(v) {
			return false
		}
	}
	return s.InputSeq == 2 && s.PrefixSeq == 5 && s.Harness == "chat" && s.HarnessVersion == SessionReferenceHarnessVersion
}
func sessionReferenceIdentity(v string) bool {
	if len(v) == 0 || len(v) > 256 || secretshapes.Match(v) != nil {
		return false
	}
	for _, r := range v {
		if r < '!' || r > '~' {
			return false
		}
	}
	return true
}
func sessionReferenceDigest(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, r := range v {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func sessionReferenceAssertionsDigest(c SessionReferenceCase) string {
	// Expanding legacy assertions keeps their ordinal IDs and binds every authored
	// ID as well as its definition (including judge evaluator compatibility).
	var frames []byte
	for _, a := range EvaluationCaseAssertions(EvaluationCase{ExpectContains: c.ExpectContains, Assertions: c.Assertions}) {
		frames = append(frames, frameEntry("id", []byte(a.ID))...)
		frames = append(frames, frameEntry("definition", []byte(EvaluationAssertionDigest(a)))...)
	}
	return sha256Hex(frames)
}

// Decode with exact field spelling/presence and no duplicate/null values before
// Go's decoder can silently accept aliases, duplicate keys or zero values.
func strictReferenceJSON(d *json.Decoder, t reflect.Type, depth int) (any, error) {
	if depth > 16 {
		return nil, errSessionReference
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	token, err := d.Token()
	if err != nil || token == nil {
		return nil, errSessionReference
	}
	switch t.Kind() {
	case reflect.Struct:
		if token != json.Delim('{') {
			return nil, errSessionReference
		}
		fields := make(map[string]reflect.StructField)
		for i := range t.NumField() {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			fields[name] = f
		}
		seen := make(map[string]bool)
		kind := ""
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, errSessionReference
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return nil, errSessionReference
			}
			field, ok := fields[name]
			if !ok {
				return nil, errSessionReference
			}
			seen[name] = true
			value, err := strictReferenceJSON(d, field.Type, depth+1)
			if err != nil {
				return nil, err
			}
			if name == "type" {
				kind, _ = value.(string)
			}
		}
		if end, err := d.Token(); err != nil || end != json.Delim('}') {
			return nil, errSessionReference
		}
		for name, field := range fields {
			if !seen[name] && !strings.Contains(field.Tag.Get("json"), ",omitempty") {
				return nil, errSessionReference
			}
		}
		if t == reflect.TypeFor[EvaluationAssertion]() {
			for _, name := range []string{"value", "pattern", "tool", "rubric", "minScore", "judgeModel", "judgeVersion"} {
				allowed := name == evaluationAssertionOperandField(kind)
				if kind == "llmJudge" {
					allowed = name == "rubric" || name == "minScore" || name == "judgeModel" || name == "judgeVersion"
				}
				if seen[name] && !allowed {
					return nil, errSessionReference
				}
			}
		}
	case reflect.Slice:
		if token != json.Delim('[') {
			return nil, errSessionReference
		}
		count := 0
		for d.More() {
			count++
			if count > sessionReferenceMaxCases {
				return nil, errSessionReference
			}
			if _, err := strictReferenceJSON(d, t.Elem(), depth+1); err != nil {
				return nil, err
			}
		}
		if end, err := d.Token(); err != nil || end != json.Delim(']') {
			return nil, errSessionReference
		}
	case reflect.String:
		if _, ok := token.(string); !ok {
			return nil, errSessionReference
		}
		return token, nil
	case reflect.Int, reflect.Int64:
		n, ok := token.(json.Number)
		if !ok {
			return nil, errSessionReference
		}
		if _, err := n.Int64(); err != nil {
			return nil, errSessionReference
		}
	default:
		return nil, errSessionReference
	}
	return nil, nil
}
