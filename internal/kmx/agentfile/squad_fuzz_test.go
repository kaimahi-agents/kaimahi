package agentfile

import (
	"bytes"
	"os"
	"testing"
)

func FuzzParseSquadSourceSpans(f *testing.F) {
	for _, name := range []string{"catalog", "fallback"} {
		raw, err := os.ReadFile("testdata/squad-v1.1.0/" + name + ".md")
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw, name == "catalog")
		f.Add(enrichedSquad(raw), name == "catalog")
	}
	f.Fuzz(func(t *testing.T, raw []byte, catalog bool) {
		id, role := "greeter", "Greeting specialist"
		if catalog {
			id, role = "leader", "Lead / Architect"
		}
		doc, err := ParseSquad(".squad/agents/"+id+"/charter.md", raw, SquadProfile{"1.1.0", id, role})
		if err != nil {
			if doc != nil {
				t.Fatal("refusal returned a usable document")
			}
			return
		}
		if !bytes.Equal(doc.Source(), raw) {
			t.Fatal("parser changed original bytes")
		}
		for _, section := range doc.Sections {
			if section.Span.Start < 0 || section.Span.Start > section.Body.Start || section.Body.Start > section.Body.End || section.Body.End != section.Span.End || section.Span.End > len(raw) {
				t.Fatal("source spans do not address original bytes")
			}
		}
		if p := doc.Preferred; p != nil {
			if p.Start < 0 || p.Start > p.End || p.End > len(raw) || string(raw[p.Start:p.End]) != doc.Model {
				t.Fatal("Preferred span does not address exact model value")
			}
		}
		examples := false
		for _, section := range doc.Sections {
			examples = examples || section.Name == "Examples"
		}
		// Zero authored examples are valid; the Examples section is not optional.
		if doc.Enriched && (doc.Model == "auto" || !examples || doc.TaskInstructions == "") {
			t.Fatal("partial additions accepted as enriched")
		}
	})
}
