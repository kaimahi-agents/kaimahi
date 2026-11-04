package agentfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrkaMarkerRefusesPreview(t *testing.T) {
	for _, header := range []string{
		"kmx: {schemaVersion: 1, runtime: orka}",
		"kmx: {schemaVersion: 2, runtime: private-runtime-canary}",
		"kmx: null", "kmx: []", "kmx: private-marker-canary",
		"defaults: &profile {kmx: {schemaVersion: 1, runtime: orka}}\n<<: *profile",
		"<<: [{kmx: {schemaVersion: 1, runtime: orka}}]",
	} {
		t.Run(header, func(t *testing.T) {
			source := []byte("---\n" + header + "\n---\nA prompt.\n")
			for _, parse := range []func(string, []byte) (*Agent, error){Parse, Inspect} {
				_, err := parse("reader.agent.md", source)
				if err == nil {
					t.Fatal("native source entered preview")
				}
				var detail *Error
				if !errors.Is(err, ErrOrkaSourceUnsupported) || !errors.As(err, &detail) || detail.Field != "kmx" || !strings.Contains(err.Error(), "native package") {
					t.Fatalf("missing safe native hint: %v", err)
				}
				if strings.Contains(err.Error(), "private-") {
					t.Fatalf("marker value leaked: %v", err)
				}
			}
			path := filepath.Join(t.TempDir(), "reader.agent.md")
			if err := os.WriteFile(path, source, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadCopilotdPair(path, ""); err == nil {
				t.Fatal("native source entered copilotd")
			}
		})
	}
}

func TestFrontmatterLegacyDelimitersAndUnknowns(t *testing.T) {
	for _, source := range []string{
		"--- \t\nname: reader\nmetadata: {nested: value}\n---\t\nA prompt.\n",
		"---\nmetadata: &m {value: example}\nother: *m\n---\nA prompt.\n",
		"---\nmetadata: {kmx: prose-only}\n---\nA prompt.\n",
	} {
		agent, err := Parse("reader.agent.md", []byte(source))
		if err != nil || agent.Instructions != "A prompt.\n" {
			t.Fatalf("legacy acceptance changed: %v", err)
		}
	}
	for _, header := range []string{"kmx: [", "kmx: {}\nkmx: {}"} {
		if _, err := Parse("reader.agent.md", []byte("---\n"+header+"\n---\nA prompt.\n")); err == nil {
			t.Fatal("invalid YAML accepted")
		}
	}
}
