package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

const publicModuleSum = "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func TestSuitePublicProducerIdentityRoutes(t *testing.T) {
	clean := []debug.BuildSetting{{Key: "vcs.revision", Value: strings.Repeat("a", 40)}, {Key: "vcs.modified", Value: "false"}}
	for _, tc := range []struct {
		name, version, sum, want string
		settings                 []debug.BuildSetting
		replace                  *debug.Module
	}{
		{name: "module-install", version: "v1.2.3", sum: publicModuleSum, want: "v1.2.3-mod-" + strings.Repeat("0", 64)},
		{name: "module-prerelease", version: "v1.2.3-rc.1", sum: publicModuleSum, want: "v1.2.3-rc.1-mod-" + strings.Repeat("0", 64)},
		{name: "release-binary", version: "v1.2.3", settings: clean, want: "v1.2.3-git-" + strings.Repeat("a", 40)},
		{name: "checkout", version: "(devel)", settings: clean, want: "devel-git-" + strings.Repeat("a", 40)},
		{name: "vcs-without-module-version", settings: clean, want: "git-" + strings.Repeat("a", 40)},
		{name: "pseudo-clean", version: "v1.2.4-0.20260101000000-aaaaaaaaaaaa", sum: publicModuleSum, settings: clean, want: "v1.2.4-0.20260101000000-aaaaaaaaaaaa-git-" + strings.Repeat("a", 40)},
		{name: "devel-unstamped", version: "(devel)", sum: publicModuleSum},
		{name: "empty-version", sum: publicModuleSum},
		{name: "empty-sum", version: "v1.2.3"},
		{name: "malformed-sum", version: "v1.2.3", sum: "h1:not-a-checksum"},
		{name: "short-sum", version: "v1.2.3", sum: "h1:YQ=="},
		{name: "wrong-sum-kind", version: "v1.2.3", sum: "h2:" + strings.TrimPrefix(publicModuleSum, "h1:")},
		{name: "noncanonical-sum", version: "v1.2.3", sum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAB="},
		{name: "newline-sum", version: "v1.2.3", sum: publicModuleSum + "\n"},
		{name: "malformed-sum-clean", version: "v1.2.3", sum: "invalid", settings: clean},
		{name: "replacement", version: "v1.2.3", sum: publicModuleSum, replace: &debug.Module{Path: "example.invalid/replacement", Version: "v9.0.0", Sum: publicModuleSum}},
		{name: "pseudo-without-revision", version: "v1.2.4-0.20260101000000-aaaaaaaaaaaa", sum: publicModuleSum},
		{name: "pseudo-zero-without-revision", version: "v0.0.0-20260101000000-aaaaaaaaaaaa", sum: publicModuleSum},
		{name: "pseudo-prerelease-without-revision", version: "v1.2.3-rc.1.0.20260101000000-aaaaaaaaaaaa", sum: publicModuleSum},
		{name: "noncanonical-version", version: "v01.2.3", sum: publicModuleSum},
		{name: "noncanonical-prerelease", version: "v1.2.3-01", sum: publicModuleSum},
		{name: "short-version", version: "v1.2", sum: publicModuleSum},
		{name: "missing-v-prefix", version: "1.2.3", sum: publicModuleSum},
		{name: "unsupported-version", version: "v1.2.3+incompatible", sum: publicModuleSum},
		{name: "oversize-version", version: "v1.2.3-" + strings.Repeat("a", 58), sum: publicModuleSum},
		{name: "dirty-tag", version: "v1.2.3", sum: publicModuleSum, settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}},
		{name: "invalid-modified-tag", version: "v1.2.3", sum: publicModuleSum, settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "FALSE"}}},
		{name: "invalid-revision-tag", version: "v1.2.3", sum: publicModuleSum, settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}}},
		{name: "empty-revision-tag", version: "v1.2.3", sum: publicModuleSum, settings: []debug.BuildSetting{{Key: "vcs.revision", Value: ""}}},
		{name: "missing-modified-tag", version: "v1.2.3", sum: publicModuleSum, settings: clean[:1], want: "v1.2.3-mod-" + strings.Repeat("0", 64)},
		{name: "missing-revision-tag", version: "v1.2.3", sum: publicModuleSum, settings: clean[1:], want: "v1.2.3-mod-" + strings.Repeat("0", 64)},
		{name: "complete-vcs-tag", version: "v1.2.3", sum: publicModuleSum, settings: clean, want: "v1.2.3-mod-" + strings.Repeat("0", 64)},
		{name: "duplicate-revision-tag", version: "v1.2.3", sum: publicModuleSum, settings: append(append([]debug.BuildSetting{}, clean...), clean[0])},
		{name: "duplicate-modified-tag", version: "v1.2.3", sum: publicModuleSum, settings: append(append([]debug.BuildSetting{}, clean...), clean[1])},
		{name: "duplicate-vcs-tag", version: "v1.2.3", sum: publicModuleSum, settings: []debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs", Value: "git"}}},
		{name: "duplicate-time-tag", version: "v1.2.3", sum: publicModuleSum, settings: []debug.BuildSetting{{Key: "vcs.time", Value: "2026-01-01T00:00:00Z"}, {Key: "vcs.time", Value: "2026-01-01T00:00:00Z"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := suitePublicBuildInfo(&debug.BuildInfo{Main: debug.Module{Path: "github.com/kaimahi-agents/kaimahi", Version: tc.version, Sum: tc.sum, Replace: tc.replace}, Settings: tc.settings})
			if tc.want == "" {
				if err == nil {
					t.Fatal("accepted unsupported provenance")
				}
				return
			}
			if err != nil {
				t.Fatal("supported provenance refused:", err)
			}
			if got, err := p.publicVersion(); err != nil || got != tc.want {
				t.Fatalf("version = %q, %v; want %q", got, err, tc.want)
			}
			if p.Version != tc.version {
				t.Fatalf("invented private version: %q; want %q", p.Version, tc.version)
			}
			if tc.name == "module-install" {
				raw, err := agentsuite.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				want := `{"module":"github.com/kaimahi-agents/kaimahi","sum":"h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","version":"v1.2.3"}`
				if string(raw) != want {
					t.Fatalf("invented VCS or lost module identity: %s", raw)
				}
			}
		})
	}
}

func TestSuitePublicTaggedCompanionStrict(t *testing.T) {
	for _, variant := range []string{"good", "missing-module", "missing-version", "missing-sum", "null-sum", "empty-sum", "null-modified", "null-revision", "invalid-revision", "dirty-modified", "revision-without-modified", "modified-without-revision"} {
		t.Run(variant, func(t *testing.T) {
			store, suite, raw := passStoreFixture(t)
			p, err := suitePublicBuildInfo(&debug.BuildInfo{Main: debug.Module{Path: "github.com/kaimahi-agents/kaimahi", Version: "v1.2.3", Sum: publicModuleSum}})
			if err != nil {
				t.Fatal(err)
			}
			m := publicMetadataFixture()
			m.Producer = p
			if err := store.reserve(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := store.publishPublicMetadata(t.Context(), raw, suite, m); err != nil {
				t.Fatal(err)
			}
			if _, err := store.commit(t.Context(), raw, suite); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(store.partition, m.filename())
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			producer := value["producer"].(map[string]any)
			switch variant {
			case "missing-module", "missing-version", "missing-sum":
				delete(producer, strings.TrimPrefix(variant, "missing-"))
			case "null-sum", "null-modified", "null-revision":
				producer[strings.TrimPrefix(variant, "null-")] = nil
			case "empty-sum":
				producer["sum"] = ""
			case "invalid-revision":
				producer["revision"] = "invalid"
			case "dirty-modified":
				producer["modified"] = true
			case "revision-without-modified":
				producer["revision"] = strings.Repeat("a", 40)
				p.Revision = strings.Repeat("a", 40)
			case "modified-without-revision":
				producer["modified"] = false
				modified := false
				p.Modified = &modified
			}
			data, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, data, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := store.readPublicMetadata(t.Context(), suite)
			if variant == "good" || variant == "revision-without-modified" || variant == "modified-without-revision" {
				if err != nil || !reflect.DeepEqual(got.Producer, p) {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("accepted malformed companion")
			}
			if _, _, err := store.latest(t.Context(), suite); err != nil {
				t.Fatal("private gate poisoned", err)
			}
		})
	}
}

func TestSuitePublicTaggedExportAndChecksumBinding(t *testing.T) {
	var previous agentsuite.EvaluationEvidence
	for i, sum := range []string{publicModuleSum, "h1:AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="} {
		t.Run([]string{"original", "changed"}[i], func(t *testing.T) {
			store, suite, raw := passStoreFixture(t)
			p, err := suitePublicBuildInfo(&debug.BuildInfo{Main: debug.Module{Path: "github.com/kaimahi-agents/kaimahi", Version: "v1.2.3", Sum: sum}})
			if err != nil {
				t.Fatal(err)
			}
			m := publicMetadataFixture()
			m.Producer = p
			if err := store.reserve(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := store.publishPublicMetadata(t.Context(), raw, suite, m); err != nil {
				t.Fatal(err)
			}
			if _, err := store.commit(t.Context(), raw, suite); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "tagged-evidence.json")
			if err := (&App{Out: &bytes.Buffer{}}).ExportSuiteEvaluation(t.Context(), SuiteEvaluationExportOptions{Workspace: store.workspace, Member: suite.MemberID, Out: out}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			var evidence agentsuite.EvaluationEvidence
			if err := agentsuite.Unmarshal(data, &evidence); err != nil {
				t.Fatal(err)
			}
			source := []byte(`{"domain":"kmx.go-build-source/v1","producer":{"module":"github.com/kaimahi-agents/kaimahi","sum":"` + sum + `","version":"v1.2.3"}}`)
			if evidence.Execution.Renderer.Digest != suitePublicHash(source) || evidence.Execution.Profile.Digest != suitePublicHash(source) {
				t.Fatal("module checksum not bound to source projection")
			}
			if evidence.Evaluator.Identity.Version != "v1.2.3-mod-"+strings.Repeat([]string{"00", "01"}[i], 32) {
				t.Fatal("checksum not losslessly represented", evidence.Evaluator.Identity)
			}
			if i > 0 && (previous.Execution.Renderer.Digest == evidence.Execution.Renderer.Digest || previous.Evaluator.Identity == evidence.Evaluator.Identity || previous.Evaluator.Identity.Policy.ConfigurationDigest == evidence.Evaluator.Identity.Policy.ConfigurationDigest) {
				t.Fatal("checksum change did not change source/evaluator identity")
			}
			previous = evidence
			prepared, err := workspace.Prepare(t.Context(), store.workspace, store.cache)
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := suiteoras.ReadArtifactLayout(t.Context(), prepared.Path, suite.Digest)
			if err != nil {
				t.Fatal(err)
			}
			r, err := parseSuitePassingReceipt(raw)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := suitePublicExpected(artifact, suite, r, m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := agentsuite.VerifyEvaluation(prepared.Path, expected, bytes.NewReader(data)); err != nil {
				t.Fatal("shared verification", err)
			}
		})
	}
}
