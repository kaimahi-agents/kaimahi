package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	suiteevidence "github.com/agent-primitives/suite/evidence"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/content/oci"
)

const verifyCanary = "PRIVATE_VERIFY_CANARY"

type evaluationVerifyFixture struct {
	layout, source, evidencePath, expectPath string
	expected                                 suiteevidence.ExpectedEvaluation
	evidence                                 suiteevidence.EvaluationEvidence
}

func verifyJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := suitecore.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func verifyWrite(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
}

// Build the reviewed expectations from literal public identities and authored
// descriptors first. Claims are generated from those expectations, never vice versa.
// No private receipts, provenance mapper, KMX state, or module-cache data is used.
func newEvaluationVerifyFixture(t *testing.T, judge, repeated bool) evaluationVerifyFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	f := evaluationVerifyFixture{source: filepath.Join(root, "source"), layout: filepath.Join(root, "layout"), evidencePath: filepath.Join(root, "evidence.json"), expectPath: filepath.Join(root, "expected.json")}
	if err := os.CopyFS(f.source, os.DirFS(minimalSuitePath())); err != nil {
		t.Fatal(err)
	}
	var graph suitecore.Suite
	var member suitecore.Agent
	read := func(path string, out any) {
		raw, err := os.ReadFile(filepath.Join(f.source, path))
		if err != nil {
			t.Fatal(err)
		}
		if err := suitecore.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
	}
	read("agentsuite.json", &graph)
	read("agents/writer.json", &member)
	writeDoc := func(path, media string, v any) suitecore.FileRef {
		raw := verifyJSON(t, v)
		verifyWrite(t, filepath.Join(f.source, path), raw)
		return suitecore.FileRef{Path: path, Descriptor: suitecore.Descriptor{MediaType: media, Digest: digest.FromBytes(raw).String(), Size: int64(len(raw))}}
	}
	c := suitecore.EvaluationCase{SchemaVersion: graph.SchemaVersion, MediaType: suitecore.MediaTypeEvaluationCase, ID: "example", Input: verifyCanary, Assertions: []suitecore.EvaluationAssertion{{ID: "contains", Type: "contains", Value: verifyCanary}}}
	if judge {
		threshold := 70
		c.Assertions = []suitecore.EvaluationAssertion{{ID: "quality", Type: "llmJudge", Rubric: verifyCanary, MinScore: &threshold, JudgeModel: "review-judge", JudgeVersion: "v1"}}
	}
	runs, minPasses := 1, 1
	if repeated {
		runs, minPasses = 3, 2
		c.Runs = &runs
		c.MinPasses = &minPasses
	}
	caseRef := writeDoc("evals/writer/cases/example.json", suitecore.MediaTypeEvaluationCase, c)
	indexRef := writeDoc("evals/writer/index.json", suitecore.MediaTypeEvaluationIndex, suitecore.EvaluationIndex{SchemaVersion: graph.SchemaVersion, MediaType: suitecore.MediaTypeEvaluationIndex, Agent: "writer", Cases: []suitecore.FileRef{caseRef}, Limits: suitecore.EvaluationLimits{MaxCases: 10, MaxCaseBytes: 65536}})
	member.Evals = []suitecore.FileRef{indexRef}
	memberRef := writeDoc("agents/writer.json", suitecore.MediaTypeAgent, member)
	graph.Agents[0].Descriptor = memberRef.Descriptor
	writeDoc("agentsuite.json", suitecore.MediaTypeSuite, graph)
	var layer bytes.Buffer
	gz := gzip.NewWriter(&layer)
	tw := tar.NewWriter(gz)
	if err := filepath.WalkDir(f.source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name, err := filepath.Rel(f.source, path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(name), Mode: 0644, Size: int64(len(raw))}); err != nil {
			return err
		}
		_, err = tw.Write(raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	src := memory.New()
	layerDesc := content.NewDescriptorFromBytes(suitecore.MediaTypeContent, layer.Bytes())
	if err := src.Push(t.Context(), layerDesc, bytes.NewReader(layer.Bytes())); err != nil {
		t.Fatal(err)
	}
	dst, err := oci.New(f.layout)
	if err != nil {
		t.Fatal(err)
	}
	packed, err := suiteoras.New(nil).Pack(t.Context(), src, dst, layerDesc)
	if err != nil {
		t.Fatal(err)
	}
	d := "sha256:" + strings.Repeat("a", 64)
	impl := suitecore.EvidenceImplementation{ID: "reviewed-runtime", Version: "1.0.0", Digest: d}
	f.expected = suiteevidence.ExpectedEvaluation{
		Subject:   suiteevidence.EvidenceSubject{Manifest: suiteevidence.EvidenceDescriptor{MediaType: packed.Descriptor.MediaType, Digest: packed.Descriptor.Digest.String(), Size: packed.Descriptor.Size}, MemberID: "writer", MemberDigest: memberRef.Descriptor.Digest, CompositionDigest: graph.Compositions[0].Descriptor.Digest, BuildProfileDigest: graph.BuildProfiles[0].Descriptor.Digest, Platform: suiteevidence.EvidencePlatform{OS: "linux", Architecture: "amd64", Variant: ""}},
		Execution: suiteevidence.EvidenceExecution{Runtime: impl, Harness: impl, Renderer: impl, Profile: impl, Model: suiteevidence.EvidenceModel{Protocol: "openai-compatible", ID: "example-model", ConfigurationDigest: d}, ConfigurationDigest: d},
		Evaluator: suiteevidence.EvidenceEvaluatorIdentity{ID: "reviewed-evaluator", Version: "1.0.0", Policy: suiteevidence.EvidencePolicy{ID: suiteevidence.EvidencePolicyID, Version: suiteevidence.EvidencePolicyVersion, ConfigurationDigest: d}}, Judges: []suiteevidence.EvidenceJudgeConfiguration{},
	}
	if judge {
		f.expected.Judges = []suiteevidence.EvidenceJudgeConfiguration{{Model: "review-judge", Version: "v1", Policy: suiteevidence.EvidencePolicy{ID: suiteevidence.EvidenceJudgePolicyID, Version: suiteevidence.EvidenceJudgePolicyVersion, ConfigurationDigest: d}}}
	}
	toDesc := func(r suitecore.FileRef) suiteevidence.EvidenceDescriptor {
		return suiteevidence.EvidenceDescriptor{MediaType: r.Descriptor.MediaType, Digest: r.Descriptor.Digest, Size: r.Descriptor.Size}
	}
	f.evidence = suiteevidence.EvaluationEvidence{SchemaVersion: suiteevidence.EvidenceSchemaVersion, MediaType: suiteevidence.MediaTypeEvaluationEvidence, Subject: f.expected.Subject, Execution: f.expected.Execution, Evaluator: suiteevidence.EvidenceEvaluator{Identity: f.expected.Evaluator, StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:01:00Z"}, Evaluation: suiteevidence.EvidenceEvaluation{Index: suiteevidence.EvidenceFileRef{Path: indexRef.Path, Descriptor: toDesc(indexRef)}, Cases: []suiteevidence.EvidenceCaseRef{{ID: "example", Path: caseRef.Path, Descriptor: toDesc(caseRef), Runs: runs, MinPasses: minPasses}}}, Results: []suiteevidence.EvidenceCaseResult{{ID: "example", Outcome: "pass", Runs: []suiteevidence.EvidenceRunResult{}}}}
	for n := 1; n <= runs; n++ {
		a := suiteevidence.EvidenceAssertionResult{ID: c.Assertions[0].ID, Type: c.Assertions[0].Type, Outcome: "pass"}
		if judge {
			a.Judge = &suiteevidence.EvidenceJudgeResult{Configuration: f.expected.Judges[0], ObservedModels: []string{"review-judge", "review-judge-v1"}, Scores: []int{80, 90}, MinScore: 70}
		}
		f.evidence.Results[0].Runs = append(f.evidence.Results[0].Runs, suiteevidence.EvidenceRunResult{Number: n, Status: "completed", Outcome: "pass", Assertions: []suiteevidence.EvidenceAssertionResult{a}})
	}
	f.save(t)
	return f
}

func (f evaluationVerifyFixture) save(t *testing.T) {
	t.Helper()
	verifyWrite(t, f.expectPath, verifyJSON(t, f.expected))
	verifyWrite(t, f.evidencePath, verifyJSON(t, f.evidence))
}
func (f evaluationVerifyFixture) options() SuiteEvaluationVerifyOptions {
	return SuiteEvaluationVerifyOptions{Suite: f.layout, Expect: f.expectPath, Evidence: f.evidencePath}
}
func verifyRefusal(t *testing.T, opt SuiteEvaluationVerifyOptions, code string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	err := (&App{Out: &out, Err: &diagnostics}).VerifySuiteEvaluation(t.Context(), opt)
	var refusal *suiteevidence.EvidenceRefusal
	if !errors.As(err, &refusal) || refusal.Code != code {
		t.Fatalf("refusal=%v, want %s", err, code)
	}
	if !strings.HasPrefix(err.Error(), "evaluation: not verified ["+code+"]: ") || strings.Contains(err.Error(), verifyCanary) {
		t.Fatalf("unsafe refusal: %v", err)
	}
	if out.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatalf("refusal wrote output: %q %q", out.String(), diagnostics.String())
	}
}

func TestSuiteEvaluationVerifySuccess(t *testing.T) {
	for _, mode := range []string{"layout-text", "digest-json", "judge-repeated-json"} {
		t.Run(mode, func(t *testing.T) {
			f := newEvaluationVerifyFixture(t, strings.Contains(mode, "judge"), strings.Contains(mode, "repeated"))
			// The actual operation must work with no configuration, runner, or KMX home.
			home := filepath.Join(t.TempDir(), "absent-home")
			t.Setenv("KMX_HOME", home)
			t.Setenv("PATH", "")
			t.Setenv("KUBECONFIG", filepath.Join(home, verifyCanary))
			opt := f.options()
			if strings.HasPrefix(mode, "digest") {
				opt.Suite = f.expected.Subject.Manifest.Digest
				opt.FromLayout = f.layout
			}
			if strings.HasSuffix(mode, "json") {
				opt.Output = "json"
			}
			var out bytes.Buffer
			if err := (&App{Out: &out}).VerifySuiteEvaluation(t.Context(), opt); err != nil {
				t.Fatal(err)
			}
			if opt.Output == "json" {
				var got suiteevidence.VerifiedEvaluation
				if err := suitecore.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				wantRuns := 1
				if strings.Contains(mode, "repeated") {
					wantRuns = 3
				}
				if got.Subject != f.expected.Subject || got.Cases != 1 || got.Runs != wantRuns || got.CompletedAt != "2026-01-01T00:01:00Z" || !got.Unsigned {
					t.Fatalf("summary=%+v", got)
				}
				want := verifyJSON(t, got)
				if !bytes.Equal(bytes.TrimSpace(out.Bytes()), want) {
					t.Fatal("JSON is not shared canonical serialization")
				}
			} else {
				for _, s := range []string{"evaluation: verified", "1 cases", "1 runs", "unsigned", "completedAt (unsigned claim): 2026-01-01T00:01:00Z"} {
					if !strings.Contains(out.String(), s) {
						t.Fatalf("summary missing %q: %q", s, out.String())
					}
				}
				if out.Len() > 1024 {
					t.Fatal("unbounded summary")
				}
			}
			if strings.Contains(out.String(), verifyCanary) {
				t.Fatal("payload in summary")
			}
			if _, err := os.Lstat(home); !os.IsNotExist(err) {
				t.Fatalf("KMX state created: %v", err)
			}
		})
	}
}

func TestSuiteEvaluationVerifyRefusalCodes(t *testing.T) {
	messages := map[string]string{
		"missing":     "required evaluation inputs or identities are missing",
		"malformed":   "evaluation inputs must be complete, valid regular files",
		"unsupported": "the evaluation format or policy is unsupported",
		"limit":       "evaluation inputs exceed verification limits",
		"artifact":    "a valid single-manifest local OCI layout is required",
		"subject":     "the artifact or execution does not match the reviewed expectations",
		"coverage":    "evidence does not cover the complete authored evaluation",
		"policy":      "evidence does not match the reviewed evaluation policy",
		"unknown":     "the evaluation contains unknown or incomplete outcomes",
		"not-passing": "the complete evaluation is not passing",
	}
	for _, code := range []string{"missing", "malformed", "unsupported", "limit", "artifact", "subject", "coverage", "policy", "unknown", "not-passing"} {
		t.Run(code, func(t *testing.T) {
			f := newEvaluationVerifyFixture(t, false, false)
			switch code {
			case "missing":
				f.expected.Subject.MemberID = ""
			case "malformed":
				f.evidence.Evaluator.CompletedAt = verifyCanary
			case "unsupported":
				f.expected.Evaluator.Policy.ID = "future-policy"
			case "artifact":
				if err := os.Remove(filepath.Join(f.layout, "oci-layout")); err != nil {
					t.Fatal(err)
				}
			case "subject":
				f.expected.Execution.Runtime.Version = "2.0.0"
			case "coverage":
				f.evidence.Results = []suiteevidence.EvidenceCaseResult{}
			case "policy":
				f.evidence.Evaluator.Identity.Version = "2.0.0"
			case "unknown":
				f.evidence.Results[0].Runs[0].Assertions[0].Outcome = "unknown"
			case "not-passing":
				f.evidence.Results[0].Outcome = "fail"
				f.evidence.Results[0].Runs[0].Outcome = "fail"
				f.evidence.Results[0].Runs[0].Assertions[0].Outcome = "fail"
			}
			f.save(t)
			if code == "limit" {
				verifyWrite(t, f.evidencePath, bytes.Repeat([]byte("x"), 4*1024*1024+1))
			}
			verifyRefusal(t, f.options(), code)
			err := (&App{Out: io.Discard}).VerifySuiteEvaluation(t.Context(), f.options())
			if want := "evaluation: not verified [" + code + "]: " + messages[code]; err == nil || err.Error() != want {
				t.Fatalf("refusal=%v, want %q", err, want)
			}
		})
	}
}

func TestSuiteEvaluationVerifyIndependentIdentities(t *testing.T) {
	for _, field := range []string{"manifest", "member", "composition", "build", "platform", "runtime", "harness", "renderer", "profile", "model", "execution-config", "evaluator", "judge", "judge-empty", "judge-config", "judge-observed", "judge-policy", "judge-split", "repeat-coverage"} {
		t.Run(field, func(t *testing.T) {
			f := newEvaluationVerifyFixture(t, true, true)
			code := "subject"
			switch field {
			case "manifest":
				f.expected.Subject.Manifest.Size++
			case "member":
				f.expected.Subject.MemberID = "other"
			case "composition":
				f.expected.Subject.CompositionDigest = "sha256:" + strings.Repeat("b", 64)
			case "build":
				f.expected.Subject.BuildProfileDigest = "sha256:" + strings.Repeat("b", 64)
			case "platform":
				f.expected.Subject.Platform.Architecture = "arm64"
			case "runtime":
				f.evidence.Execution.Runtime.ID = "substituted"
			case "harness":
				f.evidence.Execution.Harness.Version = "2.0.0"
			case "renderer":
				f.expected.Execution.Renderer.Digest = "sha256:" + strings.Repeat("b", 64)
			case "profile":
				f.expected.Execution.Profile.Version = "2.0.0"
			case "model":
				f.expected.Execution.Model.ConfigurationDigest = "sha256:" + strings.Repeat("b", 64)
			case "execution-config":
				f.expected.Execution.ConfigurationDigest = "sha256:" + strings.Repeat("b", 64)
			case "evaluator":
				f.expected.Evaluator.Policy.ConfigurationDigest = "sha256:" + strings.Repeat("b", 64)
				code = "policy"
			case "judge":
				f.expected.Judges[0].Version = "v2"
				code = "policy"
			case "judge-empty":
				f.expected.Judges = []suiteevidence.EvidenceJudgeConfiguration{}
				code = "policy"
			case "judge-config":
				f.evidence.Results[0].Runs[0].Assertions[0].Judge.Configuration.Policy.ConfigurationDigest = "sha256:" + strings.Repeat("b", 64)
				code = "policy"
			case "judge-observed":
				f.evidence.Results[0].Runs[0].Assertions[0].Judge.ObservedModels[0] = "other"
				code = "policy"
			case "judge-policy":
				f.evidence.Results[0].Runs[0].Assertions[0].Judge.Configuration.Policy.ID = "future-policy"
				code = "unsupported"
			case "judge-split":
				f.evidence.Results[0].Runs[0].Assertions[0].Judge.Scores = []int{60, 80}
				code = "unknown"
			case "repeat-coverage":
				f.evidence.Results[0].Runs = f.evidence.Results[0].Runs[:2]
				code = "coverage"
			}
			f.save(t)
			verifyRefusal(t, f.options(), code)
		})
	}
}

func TestSuiteEvaluationVerifyStrictJSON(t *testing.T) {
	for _, input := range []string{"expect", "evidence"} {
		for _, change := range []string{"empty", "whitespace", "invalid", "duplicate", "unknown", "null", "trailing", "oversize", "exact-bound"} {
			t.Run(input+"/"+change, func(t *testing.T) {
				f := newEvaluationVerifyFixture(t, false, false)
				path := f.expectPath
				if input == "evidence" {
					path = f.evidencePath
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				code := "malformed"
				switch change {
				case "empty":
					raw = nil
					code = "missing"
				case "whitespace":
					raw = []byte(" \n\t")
				case "invalid":
					raw = []byte(verifyCanary)
				case "duplicate":
					raw = append([]byte(`{"subject":{},`), raw[1:]...)
				case "unknown":
					raw = append([]byte(`{"`+verifyCanary+`":1,`), raw[1:]...)
				case "null":
					raw = []byte("null")
				case "trailing":
					raw = append(raw, []byte(` {"`+verifyCanary+`":1}`)...)
				case "oversize":
					raw = append(raw, bytes.Repeat([]byte(" "), 4*1024*1024+1-len(raw))...)
					code = "limit"
				case "exact-bound":
					raw = append(raw, bytes.Repeat([]byte(" "), 4*1024*1024-len(raw))...)
				}
				verifyWrite(t, path, raw)
				if change == "exact-bound" {
					var out bytes.Buffer
					if err := (&App{Out: &out}).VerifySuiteEvaluation(t.Context(), f.options()); err != nil {
						t.Fatal(err)
					}
					return
				}
				verifyRefusal(t, f.options(), code)
			})
		}
	}
}

func TestSuiteEvaluationVerifyExpectedRequiredPresence(t *testing.T) {
	f := newEvaluationVerifyFixture(t, true, false)
	raw := verifyJSON(t, f.expected)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// Every field, including optional-looking empty platform.variant and judges,
	// is required. Traverse maps and judge-array elements, omitting each in turn.
	var paths [][]string
	var visit func(map[string]any, []string)
	visit = func(m map[string]any, p []string) {
		for k, v := range m {
			next := append(append([]string{}, p...), k)
			paths = append(paths, next)
			if child, ok := v.(map[string]any); ok {
				visit(child, next)
			}
			if a, ok := v.([]any); ok && len(a) > 0 {
				visit(a[0].(map[string]any), append(next, "0"))
			}
		}
	}
	visit(doc, nil)
	for _, p := range paths {
		for _, change := range []string{"omit", "null"} {
			t.Run(strings.Join(p, ".")+"/"+change, func(t *testing.T) {
				var copy map[string]any
				if err := json.Unmarshal(raw, &copy); err != nil {
					t.Fatal(err)
				}
				var node any = copy
				for _, key := range p[:len(p)-1] {
					if key == "0" {
						node = node.([]any)[0]
					} else {
						node = node.(map[string]any)[key]
					}
				}
				m := node.(map[string]any)
				key := p[len(p)-1]
				if change == "omit" {
					delete(m, key)
				} else {
					m[key] = nil
				}
				verifyWrite(t, f.expectPath, verifyJSON(t, copy))
				verifyRefusal(t, f.options(), "malformed")
			})
		}
	}
}

func TestSuiteEvaluationVerifyJudgesRequiredArray(t *testing.T) {
	for _, value := range []string{"missing", "null", "empty"} {
		t.Run(value, func(t *testing.T) {
			f := newEvaluationVerifyFixture(t, false, false)
			var doc map[string]any
			if err := json.Unmarshal(verifyJSON(t, f.expected), &doc); err != nil {
				t.Fatal(err)
			}
			switch value {
			case "missing":
				delete(doc, "judges")
			case "null":
				doc["judges"] = nil
			}
			verifyWrite(t, f.expectPath, verifyJSON(t, doc))
			if value == "empty" {
				var out bytes.Buffer
				if err := (&App{Out: &out}).VerifySuiteEvaluation(t.Context(), f.options()); err != nil {
					t.Fatal(err)
				}
			} else {
				verifyRefusal(t, f.options(), "malformed")
			}
		})
	}
}

func TestSuiteEvaluationVerifyFileBoundaries(t *testing.T) {
	for _, input := range []string{"expect", "evidence"} {
		for _, kind := range []string{"missing", "directory", "symlink", "fifo", "parent-missing", "parent-file", "parent-fifo"} {
			t.Run(input+"/"+kind, func(t *testing.T) {
				f := newEvaluationVerifyFixture(t, false, false)
				opt := f.options()
				path := f.expectPath
				if input == "evidence" {
					path = f.evidencePath
				}
				code := "malformed"
				switch kind {
				case "missing":
					path = filepath.Join(t.TempDir(), verifyCanary)
					code = "missing"
				case "parent-missing":
					path = filepath.Join(t.TempDir(), verifyCanary, "input")
					code = "missing"
				case "parent-file":
					path = filepath.Join(path, verifyCanary)
				case "parent-fifo":
					parent := filepath.Join(t.TempDir(), verifyCanary)
					testFIFO(t, parent, 0644)
					path = filepath.Join(parent, "input")
				case "directory":
					path = t.TempDir()
				case "symlink":
					target := path
					path = filepath.Join(t.TempDir(), verifyCanary)
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				case "fifo":
					path = filepath.Join(t.TempDir(), verifyCanary)
					testFIFO(t, path, 0644)
				}
				if input == "expect" {
					opt.Expect = path
				} else {
					opt.Evidence = path
				}
				verifyRefusal(t, opt, code)
			})
		}
	}
}

func TestSuiteEvaluationVerifySelectors(t *testing.T) {
	for _, kind := range []string{"remote", "url", "invalid-digest", "uppercase-digest", "wrong-digest", "missing-layout", "extracted", "multiple-manifests", "path-from-layout", "blank-suite", "blank-evidence", "blank-expect", "bad-output"} {
		t.Run(kind, func(t *testing.T) {
			f := newEvaluationVerifyFixture(t, false, false)
			opt := f.options()
			code := "artifact"
			switch kind {
			case "remote":
				opt.Suite = "registry.example.invalid/team/suite:tag"
			case "url":
				opt.Suite = "https://" + verifyCanary + ".invalid/suite"
			case "invalid-digest":
				opt.Suite = "sha256:wrong"
			case "uppercase-digest":
				opt.Suite = "sha256:" + strings.Repeat("A", 64)
				opt.FromLayout = f.layout
			case "wrong-digest":
				opt.Suite = "sha256:" + strings.Repeat("b", 64)
				opt.FromLayout = f.layout
				opt.Evidence = filepath.Join(t.TempDir(), verifyCanary)
				testFIFO(t, opt.Evidence, 0644)
				code = "subject"
			case "missing-layout":
				opt.Suite = f.expected.Subject.Manifest.Digest
			case "extracted":
				opt.Suite = f.source
			case "multiple-manifests":
				path := filepath.Join(f.layout, "index.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var index ocispec.Index
				if err := json.Unmarshal(raw, &index); err != nil {
					t.Fatal(err)
				}
				index.Manifests = append(index.Manifests, index.Manifests[0])
				verifyWrite(t, path, verifyJSON(t, index))
			case "path-from-layout":
				opt.FromLayout = f.layout
			case "blank-suite":
				opt.Suite = " "
			case "blank-evidence":
				opt.Evidence = " "
			case "blank-expect":
				opt.Expect = " "
			case "bad-output":
				opt.Output = verifyCanary
			}
			if kind == "missing-layout" || kind == "path-from-layout" || strings.HasPrefix(kind, "blank-") || kind == "bad-output" {
				var out bytes.Buffer
				err := (&App{Out: &out}).VerifySuiteEvaluation(t.Context(), opt)
				var r *suiteevidence.EvidenceRefusal
				if err == nil || errors.As(err, &r) || out.Len() != 0 || strings.Contains(err.Error(), verifyCanary) {
					t.Fatalf("unsafe usage failure: %v %q", err, out.String())
				}
				return
			}
			verifyRefusal(t, opt, code)
		})
	}
}

type verifyFailWriter struct{ short bool }

func (w verifyFailWriter) Write(p []byte) (int, error) {
	if w.short {
		return 0, nil
	}
	return 0, errors.New(verifyCanary)
}
func TestSuiteEvaluationVerifyOutputFailureAndCancellation(t *testing.T) {
	f := newEvaluationVerifyFixture(t, false, false)
	for _, format := range []string{"text", "json"} {
		for _, short := range []bool{false, true} {
			t.Run(format+"/short="+map[bool]string{true: "true", false: "false"}[short], func(t *testing.T) {
				opt := f.options()
				opt.Output = format
				err := (&App{Out: verifyFailWriter{short: short}}).VerifySuiteEvaluation(t.Context(), opt)
				var r *suiteevidence.EvidenceRefusal
				if err == nil || errors.As(err, &r) || strings.Contains(err.Error(), verifyCanary) {
					t.Fatalf("unsafe output error: %v", err)
				}
			})
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	err := (&App{Out: &out}).VerifySuiteEvaluation(ctx, f.options())
	var r *suiteevidence.EvidenceRefusal
	if err == nil || errors.As(err, &r) || out.Len() != 0 {
		t.Fatalf("cancellation=%v output=%q", err, out.String())
	}
	if err := (&App{Out: io.Discard}).VerifySuiteEvaluation(context.Background(), f.options()); err != nil {
		t.Fatal(err)
	}
}

// Explicit opt-in export for a parent-controlled built-CLI smoke run. This test
// helper creates only public inputs and never gives production an export flag.
func TestSuiteEvaluationVerifySmokeFixture(t *testing.T) {
	dst := os.Getenv("KMX_VERIFY_TEST_FIXTURE_DIR")
	if dst == "" {
		t.Skip("fixture export not requested")
	}
	if !filepath.IsAbs(dst) {
		t.Fatal("fixture export requires an absolute new directory")
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Fatal("fixture export directory must not exist")
	}
	f := newEvaluationVerifyFixture(t, false, false)
	if err := os.CopyFS(dst, os.DirFS(filepath.Dir(f.layout))); err != nil {
		t.Fatal(err)
	}
	t.Logf("public smoke inputs: --suite %s --evidence %s --expect %s; digest %s", filepath.Join(dst, "layout"), filepath.Join(dst, "evidence.json"), filepath.Join(dst, "expected.json"), f.expected.Subject.Manifest.Digest)
}
