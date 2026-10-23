package agentfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func profileHash(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }

// Golden inputs use LF independently of Git's checkout conversion. The separate
// normalization test still proves that actual LF/CRLF source revisions differ.
func runProfileFixtureSource(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/greeter.agent.md")
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
}

func TestRunProfileGoldenAndGenericConfig(t *testing.T) {
	a, err := Parse("greeter.agent.md", runProfileFixtureSource(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, summary, err := FreezeRunProfile(a)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("testdata/run-profile.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, bytes.ReplaceAll(golden, []byte("\r\n"), []byte("\n"))) {
		t.Fatalf("profile encoding drift:\n%s", raw)
	}
	again, other, err := FreezeRunProfile(a)
	if err != nil || !bytes.Equal(raw, again) || summary != other {
		t.Fatal("nondeterministic profile")
	}
	var p runProfile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.SchemaVersion != "kmx.run-profile/v1" || p.AuthoredModel != a.Model || p.SourceRevision != a.Revision || p.TargetExamples != "not-run" || p.Instructions != a.Instructions || p.InstructionsSHA256 != profileHash([]byte(a.Instructions)) || p.SessionConfigSHA256 != profileHash(p.SessionConfig) || summary.ProfileSHA256 != profileHash(raw) || summary.ExampleCount != 1 {
		t.Fatal("wrong profile identity/claims")
	}
	var config map[string]string
	if err := json.Unmarshal(p.SessionConfig, &config); err != nil || len(config) != 1 || config["system_prompt"] != a.Instructions {
		t.Fatal("not the generic Sessions chat config")
	}
	for _, forbidden := range []string{"testdata/", "Locations", "Location", "verdict", "passed", "endpoint", "targetModel"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("unexpected metadata %s", forbidden)
		}
	}
	safe, _ := json.Marshal(summary)
	if bytes.Contains(safe, []byte("Hello,")) || bytes.Contains(safe, []byte("Ada")) {
		t.Fatal("content in summary")
	}
}

func TestRunProfileSourceNormalizationAndSnapshot(t *testing.T) {
	source := runProfileFixtureSource(t)
	lf, err := Parse("greeter.agent.md", source)
	if err != nil {
		t.Fatal(err)
	}
	crlf, err := Parse("greeter.agent.md", bytes.ReplaceAll(source, []byte("\n"), []byte("\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	raw, s1, err := FreezeRunProfile(lf)
	if err != nil {
		t.Fatal(err)
	}
	_, s2, err := FreezeRunProfile(crlf)
	if err != nil {
		t.Fatal(err)
	}
	if s1.SourceRevision == s2.SourceRevision || s1.InstructionsSHA256 != s2.InstructionsSHA256 || s1.ExamplesSHA256 != s2.ExamplesSHA256 || s1.SessionConfigSHA256 != s2.SessionConfigSHA256 {
		t.Fatal("source-byte versus normalized projection identity lost")
	}
	lf.Instructions = "mutated"
	lf.Examples[0].Input = "mutated"
	lf.Examples[0].Assertions[0].Value = "mutated"
	if bytes.Contains(raw, []byte("mutated")) {
		t.Fatal("projection aliases the parsed snapshot")
	}
	if _, _, err := FreezeRunProfile(nil); err == nil {
		t.Fatal("nil snapshot accepted")
	}
}

func TestRunProfileAllAssertionsAndJSONHashDomains(t *testing.T) {
	raw := runProfileFixtureSource(t)
	text := strings.Replace(string(raw), "value: Hello, Ada", "value: '<&> 世界'\n      - id: absent\n        type: notContains\n        value: private\n      - id: pattern\n        type: regex\n        pattern: '^Hello'\n      - id: benefit\n        type: llmJudge\n        rubric: Say why it helps.\n        minScore: 80\n        judgeModel: fake\n        judgeVersion: default", 1)
	text = strings.Replace(text, "examples:\n", "# YAML COMMENT NOT IN CONFIG\nexamples:\n", 1)
	a, err := Parse("greeter.agent.md", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err = FreezeRunProfile(a)
	if err != nil {
		t.Fatal(err)
	}
	var p runProfile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Examples) != 1 || len(p.Examples[0].Assertions) != 4 || p.Examples[0].Assertions[3].JudgeModel != "fake" || *p.Examples[0].Assertions[3].MinScore != 80 {
		t.Fatal("ordered definitions lost")
	}
	exampleBytes, err := json.Marshal(p.Examples)
	if err != nil {
		t.Fatal(err)
	}
	if p.ExamplesSHA256 != profileHash(exampleBytes) {
		t.Fatal("wrong examples JSON hash")
	}
	var first map[string]json.RawMessage
	firstBytes, _ := json.Marshal(p.Examples[0])
	if err := json.Unmarshal(firstBytes, &first); err != nil {
		t.Fatal(err)
	}
	delete(first, "sha256")
	// Pin the case domain's field order independently of map serialization.
	body := struct {
		ID         string          `json:"id"`
		Input      string          `json:"input"`
		Assertions json.RawMessage `json:"assertions"`
	}{p.Examples[0].ID, p.Examples[0].Input, first["assertions"]}
	bodyBytes, _ := json.Marshal(body)
	if p.Examples[0].SHA256 != profileHash(bodyBytes) {
		t.Fatal("wrong deterministic case JSON hash")
	}
	if !bytes.Contains(raw, []byte(`\u003c\u0026\u003e`)) || bytes.Contains(p.SessionConfig, []byte("YAML COMMENT")) || strings.Contains(p.Instructions, "## Examples") {
		t.Fatal("HTML escaping/config projection drift")
	}
}
