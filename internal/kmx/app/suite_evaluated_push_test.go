package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func TestSuiteEvaluatedFilePushContract(t *testing.T) {
	root, prepared, suite, placement, observations := passingFixture(t)
	receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalSuitePassingReceipt(t.Context(), receipt, suite)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Out: io.Discard}
	for _, change := range []string{"malformed before prepare", "incoherent before transfer", "other S", "case changed", "equivalent source spelling", "equivalent case spelling", "passing"} {
		t.Run(change, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("KMX_HOME", state)
			file := filepath.Join(privateSuiteDir(t), "receipt.json")
			raw := canonical
			source := filepath.Join(t.TempDir(), "workspace")
			copyTree(t, root, source)
			switch change {
			case "malformed before prepare":
				raw = []byte("{}")
			case "incoherent before transfer":
				raw = []byte(strings.Replace(string(raw), receipt.Subject.PayloadDigest, strings.Repeat("0", 64), 1))
			case "other S":
				raw = []byte(strings.Replace(string(raw), receipt.Subject.SuiteDigest, "sha256:"+strings.Repeat("0", 64), 1))
			case "case changed", "equivalent case spelling":
				original, err := os.ReadFile(filepath.Join(source, "eval/example.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				changed := string(original) + "# packaged cases do not retain authored comments\n"
				if change == "case changed" {
					changed = strings.Replace(string(original), "input:", "input: Changed", 1)
				}
				writeFile(t, filepath.Join(source, "eval/example.yaml"), changed)
			case "equivalent source spelling":
				member := filepath.Join(source, "agents/greeter/member.yaml")
				original, err := os.ReadFile(member)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, member, string(original)+"# artifact-equivalent spelling\n")
			}
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(t.TempDir(), "layout")
			result, err := a.pushSuiteWorkspaceEvaluated(t.Context(), source, "suite:v1", destination, false, false, SuitePushProvenance{}, file)
			passing := change == "passing" || change == "equivalent source spelling" || change == "equivalent case spelling"
			if passing {
				if err != nil || result.Descriptor.Digest != prepared.Descriptor.Digest {
					t.Fatal("valid receipt did not transfer", err)
				}
				if _, err := os.Stat(destination); err != nil {
					t.Fatal("missing transfer output", err)
				}
			} else {
				want := "invalid native passing receipt"
				if change == "other S" || change == "case changed" {
					want = "expected publication identity"
					if err == nil || !strings.Contains(err.Error(), "rerun kmx package test") {
						t.Fatalf("changed artifact needs a fresh-evaluation remedy: %v", err)
					}
				}
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("wrong refusal stage: %v", err)
				}
				if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Fatal("refusal transferred")
				}
			}
			if change == "malformed before prepare" {
				if _, err := os.Stat(filepath.Join(state, "suites")); !os.IsNotExist(err) {
					t.Fatal("malformed receipt prepared cache")
				}
			}
			if change == "incoherent before transfer" {
				if _, err := os.Stat(filepath.Join(state, "suites")); err != nil {
					t.Fatal("coherence check never reached prepared package", err)
				}
			}
			if change == "equivalent source spelling" {
				fresh, err := workspace.Prepare(t.Context(), source, filepath.Join(t.TempDir(), "cache"))
				if err != nil || fresh.Fingerprint == prepared.Fingerprint || fresh.Descriptor.Digest != prepared.Descriptor.Digest {
					t.Fatal("equivalent-spelling positive control failed", err)
				}
			}
		})
	}
}
