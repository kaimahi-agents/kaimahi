package agentfile

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const squadExamples = "\n\n## Task Instructions\n\nReply with `Hello, ` followed by the input.\n\n## Examples\n\n```yaml\nexamples:\n  - id: greeting\n    input: Ada\n    assertions:\n      - id: greeting-text\n        type: contains\n        value: Hello, Ada\n```\n"

func squadFixture(t *testing.T, catalog bool) ([]byte, SquadProfile, string) {
	t.Helper()
	name, id, role := "fallback", "greeter", "Greeting specialist"
	if catalog {
		name, id, role = "catalog", "leader", "Lead / Architect"
	}
	data, err := os.ReadFile("testdata/squad-v1.1.0/" + name + ".md")
	if err != nil {
		t.Fatal(err)
	}
	return data, SquadProfile{Release: "1.1.0", MemberID: id, Role: role}, ".squad/agents/" + id + "/charter.md"
}

func enrichedSquad(data []byte) []byte {
	return append(bytes.ReplaceAll(data, []byte("**Preferred:** auto"), []byte("**Preferred:** fake")), []byte(squadExamples)...)
}

func TestSquadNativeStartersRemainDistinctFromCanonicalAgents(t *testing.T) {
	for _, catalog := range []bool{true, false} {
		t.Run(fmt.Sprint(catalog), func(t *testing.T) {
			raw, profile, filename := squadFixture(t, catalog)
			doc, err := ParseSquad(filename, raw, profile)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Enriched || len(doc.Examples) != 0 || doc.Model != "auto" {
				t.Fatalf("starter became executable: %+v", doc)
			}
			if doc.Identity.Role != profile.Role || doc.Profile.MemberID != profile.MemberID {
				t.Fatal("cast identity lost")
			}
			wantName := profile.MemberID
			if catalog {
				wantName = ""
			}
			if doc.Identity.Name != wantName {
				t.Fatalf("invented/changed native Name: %q", doc.Identity.Name)
			}
			if !bytes.Equal(doc.Source(), raw) || doc.Revision != fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) {
				t.Fatal("original bytes/revision lost")
			}
			if _, err := Parse(filename, raw); err == nil {
				t.Fatal("canonical parser silently widened")
			}
		})
	}
}

func TestSquadEnrichedProjectionPreservesNativeInstructions(t *testing.T) {
	for _, catalog := range []bool{true, false} {
		raw, profile, filename := squadFixture(t, catalog)
		data := enrichedSquad(raw)
		doc, err := ParseSquad(filename, data, profile)
		if err != nil {
			t.Fatal(err)
		}
		if !doc.Enriched || doc.Model != "fake" || len(doc.Examples) != 1 {
			t.Fatal("missing enriched values")
		}
		if doc.Examples[0].ID != "greeting" || doc.Examples[0].Assertions[0].ID != "greeting-text" {
			t.Fatal("example identity changed")
		}
		if !strings.Contains(doc.Instructions, "## Boundaries") || !strings.Contains(doc.Instructions, "## Collaboration") || !strings.Contains(doc.Instructions, "Standard chain") || !strings.Contains(doc.Instructions, "{brief-slug}") || !strings.Contains(doc.TaskInstructions, "Hello, ") {
			t.Fatal("native/task text dropped or placeholder filled")
		}
		if strings.Contains(doc.Instructions, "## Examples") || strings.Contains(doc.Instructions, "greeting-text") {
			t.Fatal("examples leaked into instructions")
		}
		if strings.Contains(doc.TaskInstructions, "git rev-parse") {
			t.Fatal("native collaboration became task-only instructions")
		}
		if _, err := Parse(filename, data); err == nil {
			t.Fatal("enriched native charter entered canonical parser")
		}
		if doc.Locations["Identity.Role"].Line == 0 || doc.Examples[0].Location.Line <= doc.Locations["Examples"].Line {
			t.Fatal("source locations lost")
		}
	}
}

func TestSquadSpansBindOriginalBytesAndPreferredValue(t *testing.T) {
	for _, catalog := range []bool{true, false} {
		for _, crlf := range []bool{false, true} {
			raw, profile, filename := squadFixture(t, catalog)
			if crlf {
				raw = bytes.ReplaceAll(raw, []byte("\n"), []byte("\r\n"))
			}
			doc, err := ParseSquad(filename, raw, profile)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Preferred == nil || string(raw[doc.Preferred.Start:doc.Preferred.End]) != "auto" {
				t.Fatal("Preferred span includes formatting or wrong bytes")
			}
			end := 0
			for _, section := range doc.Sections {
				if section.Span.Start < end || section.Body.Start < section.Span.Start || section.Body.End != section.Span.End || section.Span.End > len(raw) {
					t.Fatal("invalid ordered byte spans")
				}
				if !bytes.HasPrefix(raw[section.Span.Start:section.Span.End], []byte("## "+section.Name)) {
					t.Fatal("section offset is not original-byte based")
				}
				end = section.Span.End
			}
			if end != len(raw) {
				t.Fatal("final newline or EOF excluded")
			}
			source := doc.Source()
			source[0] = '!'
			raw[0] = '!'
			if doc.Source()[0] != '#' {
				t.Fatal("source aliases caller-owned bytes")
			}
		}
	}
}

func TestSquadPreferredSpanPreservesSurroundingMetadata(t *testing.T) {
	raw, profile, filename := squadFixture(t, false)
	raw = bytes.ReplaceAll(raw, []byte("**Preferred:** auto"), []byte("**Preferred:** \t auto \t"))
	doc, err := ParseSquad(filename, raw, profile)
	if err != nil {
		t.Fatal(err)
	}
	span := doc.Preferred
	changed := append(append(append([]byte{}, raw[:span.Start]...), []byte("fake")...), raw[span.End:]...)
	if !bytes.Equal(changed, bytes.Replace(raw, []byte("\t auto \t"), []byte("\t fake \t"), 1)) {
		t.Fatal("value span rewrites surrounding native bytes")
	}
}

func TestSquadOptionalModelMetadataAndAppendPosition(t *testing.T) {
	raw, profile, filename := squadFixture(t, false)
	withoutFallback := bytes.Replace(raw, []byte("- **Fallback:** Standard chain\n"), nil, 1)
	doc, err := ParseSquad(filename, enrichedSquad(withoutFallback), profile)
	if err != nil || bytes.Contains(doc.Source(), []byte("**Fallback:**")) {
		t.Fatalf("invented required Fallback: %v", err)
	}
	start, end := bytes.Index(raw, []byte("## Model\n")), bytes.Index(raw, []byte("## Collaboration\n"))
	withoutModel := append(append([]byte{}, raw[:start]...), raw[end:]...)
	starter, err := ParseSquad(filename, withoutModel, profile)
	if err != nil || starter.Model != "" || starter.Preferred != nil {
		t.Fatalf("missing native Model unsupported: %v", err)
	}
	appended := append(append([]byte{}, withoutModel...), []byte("\n\n## Model\n\n- **Preferred:** fake"+squadExamples)...)
	added, err := ParseSquad(filename, appended, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !added.Enriched || added.Model != "fake" || added.Preferred == nil || string(appended[added.Preferred.Start:added.Preferred.End]) != "fake" {
		t.Fatal("appended Model did not supply the exact executable preference")
	}
	if added.Sections[5].Name != "Voice" || added.Sections[6].Name != "Model" || added.Sections[7].Name != "Task Instructions" {
		t.Fatal("appended Model changed native section order")
	}
	for _, heading := range []string{"## Boundaries", "## Voice"} {
		misplaced := bytes.Replace(withoutModel, []byte(heading), []byte("## Model\n\n- **Preferred:** fake\n\n"+heading), 1)
		_, err := ParseSquad(filename, misplaced, profile)
		var refusal *Error
		if !errors.As(err, &refusal) || refusal.Field != "Sections" {
			t.Fatal("misplaced nonduplicate Model accepted")
		}
	}
}

func TestSquadEnrichedCRLFLocationsAndSpans(t *testing.T) {
	for _, row := range []struct {
		catalog                               bool
		modelLine, exampleLine, assertionLine int
	}{
		{true, 39, 63, 66}, {false, 32, 57, 60},
	} {
		raw, profile, filename := squadFixture(t, row.catalog)
		data := bytes.ReplaceAll(enrichedSquad(raw), []byte("\n"), []byte("\r\n"))
		doc, err := ParseSquad(filename, data, profile)
		if err != nil {
			t.Fatal(err)
		}
		if !doc.Enriched || doc.Examples[0].ID != "greeting" || doc.Examples[0].Location.Line != row.exampleLine || doc.Examples[0].AssertionLocations[0].Line != row.assertionLine || doc.Locations["Model.Preferred"].Line != row.modelLine {
			t.Fatal("CRLF enriched locations do not address original source lines")
		}
		if strings.Contains(doc.Instructions, "\r") || strings.Contains(doc.TaskInstructions, "\r") || string(data[doc.Preferred.Start:doc.Preferred.End]) != "fake" {
			t.Fatal("CRLF projection/span boundaries changed model bytes")
		}
	}
}

func TestSquadRefusesAmbiguousOrUnsupportedInput(t *testing.T) {
	raw, profile, filename := squadFixture(t, false)
	good := enrichedSquad(raw)
	cases := []struct {
		name  string
		data  []byte
		field string
	}{
		{"partial task", append(bytes.Clone(raw), []byte("\n## Task Instructions\nOnly task\n")...), "Examples"},
		{"partial examples", append(bytes.Clone(raw), []byte(squadExamples[strings.Index(squadExamples, "## Examples"):])...), "Task Instructions"},
		{"auto enriched", append(bytes.Clone(raw), []byte(squadExamples)...), "Model.Preferred"},
		{"missing preferred", bytes.Replace(good, []byte("- **Preferred:** fake"), nil, 1), "Model.Preferred"},
		{"duplicate preferred", bytes.Replace(good, []byte("- **Preferred:** fake"), []byte("- **Preferred:** fake\n- **Preferred:** fake"), 1), "Model"},
		{"duplicate model", bytes.Replace(good, []byte("## Collaboration"), []byte("## Model\n- **Preferred:** fake\n\n## Collaboration"), 1), "Model"},
		{"duplicate task", bytes.Replace(good, []byte("## Examples"), []byte("## Task Instructions\nx\n\n## Examples"), 1), "Task Instructions"},
		{"indented heading", bytes.Replace(good, []byte("## Examples"), []byte(" ## Examples"), 1), "Sections"},
		{"new native section", bytes.Replace(good, []byte("## Voice"), []byte("## Tools\nshell\n\n## Voice"), 1), "Sections"},
		{"reordered native", bytes.Replace(good, []byte("## How I Work"), []byte("## Voice"), 1), "Sections"},
		{"extra after examples", append(bytes.Clone(good), []byte("\n## Voice\nx\n")...), "Examples"},
		{"hidden html", bytes.Replace(good, []byte("## Voice"), []byte("<!-- hidden -->\n## Voice"), 1), "Sections"},
		{"wrong name", bytes.Replace(good, []byte("**Name:** greeter"), []byte("**Name:** other"), 1), "Identity.Name"},
		{"wrong role", bytes.Replace(good, []byte("**Role:** Greeting specialist"), []byte("**Role:** Other"), 1), "Identity.Role"},
		{"wrong title", bytes.Replace(good, []byte("# greeter —"), []byte("# other —"), 1), "Title"},
		{"canonical identity", bytes.Replace(good, []byte("**Name:**"), []byte("**ID:**"), 1), "Identity"},
		{"wildcard model", bytes.Replace(good, []byte("**Preferred:** fake"), []byte("**Preferred:** *"), 1), "Model.Preferred"},
		{"url model", bytes.Replace(good, []byte("**Preferred:** fake"), []byte("**Preferred:** http://localhost"), 1), "Model.Preferred"},
		{"empty task", bytes.Replace(good, []byte("Reply with `Hello, ` followed by the input."), nil, 1), "Task Instructions"},
		{"broken fence", bytes.Replace(good, []byte("```yaml"), []byte("~~~yaml"), 1), "Examples"},
		{"unsupported assertion", bytes.Replace(good, []byte("type: contains"), []byte("type: toolCall"), 1), "Examples"},
		{"too large", bytes.Repeat([]byte("x"), MaxFileBytes+1), "File"},
		{"invalid utf8", append(bytes.Clone(good), 0xff), "File"},
		{"control", append(bytes.Clone(good), 0), "File"},
		{"bom", append([]byte("\xef\xbb\xbf"), good...), "File"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSquad(filename, tc.data, profile)
			var refusal *Error
			if !errors.As(err, &refusal) || !strings.Contains(refusal.Field, tc.field) {
				t.Fatalf("want field %s; got %v", tc.field, err)
			}
		})
	}
}

func TestSquadRequiresQualifiedMappingAndPlacement(t *testing.T) {
	raw, profile, filename := squadFixture(t, false)
	wrong := profile
	wrong.Release = "2.0.0"
	if _, err := ParseSquad(filename, raw, wrong); err == nil {
		t.Fatal("unqualified release accepted")
	}
	wrong = profile
	wrong.Role = "Other"
	if _, err := ParseSquad(filename, raw, wrong); err == nil {
		t.Fatal("unapproved native role accepted")
	}
	for _, id := range []string{"Squad", "squad", "scribe", "ralph", "rai", "fact-checker", "factchecker", "../escape", "white space", "bad--slug", "bad-"} {
		wrong = profile
		wrong.MemberID = id
		matching := bytes.ReplaceAll(raw, []byte("greeter"), []byte(id))
		_, err := ParseSquad(".squad/agents/"+id+"/charter.md", matching, wrong)
		var refusal *Error
		if !errors.As(err, &refusal) || refusal.Field != "Profile.MemberID" {
			t.Fatalf("unsafe/reserved cast ID %s not rejected at mapping: %v", id, err)
		}
	}
	for _, name := range []string{"charter.md", "fallback.md", ".squad/agents/other/charter.md"} {
		if _, err := ParseSquad(name, raw, profile); err == nil {
			t.Fatalf("ambiguous placement %s accepted", name)
		}
	}
	if _, err := ParseSquad(`C:\repo\.squad\agents\greeter\charter.md`, raw, profile); err != nil {
		t.Fatal(err)
	}
}

func TestSquadQualifiedFixtureBytes(t *testing.T) {
	for _, row := range []struct {
		catalog bool
		digest  string
	}{
		{true, "e95bec785702a410617b695bb93c75b45c199fd6c3e4ffec4744edd18bef523b"},
		{false, "12ae2130cda48db0ede019aeb190da150ba374c6c157e2dc9e997e836c07dcf6"},
	} {
		raw, _, _ := squadFixture(t, row.catalog)
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != row.digest {
			t.Fatal("qualified native fixture bytes changed; recapture/review provenance instead of normalizing them")
		}
	}
}

func TestSquadInputSafetyAppliesBeforeNativeProjections(t *testing.T) {
	raw, profile, filename := squadFixture(t, true)
	for _, shape := range secretshapes.All() {
		data := append(bytes.Clone(raw), []byte("\n"+shape.Example)...)
		doc, err := ParseSquad(filename, data, profile)
		var refusal *Error
		if doc != nil || !errors.As(err, &refusal) || refusal.Field != "File" || strings.Contains(err.Error(), shape.Example) {
			t.Fatal("credential content reached a native projection or leaked through refusal")
		}
	}
}

func TestSquadPreambleIsQualifiedAndOptional(t *testing.T) {
	raw, profile, filename := squadFixture(t, true)
	identity := bytes.Index(raw, []byte("## Identity"))
	firstLine := bytes.IndexByte(raw, '\n')
	withoutPreamble := append(bytes.Clone(raw[:firstLine+1]), raw[identity:]...)
	if _, err := ParseSquad(filename, withoutPreamble, profile); err != nil {
		t.Fatal(err)
	}
	for _, original := range [][]byte{raw, enrichedSquad(raw)} {
		bad := bytes.Replace(original, []byte("Adapted from agency-agents"), []byte("unqualified source"), 1)
		if _, err := ParseSquad(filename, bad, profile); err == nil {
			t.Fatal("unqualified HTML preamble accepted")
		}
	}
}

func TestSquadTaskCanContainLiteralArrowText(t *testing.T) {
	raw, profile, filename := squadFixture(t, false)
	data := bytes.Replace(enrichedSquad(raw), []byte("Reply with `Hello, ` followed by the input."), []byte("Return the literal text A --> B."), 1)
	doc, err := ParseSquad(filename, data, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.TaskInstructions, "A --> B") {
		t.Fatal("literal task text was normalized away")
	}
}

func TestSquadFencesDoNotCreateStructuralSections(t *testing.T) {
	raw, profile, filename := squadFixture(t, true)
	data := bytes.Replace(enrichedSquad(raw), []byte("Reply with `Hello, ` followed by the input."), []byte("```text\n## Model\n<!-- inert -->\n```\nReply with `Hello, ` followed by the input."), 1)
	doc, err := ParseSquad(filename, data, profile)
	if err != nil || !strings.Contains(doc.TaskInstructions, "## Model") {
		t.Fatalf("fenced literal interpreted as structure: %v", err)
	}
}

func TestSquadEnrichedZeroExamples(t *testing.T) {
	data, profile, name := squadFixture(t, false)
	zero := strings.Replace(string(enrichedSquad(data)), squadExamples[strings.Index(squadExamples, "```yaml"):], "```yaml\nexamples: []\n```\n", 1)
	doc, err := ParseSquad(name, []byte(zero), profile)
	if err != nil || !doc.Enriched || len(doc.Examples) != 0 || doc.TaskInstructions == "" || strings.Contains(doc.Instructions, "## Examples") {
		t.Fatalf("zero-example enrichment: %+v / %v", doc, err)
	}
}
