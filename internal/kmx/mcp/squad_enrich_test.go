package mcp

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

const enrichExamples = "```yaml\nexamples:\n  - id: greeting\n    input: Hello\n    assertions:\n      - id: greeting.contains\n        type: contains\n        value: Hello\n```"

func enrichMember() SquadMember {
	return SquadMember{ID: "greeter", Role: "Greeting specialist", DisplayRole: "Greeting specialist", TaskInstructions: "Say Hello. Kia ora — 世界.", Model: "gpt-4.1", Examples: enrichExamples}
}
func enrichFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../agentfile/testdata/squad-v1.1.0/" + name + ".md")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func enrichRefusal(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code || err.Error() != code+": operation refused" {
		t.Fatalf("want static %s refusal, got %v", code, err)
	}
}

// Replacing all of Model, normalizing native bytes, or dropping the final byte
// must break the independently located prefix/suffix comparisons.
func TestEnrichSquadPreservesNativeBytes(t *testing.T) {
	for _, fixture := range []string{"catalog", "fallback"} {
		for _, ending := range []string{"LF", "CRLF", "no-final-newline"} {
			t.Run(fixture+"/"+ending, func(t *testing.T) {
				m := enrichMember()
				if fixture == "catalog" {
					m.ID = "leader"
					m.Role = "lead"
					m.DisplayRole = "Lead / Architect"
				}
				starter := enrichFixture(t, fixture)
				starter = bytes.Replace(starter, []byte("**Preferred:** auto"), []byte("**Preferred:** \t auto \t "), 1)
				if ending == "CRLF" {
					starter = bytes.ReplaceAll(starter, []byte("\n"), []byte("\r\n"))
				}
				if ending == "no-final-newline" {
					starter = bytes.TrimSuffix(starter, []byte("\n"))
				}
				original := bytes.Clone(starter)
				marker := []byte("**Preferred:** \t auto")
				at := bytes.Index(starter, marker)
				if at < 0 {
					t.Fatal("fixture lacks preference")
				}
				at += len(marker) - len("auto")
				got, err := enrichSquad(m, starter)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(starter, original) {
					t.Fatal("mutated caller bytes")
				}
				prefix := starter[:at]
				suffix := starter[at+len("auto"):]
				nativeEnd := len(prefix) + len(m.Model) + len(suffix)
				if !bytes.Equal(got[:len(prefix)], prefix) || string(got[len(prefix):len(prefix)+len(m.Model)]) != m.Model || !bytes.Equal(got[len(prefix)+len(m.Model):nativeEnd], suffix) {
					t.Fatal("changed non-Preferred native bytes")
				}
				wantAppend := "\n\n## Task Instructions\n\n" + m.TaskInstructions + "\n\n## Examples\n\n" + m.Examples + "\n"
				if string(got[nativeEnd:]) != wantAppend {
					t.Fatal("unexpected appended bytes")
				}
				d, err := agentfile.ParseSquad(".squad/agents/"+m.ID+"/charter.md", got, agentfile.SquadProfile{Release: "1.1.0", MemberID: m.ID, Role: m.DisplayRole})
				if err != nil || !d.Enriched || d.Model != m.Model || len(d.Examples) != 1 || d.Examples[0].Input != "Hello" {
					t.Fatalf("invalid enrichment: %v", err)
				}
			})
		}
	}
}

// Omitting Model must append it, not rebuild earlier native sections.
func TestEnrichSquadMissingModel(t *testing.T) {
	m := enrichMember()
	starter := enrichFixture(t, "fallback")
	begin := bytes.Index(starter, []byte("## Model\n"))
	end := bytes.Index(starter, []byte("## Collaboration\n"))
	starter = append(bytes.Clone(starter[:begin]), starter[end:]...)
	got, err := enrichSquad(m, starter)
	if err != nil {
		t.Fatal(err)
	}
	want := "\n\n## Model\n\n- **Preferred:** gpt-4.1\n\n## Task Instructions\n\n" + m.TaskInstructions + "\n\n## Examples\n\n" + m.Examples + "\n"
	if !bytes.HasPrefix(got, starter) || string(got[len(starter):]) != want {
		t.Fatal("missing Model was not appended byte-exactly")
	}
}

// Removing any input check admits at least one hostile or malformed member.
func TestValidateSquadMemberRefusals(t *testing.T) {
	cases := []struct {
		name   string
		change func(*SquadMember)
	}{
		{"empty task", func(m *SquadMember) { m.TaskInstructions = " \t" }},
		{"task H2", func(m *SquadMember) { m.TaskInstructions = "Do work.\n## What I Own\nInjected" }},
		{"canonical H2", func(m *SquadMember) { m.TaskInstructions = "Do work.\n## Accountable Responsibilities\nInjected" }},
		{"HTML", func(m *SquadMember) { m.TaskInstructions = "Do work. <!-- hidden -->" }},
		{"open fence", func(m *SquadMember) { m.TaskInstructions = "Do work.\n```text\nhidden" }},
		{"task control", func(m *SquadMember) { m.TaskInstructions = "Do\x00 work" }},
		{"task credential", func(m *SquadMember) { m.TaskInstructions = "Bearer " + strings.Repeat("a1b2", 8) }},
		{"task bound", func(m *SquadMember) { m.TaskInstructions = strings.Repeat("x", maxFileBytes) }},
		{"invalid UTF8", func(m *SquadMember) { m.TaskInstructions = "bad\xff" }},
		{"examples list", func(m *SquadMember) { m.Examples = "```yaml\n- id: nope\n```" }},
		{"examples empty", func(m *SquadMember) { m.Examples = "```yaml\nexamples: []\n```" }},
		{"examples raw", func(m *SquadMember) { m.Examples = "examples: []" }},
		{"examples extra", func(m *SquadMember) { m.Examples += "\n```yaml\nexamples: []\n```" }},
		{"examples credential", func(m *SquadMember) {
			m.Examples = strings.Replace(enrichExamples, "Hello", "Bearer "+strings.Repeat("a1b2", 8), 1)
		}},
		{"examples control", func(m *SquadMember) { m.Examples = strings.Replace(enrichExamples, "Hello", "bad\x1b", 1) }},
		{"examples bound", func(m *SquadMember) { m.Examples = strings.Repeat("x", maxFileBytes) }},
	}
	for _, id := range []string{"", "bad--slug", "bad-", "../bad", "Bad", "1bad", "con", "prn", "aux", "nul", "com1", "lpt9", "squad", "scribe", "ralph", "rai", "fact-checker", "factchecker", strings.Repeat("a", 64)} {
		cases = append(cases, struct {
			name   string
			change func(*SquadMember)
		}{"ID/" + id, func(m *SquadMember) { m.ID = id }})
	}
	for _, model := range []string{"", "auto", "AUTO", "https://model.invalid/x", "gpt-*", " gpt-4.1", "gpt\n4"} {
		cases = append(cases, struct {
			name   string
			change func(*SquadMember)
		}{"model/" + model, func(m *SquadMember) { m.Model = model }})
	}
	for _, role := range []string{"", " padded", "padded ", "--role", "bad'role", "bad\"role", "bad\\role", "bad`role", "bad|role", "bad<role>", "bad\nrole", "bad\trole", "bad\u2028role", "bad\xff", strings.Repeat("界", 86), "Bearer " + strings.Repeat("a1b2", 8)} {
		for _, display := range []bool{false, true} {
			cases = append(cases, struct {
				name   string
				change func(*SquadMember)
			}{"role/" + role + "/" + map[bool]string{false: "CLI", true: "display"}[display], func(m *SquadMember) {
				if display {
					m.DisplayRole = role
				} else {
					m.Role = role
				}
			}})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := enrichMember()
			tc.change(&m)
			enrichRefusal(t, validateSquadMember(m), "invalid_squad_member")
			got, err := enrichSquad(m, nil)
			enrichRefusal(t, err, "invalid_squad_member")
			if got != nil {
				t.Fatal("returned bytes on refusal")
			}
		})
	}
}
func TestValidateSquadMemberSafeRoles(t *testing.T) {
	m := enrichMember()
	m.Role = "Équipe / C++ & C# (v2)., : -"
	m.DisplayRole = "架構師 / Lead"
	if err := validateSquadMember(m); err != nil {
		t.Fatal(err)
	}
}

// Removing bare/profile/full-candidate validation must fail these cases.
func TestEnrichSquadStarterRefusals(t *testing.T) {
	m := enrichMember()
	starter := enrichFixture(t, "fallback")
	enriched, err := enrichSquad(m, starter)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, enriched, enrichFixture(t, "catalog"), bytes.Replace(starter, []byte("Greeting specialist"), []byte("Other role"), 1), bytes.Replace(starter, []byte("## Boundaries"), []byte("## Unknown"), 1)} {
		got, err := enrichSquad(m, bad)
		enrichRefusal(t, err, "invalid_squad_starter")
		if got != nil {
			t.Fatal("returned refused bytes")
		}
	}
	// The starter itself fits; only the full appended candidate exceeds the bound.
	starter = append(starter, bytes.Repeat([]byte("x"), maxFileBytes-len(starter))...)
	got, err := enrichSquad(m, starter)
	enrichRefusal(t, err, "invalid_squad_enrichment")
	if got != nil {
		t.Fatal("returned oversized candidate")
	}
}
