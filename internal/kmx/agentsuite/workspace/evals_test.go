package workspace_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	godigest "github.com/opencontainers/go-digest"
)

// Packaging must preserve every assertion and its KMX identity.
func TestPackagedCasesRoundTripAuthoredAssertions(t *testing.T) {
	root, cache := createWorkspace(t)
	authored := map[string]string{
		"a-mixed.yaml":  "id: mixed\ninput: What is 2+2?\nexpectContains: [\"4\", four]\nassertions:\n- {id: no-five, type: notContains, value: \"5\"}\n- {id: digits, type: regex, pattern: '^\\d+$'}\n",
		"b-judge.yaml":  "id: judge\ninput: Explain rain.\nassertions:\n- {id: science, type: llmJudge, rubric: \"Score 100 if correct.\", minScore: 0, judgeModel: qwen-3.5-2b, judgeVersion: pinned}\n",
		"c-tool.yaml":   "id: tool\ninput: List pods.\nassertions:\n- {id: used, type: toolCalled, tool: kubectl}\n",
		"d-repeat.yaml": "id: repeat\ninput: Say hi.\nruns: 3\nminPasses: 2\nexpectContains: [hi]\n",
	}
	for name, body := range authored {
		writeFile(t, filepath.Join(root, "eval", name), body)
	}
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := workspace.Load(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	order := []string{"a-mixed.yaml", "b-judge.yaml", "c-tool.yaml", "d-repeat.yaml", "example.yaml"}
	if len(loaded.Cases) != len(order) {
		t.Fatalf("cases = %d", len(loaded.Cases))
	}
	for i, name := range order {
		want, err := runtime.ParseEvaluationCase(readFile(t, filepath.Join(root, "eval", name)))
		if err != nil {
			t.Fatal(err)
		}
		c := loaded.Cases[i]
		got, err := workspace.DecodePackagedCase(c.Bytes)
		if err != nil || !reflect.DeepEqual(got, want) || c.Path != "evals/greeter/cases/"+want.ID+".json" || c.Digest != godigest.FromBytes(c.Bytes).String() {
			t.Fatalf("case %s did not round-trip: %+v %v", name, got, err)
		}
		if !reflect.DeepEqual(runtime.EvaluationCaseAssertions(got), runtime.EvaluationCaseAssertions(want)) {
			t.Fatalf("case %s assertion identities changed", name)
		}
	}
}

// scripts/ci/suite-native.py pins the same digest.
func TestPackagedCaseBytesMatchQualifierDigest(t *testing.T) {
	root, cache := createWorkspace(t)
	if err := os.Remove(filepath.Join(root, "eval/example.yaml")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "eval/one.yaml"), "id: arithmetic\ninput: What is 2 + 2?\nexpectContains:\n  - \"4\"\n")
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	b := readFile(t, filepath.Join(extract(t, p), "evals/greeter/cases/arithmetic.json"))
	if got := godigest.FromBytes(b).Encoded(); got != "ff20948597abcfbda7dfbf295cc4af52635ab2e639bf7e899fe0eb6fe8e5b33f" {
		t.Fatalf("packaged case digest %s: %s", got, b)
	}
}

func TestPackagedCasesRefuseUnsafeOrAmbiguousAuthoring(t *testing.T) {
	for name, body := range map[string]string{
		"letter-case duplicate": "id: EXAMPLE\ninput: Question\nexpectContains: [answer]\n",
		"line separator":        "id: separator\ninput: Question\nexpectContains: [\"a\u2028b\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			root, cache := createWorkspace(t)
			writeFile(t, filepath.Join(root, "eval/other.yaml"), body)
			if _, err := workspace.Prepare(t.Context(), root, cache); err == nil {
				t.Fatal("accepted case that cannot be packaged unambiguously")
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("refused source created cache", err)
			}
		})
	}
}

func TestDecodePackagedCaseRefusesUnexecutableOrMalformedCases(t *testing.T) {
	base := `{"assertions":[{"id":"expectContains.0","type":"contains","value":"4"}],"id":"sum","input":"What is 2+2?","mediaType":"application/vnd.agentsuite.eval.case.v1+json","schemaVersion":"1.0.0-draft"}`
	if _, err := workspace.DecodePackagedCase([]byte(base)); err != nil {
		t.Fatal(err)
	}
	explicit := strings.Replace(base, `"type":"contains"`, `"critical":true,"type":"contains"`, 1)
	if c, err := workspace.DecodePackagedCase([]byte(explicit)); err != nil || len(c.ExpectContains) != 1 {
		t.Fatal("explicit default critical:true refused", err)
	}
	for name, doc := range map[string]string{
		"runs over limit":    strings.Replace(base, `"schemaVersion"`, `"runs":101,"schemaVersion"`, 1),
		"minPasses only":     strings.Replace(base, `"schemaVersion"`, `"minPasses":1,"schemaVersion"`, 1),
		"non-critical":       strings.Replace(base, `"type":"contains"`, `"critical":false,"type":"contains"`, 1),
		"skipped legacy":     strings.Replace(base, "expectContains.0", "expectContains.1", 1),
		"legacy after":       `{"assertions":[{"id":"a","type":"contains","value":"4"},{"id":"expectContains.0","type":"contains","value":"4"}],"id":"sum","input":"What is 2+2?","mediaType":"application/vnd.agentsuite.eval.case.v1+json","schemaVersion":"1.0.0-draft"}`,
		"legacy not contain": strings.Replace(base, `"type":"contains","value":"4"`, `"pattern":"4","type":"regex"`, 1),
		"media type":         strings.Replace(base, "eval.case.v1", "eval.case.v2", 1),
		"unknown field":      strings.Replace(base, `"schemaVersion"`, `"private":"x","schemaVersion"`, 1),
		"credential input":   strings.Replace(base, "What is 2+2?", "ghp_"+strings.Repeat("x", 36), 1),
		"blank input":        strings.Replace(base, "What is 2+2?", " ", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := workspace.DecodePackagedCase([]byte(doc)); err == nil {
				t.Fatal("accepted packaged case")
			}
		})
	}
}

// 128 cases fill the native artifact file limit exactly.
func TestMaximumCaseInventoryPreparesAndTransfers(t *testing.T) {
	root, cache := createWorkspace(t)
	for i := 1; i < 128; i++ {
		writeFile(t, filepath.Join(root, "eval", fmt.Sprintf("case-%03d.yaml", i)), fmt.Sprintf("id: c%03d\ninput: Question\nexpectContains: [answer]\n", i))
	}
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), p.Path, p.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := workspace.LoadArtifact(t.Context(), artifact)
	if err != nil || len(loaded.Cases) != 128 {
		t.Fatal("maximum inventory refused", err)
	}
}

func TestLoadNamesContentFreeRefusalReasons(t *testing.T) {
	for name, test := range map[string]struct {
		mutate func(t *testing.T, out string)
		want   string
	}{
		"previous kmx": {func(t *testing.T, out string) {
			name := filepath.Join(out, "metadata/kmx/native-orka.json")
			b := string(readFile(t, name))
			old := strings.NewReplacer(`"schemaVersion":2`, `"cases":[],"schemaVersion":1`, "kmx.native-orka/v2", "kmx.native-orka/v1").Replace(b)
			if old == b {
				t.Fatal("native metadata fixture did not change")
			}
			writeFile(t, name, old)
		}, "rerun kmx eval and kmx package push"},
		"runs over limit": {func(t *testing.T, out string) {
			rebindEvals(t, out, func(_ *suitecore.EvaluationIndex, c map[string]any) { c["runs"] = 101 })
		}, "runs <= 100"},
	} {
		t.Run(name, func(t *testing.T) {
			root, cache := createWorkspace(t)
			p, err := workspace.Prepare(t.Context(), root, cache)
			if err != nil {
				t.Fatal(err)
			}
			out := extract(t, p)
			test.mutate(t, out)
			layout := filepath.Join(t.TempDir(), "layout")
			result, err := suiteoras.Push(t.Context(), out, layout, "modified")
			if err != nil {
				t.Fatal(err)
			}
			if err := filepath.WalkDir(layout, func(name string, d os.DirEntry, err error) error {
				if err != nil || !d.IsDir() {
					return err
				}
				return os.Chmod(name, 0o700)
			}); err != nil {
				t.Fatal(err)
			}
			candidate := *p
			candidate.Path, candidate.Descriptor = layout, result.Descriptor
			_, loadErr := workspace.Load(t.Context(), &candidate)
			artifact, err := suiteoras.ReadArtifactLayout(t.Context(), layout, result.Descriptor.Digest.String())
			if err != nil {
				t.Fatal(err)
			}
			_, artifactErr := workspace.LoadArtifact(t.Context(), artifact)
			for _, err := range []error{loadErr, artifactErr} {
				if err == nil || !strings.HasPrefix(err.Error(), "packaged native suite verification failed: ") || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("refusal reason not named: %v", err)
				}
			}
		})
	}
}
