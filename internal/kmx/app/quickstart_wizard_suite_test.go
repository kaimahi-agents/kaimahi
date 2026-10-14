package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestQuickstartWizardSuiteRefusesLegacyOptionsBeforeWork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		create CreateOptions
	}{
		{"skills", CreateOptions{Skills: "legacy-skill"}},
		{"out", CreateOptions{Out: "agent.yaml"}},
		{"bundle", CreateOptions{BundlePath: "bundle"}},
		{"provider", CreateOptions{ProviderType: "anthropic"}},
		{"runtime", CreateOptions{Runtime: "kagent"}},
		{"limits", CreateOptions{AgentTokensPerMinute: "10"}},
		{"endpoint", CreateOptions{BaseURL: "https://example.com/v1"}},
		{"secret", CreateOptions{Secret: "legacy-key"}},
		{"preview", CreateOptions{NoApply: true}},
		{"blank task", CreateOptions{Task: "   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			home := filepath.Join(t.TempDir(), "private")
			t.Setenv("KMX_HOME", home)
			err := f.app.QuickstartWizard(QuickstartWizardOptions{Create: tc.create})
			if err == nil || strings.Contains(err.Error(), "requires an interactive terminal") || !strings.Contains(err.Error(), "workspace") {
				t.Fatalf("expected actionable source-first refusal, got %v", err)
			}
			if f.calls(t) != "" {
				t.Fatal("external work before refusal")
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatalf("state written before refusal: %v", err)
			}
		})
	}
}

// The source form must not call live-create finishFields: that renders a bundle
// and asks for credentials unrelated to an authored member.
func TestQuickstartWizardSuiteFormOnlyCollectsSource(t *testing.T) {
	create, err := newCreateWizardModel(CreateOptions{Description: "Describe incidents", Namespace: OrkaNamespace, Model: "exact-model"})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create}
	m.create.input.SetValue("incident-helper")
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(quickstartWizardModel)
	if m.create.step == createProviderType || m.create.step == createSecret || m.create.opt.Out != "" {
		t.Fatalf("live provider/artifact question leaked: %+v", m.create.opt)
	}
	// A source purpose is used to generate instructions, then authoring is
	// confirmed separately from the shared evaluation and lift consents.
	m.create.input.SetValue("Explain incidents without guessing")
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(quickstartWizardModel)
	if m.create.step != createConfirm || !strings.Contains(m.create.opt.InstructionText, "Explain incidents without guessing") {
		t.Fatalf("source purpose not collected: step=%v options=%+v", m.create.step, m.create.opt)
	}
	if m.create.opt.Out != "" || m.create.opt.Secret != "" {
		t.Fatal("source form produced live artifact options")
	}
}

func TestQuickstartWizardSuiteAuthorsOnlyPrivateSource(t *testing.T) {
	for _, file := range []bool{false, true} {
		t.Run(fmt.Sprintf("file=%v", file), func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			home := t.TempDir()
			t.Setenv("KMX_HOME", home)
			instructions := "Report only verified incidents.\nDo not invent details.\n"
			opt := CreateOptions{Name: "incident-helper", Description: "Explain incidents", Namespace: "team", Model: "exact-model", InstructionText: instructions}
			if file {
				input := filepath.Join(t.TempDir(), "source.md")
				if err := os.WriteFile(input, []byte(instructions), 0600); err != nil {
					t.Fatal(err)
				}
				opt.InstructionText = ""
				opt.Instructions = input
			}
			root, err := f.app.authorQuickstartSource(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(root, filepath.Join(home, "quickstart-authored")+string(filepath.Separator)) {
				t.Fatalf("source location %s", root)
			}
			body, err := os.ReadFile(filepath.Join(root, "agents", "incident-helper", "instructions.md"))
			if err != nil || string(body) != instructions {
				t.Fatalf("instructions %q err=%v", body, err)
			}
			member, err := os.ReadFile(filepath.Join(root, "agents", "incident-helper", "member.yaml"))
			if err != nil || !strings.Contains(string(member), "exact-model") {
				t.Fatalf("member %q err=%v", member, err)
			}
			for _, name := range []string{"agent.yaml", "agentsuite.json", "bindings.yaml", "bundle", "placement.json"} {
				if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
					t.Fatalf("legacy/generated artifact %s: %v", name, err)
				}
			}
			if _, err := os.Stat(filepath.Join(home, "suites")); !os.IsNotExist(err) {
				t.Fatalf("offline author populated cache: %v", err)
			}
			if f.calls(t) != "" || f.applied(t) != "" {
				t.Fatal("source author contacted cluster")
			}
			// Real preparation/review verifies authored source and visibly distinguishes
			// scaffold assertions from proof, with no native evaluation or placement.
			review, err := f.app.reviewSuiteWorkspace(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			if len(review.inventory) != 1 || review.inventory[0].Case.ID != "example" {
				t.Fatalf("case inventory %+v", review.inventory)
			}
			if !strings.Contains(strings.Join(review.sourceLines(100), "\n"), "not quality proof") {
				t.Fatal("scaffold caveat missing")
			}
			before, err := os.ReadFile(filepath.Join(root, "workspace.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err = f.app.runSuiteWorkspaceReview(ctx, root, opt.Namespace)
			if err == nil {
				t.Fatal("cancelled review succeeded")
			}
			after, err := os.ReadFile(filepath.Join(root, "workspace.yaml"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("cancelled review removed/modified source")
			}
			if f.applied(t) != "" {
				t.Fatal("cancelled review wrote resources")
			}
		})
	}
}
func TestQuickstartWizardSourceRefusesLinkedOrPublicState(t *testing.T) {
	for _, linked := range []bool{true, false} {
		t.Run(fmt.Sprint(linked), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("KMX_HOME", home)
			base := filepath.Join(home, "quickstart-authored")
			outside := t.TempDir()
			if linked {
				if err := os.Symlink(outside, base); err != nil {
					t.Skip(err)
				}
			} else {
				if err := os.Mkdir(base, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(base, 0755); err != nil {
					t.Fatal(err)
				}
				outside = base
			}
			a := &App{}
			_, err := a.authorQuickstartSource(t.Context(), CreateOptions{Name: "private-agent", Description: "A public fixture", Namespace: OrkaNamespace, Model: "exact-model", InstructionText: "Answer factual questions."})
			entries, readErr := os.ReadDir(outside)
			if err == nil || readErr != nil || len(entries) != 0 {
				t.Fatalf("unsafe state reached local writes: %v entries=%d", err, len(entries))
			}
		})
	}
}

func TestQuickstartWizardSuiteCancelledAuthorDoesNotWrite(t *testing.T) {
	home := filepath.Join(t.TempDir(), "private")
	t.Setenv("KMX_HOME", home)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	root, err := (&App{}).authorQuickstartSource(ctx, CreateOptions{})
	if !errors.Is(err, context.Canceled) || root != "" {
		t.Fatalf("root=%q err=%v", root, err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("cancelled author wrote state: %v", err)
	}
}
