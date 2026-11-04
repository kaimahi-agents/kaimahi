package agentfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const orkaExample = "---\nname: reader\ndescription: '  display text  '\nmodel: model/v1\nkmx:\n  schemaVersion: 1\n  runtime: orka\n  tools: []\n  skills: []\n---\n# Reader\n\n  Keep whitespace.  \n"

func orkaWith(extra string) []byte {
	return []byte(strings.Replace(orkaExample, "  tools: []", extra+"\n  tools: []", 1))
}

func TestOrkaSourcePreservesOriginal(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n"} {
		raw := []byte(strings.ReplaceAll(orkaExample, "\n", eol))
		original := bytes.Clone(raw)
		s, err := ParseOrkaSource("reader.agent.md", raw)
		if err != nil {
			t.Fatal(err)
		}
		if s.Name != "reader" || s.Description != "  display text  " || s.Model != "model/v1" || s.Protocol != "openai-compatible" || s.Instructions != "# Reader\n\n  Keep whitespace.  \n" {
			t.Fatalf("wrong projection: %#v", s)
		}
		if s.Revision != fmt.Sprintf("sha256:%x", sha256.Sum256(original)) || !bytes.Equal(s.Source, original) || len(s.Unsupported) != 0 {
			t.Fatal("lost source identity")
		}
		raw[0] = 'x'
		if !bytes.Equal(s.Source, original) {
			t.Fatal("source aliases caller bytes")
		}
	}
}

func TestOrkaSoulAndSkills(t *testing.T) {
	inline := "Be kind.\n"
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(inline)))
	for _, eol := range []string{"\n", "\r\n"} {
		raw := orkaWith("  soul:\n    inline: |\n      Be kind.\n    digest: " + digest)
		raw = []byte(strings.ReplaceAll(string(raw), "\n", eol))
		s, err := ParseOrkaSource("reader.agent.md", raw)
		if err != nil {
			t.Fatal(err)
		}
		if s.Soul == nil || s.Soul.Inline != inline || s.Soul.Digest != digest || s.Soul.ConfigMapRef != nil {
			t.Fatal("lost decoded Soul identity")
		}
	}
	source := string(orkaWith("  soul: {configMapRef: {name: persona, key: soul.md}, digest: " + digest + "}"))
	source = strings.Replace(source, "  skills: []", "  skills: [{name: research}, {configMapRef: {name: custom, key: SKILL.md}}]", 1)
	s, err := ParseOrkaSource("reader.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if s.Soul.ConfigMapRef.Name != "persona" || s.Soul.ConfigMapRef.Key != "soul.md" || len(s.Skills) != 2 || s.Skills[0].Name != "research" || s.Skills[1].ConfigMapRef.Key != "SKILL.md" {
		t.Fatal("reference/order changed")
	}
	for _, text := range []string{strings.Repeat("a", 8192), strings.Repeat("é", 4096)} {
		quoted, _ := json.Marshal(text)
		if _, err := ParseOrkaSource("reader.agent.md", orkaWith("  soul: {inline: "+string(quoted)+"}")); err != nil {
			t.Fatalf("valid byte boundary: %v", err)
		}
	}
}

func TestOrkaUnsupportedPreservesDeclarations(t *testing.T) {
	raw := strings.Replace(orkaExample, "model: model/v1", "model: model/v1\ncustom: {kept: value}", 1)
	raw = strings.Replace(raw, "  tools: []", "  future: unchanged\n  tools: [{kind: builtin, name: web_fetch}, {http: custom}]", 1)
	s, err := ParseOrkaSource("reader.agent.md", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s.Source, []byte(raw)) || len(s.Tools) != 2 || !strings.Contains(string(s.Tools[0]), "web_fetch") {
		t.Fatal("unsupported declaration was dropped")
	}
	want := map[string]string{"Frontmatter": "unrepresented", "kmx": "unrepresented", "kmx.tools": "not_yet_supported"}
	if len(s.Unsupported) != len(want) {
		t.Fatalf("diagnostics: %#v", s.Unsupported)
	}
	for _, u := range s.Unsupported {
		if want[u.Field] != u.Reason {
			t.Fatalf("wrong diagnostic: %#v", u)
		}
	}
}

func TestOrkaValidationRejectsInvalidKnownContent(t *testing.T) {
	cases := map[string][]byte{
		"missing marker":     []byte("A prompt.\n"),
		"version":            []byte(strings.Replace(orkaExample, "schemaVersion: 1", "schemaVersion: 2", 1)),
		"runtime":            []byte(strings.Replace(orkaExample, "runtime: orka", "runtime: other", 1)),
		"model":              []byte(strings.Replace(orkaExample, "model: model/v1", "model: auto", 1)),
		"model list":         []byte(strings.Replace(orkaExample, "model: model/v1", "model: [a, b]", 1)),
		"name":               []byte(strings.Replace(orkaExample, "name: reader", "name: BAD-NAME-CANARY", 1)),
		"empty prompt":       []byte(strings.TrimSuffix(orkaExample, "# Reader\n\n  Keep whitespace.  \n")),
		"duplicate":          orkaWith("  runtime: orka"),
		"alias unknown":      []byte(strings.Replace(orkaExample, "model: model/v1", "model: model/v1\ncustom: &c value\nother: *c", 1)),
		"inline and ref":     orkaWith("  soul: {inline: text, configMapRef: {name: persona, key: soul.md}}"),
		"ref without digest": orkaWith("  soul: {configMapRef: {name: persona, key: soul.md}}"),
		"digest mismatch":    orkaWith("  soul: {inline: text, digest: 'sha256:" + strings.Repeat("0", 64) + "'}"),
		"Soul byte overflow": orkaWith("  soul: {inline: '" + strings.Repeat("é", 4097) + "'}"),
		"empty Soul":         orkaWith("  soul: {inline: ''}"),
		"ref name":           orkaWith("  soul: {configMapRef: {name: BAD-NAME-CANARY, key: soul.md}, digest: 'sha256:" + strings.Repeat("0", 64) + "'}"),
		"duplicate skills":   []byte(strings.Replace(orkaExample, "skills: []", "skills: [{name: research}, {name: research}]", 1)),
		"ambiguous skill":    []byte(strings.Replace(orkaExample, "skills: []", "skills: [{name: research, configMapRef: {name: cm, key: skill}}]", 1)),
		"invalid key":        []byte(strings.Replace(orkaExample, "skills: []", "skills: [{configMapRef: {name: cm, key: 'PRIVATE/CANARY'}}]", 1)),
		"tools non JSON key": []byte(strings.Replace(orkaExample, "tools: []", "tools: [{1: value}]", 1)),
		"tools non finite":   []byte(strings.Replace(orkaExample, "tools: []", "tools: [{value: .inf}]", 1)),
		"mixed tools":        []byte(strings.Replace(orkaExample, "model: model/v1", "model: model/v1\ntools: [web_fetch]", 1)),
		"body control":       []byte(orkaExample + "\n## Model\n- **Preferred:** other\n"),
		"inline examples":    []byte(orkaExample + "\n## Examples\n```yaml\nexamples: []\n```\n"),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseOrkaSource("reader.agent.md", raw)
			if err == nil {
				t.Fatal("invalid native source accepted")
			}
			if strings.Contains(err.Error(), "BAD-NAME-CANARY") || strings.Contains(err.Error(), "PRIVATE/CANARY") {
				t.Fatalf("value leaked: %v", err)
			}
		})
	}
	for _, name := range []string{"reader.md", ".agent.md", "reader.agent.test.md", "charter.md"} {
		if _, err := ParseOrkaSource(name, []byte(orkaExample)); !errors.Is(err, ErrOrkaSourceUnsupported) {
			t.Fatalf("wrong filename classification: %v", err)
		}
	}
}

func TestOrkaValidationDecodedValues(t *testing.T) {
	token := "ghp_" + strings.Repeat("x", 36)
	var escaped strings.Builder
	for _, r := range token {
		fmt.Fprintf(&escaped, "\\u%04x", r)
	}
	for _, value := range []string{escaped.String(), `\u2028`, `\u0085`, `\u0001`} {
		quoted := `"` + value + `"`
		for _, raw := range [][]byte{
			[]byte(strings.Replace(orkaExample, "'  display text  '", quoted, 1)),
			orkaWith("  soul: {inline: " + quoted + "}"),
			orkaWith("  soul: {configMapRef: {name: " + quoted + ", key: soul.md}, digest: 'sha256:" + strings.Repeat("0", 64) + "'}"),
			[]byte(strings.Replace(orkaExample, "skills: []", "skills: [{name: "+quoted+"}]", 1)),
			[]byte(strings.Replace(orkaExample, "skills: []", "skills: [{configMapRef: {name: cm, key: "+quoted+"}}]", 1)),
		} {
			if _, err := ParseOrkaSource("reader.agent.md", raw); err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), escaped.String()) {
				t.Fatalf("unsafe decoded value: %v", err)
			}
		}
	}
}
