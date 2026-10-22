package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	suitecore "github.com/agent-primitives/suite/core"
	suiteevidence "github.com/agent-primitives/suite/evidence"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// SuiteEvaluationExportOptions selects one offline, exact-source public export.
type SuiteEvaluationExportOptions struct{ Workspace, Member, Out string }

var (
	errSuitePublicSubject     = errors.New("public evaluation export refused: artifact or member mismatch; select the sole packaged member and re-evaluate unchanged source")
	errSuitePublicProof       = errors.New("public evaluation export refused: latest exact-suite proof incomplete; use a tagged module/checksum or clean VCS-recorded kmx build and re-evaluate")
	errSuitePublicProjection  = errors.New("public evaluation export refused: unsupported public identity or policy; use supported token identities and re-evaluate")
	errSuitePublicOutput      = errors.New("public evaluation export refused: unsafe or occupied output; choose a new file outside workspace, cache and managed state in a non-writable-to-others directory")
	errSuitePublicPersistence = errors.New("public evaluation export incomplete: output may exist; inspect the chosen file before selecting a new output")
	errSuitePublicCancelled   = errors.New("public evaluation export cancelled; retry offline with a new output file")
	suitePublicPrivateToken   = regexp.MustCompile(`(?i)^(sk[-_]|gh[pousr]_|github_pat_|AKIA|ASIA|eyJ)|^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}$|^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$`)
)

func suitePublicID(s string) bool {
	return suitePublicToken.MatchString(s) && !suitePublicPrivateToken.MatchString(s)
}

// ExportSuiteEvaluation reads prepared local OCI and private historical proof;
// it never loads application config, dispatches execution or contacts a registry.
func (a *App) ExportSuiteEvaluation(ctx context.Context, opt SuiteEvaluationExportOptions) (err error) {
	if ctx.Err() != nil {
		return errSuitePublicCancelled
	}
	if opt.Out == "" || !suitePublicID(opt.Member) {
		return errSuitePublicSubject
	}
	root, e := resolveSuiteWorkspace(opt.Workspace)
	if e != nil {
		return errSuitePublicSubject
	}
	state, e := config.StateDir()
	if e != nil {
		return errSuitePublicProof
	}
	cache := filepath.Join(state, "suites")
	if suiteReceiptInside(opt.Out, filepath.Join(state, "passing")) {
		return errSuitePublicOutput
	}
	prepared, e := workspace.Prepare(ctx, root, cache)
	if errors.Is(e, workspace.ErrCacheMigration) {
		return workspace.ErrCacheMigration
	}
	if e != nil {
		return errSuitePublicSubject
	}
	artifact, e := suiteoras.ReadArtifactLayout(ctx, prepared.Path, prepared.Descriptor.Digest.String())
	if e != nil {
		return errSuitePublicSubject
	}
	suite, e := workspace.LoadArtifact(ctx, artifact)
	if errors.Is(e, workspace.ErrProfileSelectionRequired) {
		return workspace.ErrProfileExecutionUnsupported
	}
	if e != nil || suite.MemberID != opt.Member {
		return errSuitePublicSubject
	}
	store, e := openSuitePassStore(ctx, root, cache, suite.Digest)
	if e != nil {
		return errSuitePublicProof
	}
	defer func() {
		if store.Close() != nil {
			err = errSuitePublicPersistence
		}
	}()
	receipt, _, e := store.latest(ctx, suite)
	if e != nil {
		return errSuitePublicProof
	}
	metadata, e := store.readPublicMetadata(ctx, suite)
	if e != nil {
		return errSuitePublicProof
	}
	evidence, expected, e := suitePublicEvidence(ctx, artifact, suite, receipt, metadata)
	if e != nil {
		return e
	}
	raw, e := suitecore.Marshal(evidence)
	if e != nil {
		return errSuitePublicProjection
	}
	if _, e := suiteevidence.VerifyEvaluation(prepared.Path, expected, bytes.NewReader(raw)); e != nil {
		return errSuitePublicProjection
	}
	// The publisher is not even opened until complete shared verification succeeds.
	output, e := preflightSuitePublicExport(ctx, opt.Out, root, cache)
	if e != nil {
		return errSuitePublicOutput
	}
	defer func() {
		if output.Close() != nil {
			err = errSuitePublicPersistence
		}
	}()
	captured, e := suitecore.Marshal(metadata)
	if e != nil {
		return errSuitePublicProof
	}
	output.beforeLink = func() error {
		if workspace.CheckCurrent(ctx, root, prepared) != nil {
			return errSuitePublicSubject
		}
		current, e := store.readPublicMetadata(ctx, suite)
		if e != nil {
			return errSuitePublicProof
		}
		now, e := suitecore.Marshal(current)
		if e != nil || !bytes.Equal(captured, now) {
			return errSuitePublicProof
		}
		return nil
	}
	if e := output.publishBytes(ctx, raw); e != nil {
		if output.linked {
			return errSuitePublicPersistence
		}
		if ctx.Err() != nil {
			return errSuitePublicCancelled
		}
		return errSuitePublicOutput
	}
	if a.Out != nil {
		if _, e := fmt.Fprintln(a.Out, "Public evaluation evidence exported (unsigned consistency claim; not authentication or binary attestation)."); e != nil {
			return errSuitePublicPersistence
		}
	}
	return nil
}

func suitePublicImplementation(label, digest string) (suitecore.EvidenceImplementation, error) {
	i := strings.LastIndex(label, "/")
	if i < 1 || !suitePublicID(label[:i]) || !suitePublicID(label[i+1:]) || !suitePrefixedDigest(digest) {
		return suitecore.EvidenceImplementation{}, errSuitePublicProjection
	}
	return suitecore.EvidenceImplementation{ID: label[:i], Version: label[i+1:], Digest: digest}, nil
}
func suitePublicRuntime(r *suitePassingReceipt) (runtime, harness suitecore.EvidenceImplementation, err error) {
	if r == nil {
		return runtime, harness, errSuitePublicProjection
	}
	switch r.ExecutionMode {
	case "native-orka":
		s := r.Subject
		if !suiteNativePinnedRuntime(s.Runtime, s.Target.Namespace) {
			return runtime, harness, errSuitePublicProjection
		}
		runtime = suitecore.EvidenceImplementation{ID: "orka", Version: "0.2.0", Digest: orkaControllerDigest}
		harness = suitecore.EvidenceImplementation{ID: "orka", Version: "harness-v2", Digest: orkaAIWorkerDigest}
	case "native-kagent":
		if !validSuiteKagentPassingProof(r.Kagent) {
			return runtime, harness, errSuitePublicProjection
		}
		p := kagentLiftProfile()
		// Equivalent qualified observed imageIDs select the canonical image-list pin,
		// never an arbitrary pod/platform/config digest from the private inventory.
		runtime = suitecore.EvidenceImplementation{ID: "kagent", Version: p.Version, Digest: "sha256:" + p.ControllerDigests[0]}
		harness = suitecore.EvidenceImplementation{ID: "kagent-go", Version: p.Version, Digest: "sha256:" + p.GoDigests[0]}
	case "native-agentsessions":
		if !validSuiteSessionsPassingProof(r.Sessions) {
			return runtime, harness, errSuitePublicProjection
		}
		h := r.Sessions.Subject.Host
		runtime = suitecore.EvidenceImplementation{ID: "agentsessions", Version: h.RuntimeVersion, Digest: h.RuntimeBinaryDigest}
		harness = suitecore.EvidenceImplementation{ID: "chat", Version: "1", Digest: h.RuntimeBinaryDigest}
	default:
		return runtime, harness, errSuitePublicProjection
	}
	return runtime, harness, nil
}

// Every digest below is a new closed NON-SECRET projection. None copies private
// binding/render/judge configuration digests, endpoints, raw payloads or IDs.
type suitePublicSourceProjection struct {
	Domain   string              `json:"domain"`
	Producer suitePublicProducer `json:"producer"`
}
type suitePublicModelProjection struct {
	Domain   string                           `json:"domain"`
	Protocol string                           `json:"protocol"`
	Model    string                           `json:"model"`
	Profile  suitecore.EvidenceImplementation `json:"profile"`
	Harness  suitecore.EvidenceImplementation `json:"harness"`
	ToolFree bool                             `json:"toolFree"`
	Sampling string                           `json:"sampling"`
}
type suitePublicExecutionProjection struct {
	Domain       string                           `json:"domain"`
	Suite        string                           `json:"suite"`
	Member       string                           `json:"member"`
	Instructions string                           `json:"instructions"`
	Runtime      suitecore.EvidenceImplementation `json:"runtime"`
	Harness      suitecore.EvidenceImplementation `json:"harness"`
	Renderer     suitecore.EvidenceImplementation `json:"renderer"`
	Profile      suitecore.EvidenceImplementation `json:"profile"`
	Model        suiteevidence.EvidenceModel      `json:"model"`
}
type suitePublicEvaluatorProjection struct {
	Domain      string `json:"domain"`
	Source      string `json:"source"`
	TextPolicy  string `json:"textPolicy"`
	UnknownVeto bool   `json:"unknownVeto"`
}
type suitePublicJudgeProjection struct {
	Domain              string `json:"domain"`
	Model               string `json:"model"`
	Version             string `json:"version"`
	Policy              string `json:"policy"`
	RequestStyle        string `json:"requestStyle"`
	Samples             int    `json:"samples"`
	MaxCompletionTokens int    `json:"maxCompletionTokens"`
	Stream              bool   `json:"stream"`
	Sampling            string `json:"sampling"`
	MinScore            int    `json:"minScore"`
}

func suitePublicJudge(model, version string, threshold int) (suiteevidence.EvidenceJudgeConfiguration, error) {
	if !suitePublicID(model) || !suitePublicID(version) {
		return suiteevidence.EvidenceJudgeConfiguration{}, errSuitePublicProjection
	}
	digest, e := suitePublicDigest(suitePublicJudgeProjection{"kmx.public-judge-config/v1", model, version, agentruntime.EvaluationJudgeEvaluatorDigest(), agentruntime.EvaluationJudgeRequestStyle, 2, 1024, false, "no-override", threshold})
	if e != nil {
		return suiteevidence.EvidenceJudgeConfiguration{}, e
	}
	return suiteevidence.EvidenceJudgeConfiguration{Model: model, Version: version, Policy: suiteevidence.EvidencePolicy{ID: suiteevidence.EvidenceJudgePolicyID, Version: suiteevidence.EvidenceJudgePolicyVersion, ConfigurationDigest: digest}}, nil
}

// Expectations are resolved from verified artifact bytes, captured provenance
// and validated runtime observations, independently of the document/result rows.
func suitePublicExpected(artifact *suiteoras.Artifact, suite *workspace.NativeSuite, r *suitePassingReceipt, m *suitePublicMetadata) (suiteevidence.ExpectedEvaluation, error) {
	var expected suiteevidence.ExpectedEvaluation
	if artifact == nil || suite == nil || m == nil {
		return expected, errSuitePublicProjection
	}
	version, err := m.Producer.publicVersion()
	if err != nil {
		return expected, errSuitePublicMetadata
	}
	files := artifact.Files()
	var graph suitecore.Suite
	if suitecore.Unmarshal(files["agentsuite.json"], &graph) != nil || len(graph.Agents) != 1 || len(graph.Compositions) != 1 || len(graph.BuildProfiles) != 1 {
		return expected, errSuitePublicSubject
	}
	var member suitecore.Agent
	var composition suitecore.Composition
	if suitecore.Unmarshal(files[graph.Agents[0].Path], &member) != nil || suitecore.Unmarshal(files[graph.Compositions[0].Path], &composition) != nil {
		return expected, errSuitePublicSubject
	}
	descriptor := artifact.Descriptor()
	expected.Subject = suiteevidence.EvidenceSubject{Manifest: suiteevidence.EvidenceDescriptor{MediaType: descriptor.MediaType, Digest: descriptor.Digest.String(), Size: descriptor.Size}, MemberID: member.ID, MemberDigest: graph.Agents[0].Descriptor.Digest, CompositionDigest: graph.Compositions[0].Descriptor.Digest, BuildProfileDigest: graph.BuildProfiles[0].Descriptor.Digest, Platform: suiteevidence.EvidencePlatform{OS: composition.Platform.OS, Architecture: composition.Platform.Architecture, Variant: composition.Platform.Variant}}
	runtime, harness, err := suitePublicRuntime(r)
	if err != nil {
		return expected, err
	}
	source, err := suitePublicDigest(suitePublicSourceProjection{"kmx.go-build-source/v1", m.Producer})
	if err != nil {
		return expected, err
	}
	renderer, err := suitePublicImplementation(suite.RendererVersion, source)
	if err != nil {
		return expected, err
	}
	label := suite.ExecutionProfile
	if suite.Runtime.ID == "orka" {
		label = "kmx.orka-harness-v2-tool-free-openai/v1"
	}
	profile, err := suitePublicImplementation(label, source)
	if err != nil {
		return expected, err
	}
	if !suitePublicID(member.Model.Protocol) || !suitePublicID(member.Model.Model) {
		return expected, errSuitePublicProjection
	}
	modelDigest, err := suitePublicDigest(suitePublicModelProjection{"kmx.public-model-config/v1", member.Model.Protocol, member.Model.Model, profile, harness, true, "runtime-default"})
	if err != nil {
		return expected, err
	}
	model := suiteevidence.EvidenceModel{Protocol: member.Model.Protocol, ID: member.Model.Model, ConfigurationDigest: modelDigest}
	executionDigest, err := suitePublicDigest(suitePublicExecutionProjection{"kmx.public-execution-config/v1", descriptor.Digest.String(), member.ID, member.Instructions.Descriptor.Digest, runtime, harness, renderer, profile, model})
	if err != nil {
		return expected, err
	}
	expected.Execution = suiteevidence.EvidenceExecution{Runtime: runtime, Harness: harness, Renderer: renderer, Profile: profile, Model: model, ConfigurationDigest: executionDigest}
	policyDigest, err := suitePublicDigest(suitePublicEvaluatorProjection{"kmx.public-evaluator-config/v1", source, suitePassingEvaluatorVersion, true})
	if err != nil {
		return expected, err
	}
	expected.Evaluator = suiteevidence.EvidenceEvaluatorIdentity{ID: "kmx", Version: version, Policy: suiteevidence.EvidencePolicy{ID: suiteevidence.EvidencePolicyID, Version: suiteevidence.EvidencePolicyVersion, ConfigurationDigest: policyDigest}}
	expected.Judges = []suiteevidence.EvidenceJudgeConfiguration{}
	seenJudges := map[suiteevidence.EvidenceJudgeConfiguration]bool{}
	for _, file := range suite.Cases {
		c, e := workspace.DecodePackagedCase(file.Bytes)
		if e != nil {
			return expected, errSuitePublicProjection
		}
		for _, a := range c.Assertions {
			if a.Type != "llmJudge" {
				continue
			}
			if a.MinScore == nil {
				return expected, errSuitePublicProjection
			}
			j, e := suitePublicJudge(a.JudgeModel, a.JudgeVersion, *a.MinScore)
			if e != nil {
				return expected, e
			}
			if !seenJudges[j] {
				expected.Judges = append(expected.Judges, j)
				seenJudges[j] = true
			}
		}
	}
	return expected, nil
}

func suitePublicEvidence(ctx context.Context, artifact *suiteoras.Artifact, suite *workspace.NativeSuite, r *suitePassingReceipt, m *suitePublicMetadata) (suiteevidence.EvaluationEvidence, suiteevidence.ExpectedEvaluation, error) {
	if err := suite.RequireUnprofiled(); err != nil {
		return suiteevidence.EvaluationEvidence{}, suiteevidence.ExpectedEvaluation{}, err
	}
	var evidence suiteevidence.EvaluationEvidence
	var empty suiteevidence.ExpectedEvaluation
	if _, err := canonicalSuitePassingReceipt(ctx, r, suite); err != nil {
		return evidence, empty, errSuitePublicProof
	}
	expected, err := suitePublicExpected(artifact, suite, r, m)
	if err != nil {
		return evidence, empty, err
	}
	// Claims use independently resolved expectations, never the reverse. Result
	// rows below cannot expand the accepted subject, identities or judge policy.
	evidence = suiteevidence.EvaluationEvidence{SchemaVersion: suiteevidence.EvidenceSchemaVersion, MediaType: suiteevidence.MediaTypeEvaluationEvidence, Subject: expected.Subject, Execution: expected.Execution, Evaluator: suiteevidence.EvidenceEvaluator{Identity: expected.Evaluator, StartedAt: m.StartedAt, CompletedAt: m.CompletedAt}, Results: []suiteevidence.EvidenceCaseResult{}}
	files := artifact.Files()
	var member suitecore.Agent
	if suitecore.Unmarshal(files["agents/"+suite.MemberID+".json"], &member) != nil || len(member.Evals) != 1 {
		return evidence, empty, errSuitePublicSubject
	}
	indexRef := member.Evals[0]
	var index suitecore.EvaluationIndex
	if suitecore.Unmarshal(files[indexRef.Path], &index) != nil {
		return evidence, empty, errSuitePublicSubject
	}
	evidence.Evaluation = suiteevidence.EvidenceEvaluation{Index: suiteevidence.EvidenceFileRef{Path: indexRef.Path, Descriptor: suiteevidence.EvidenceDescriptor{MediaType: indexRef.Descriptor.MediaType, Digest: indexRef.Descriptor.Digest, Size: indexRef.Descriptor.Size}}, Cases: []suiteevidence.EvidenceCaseRef{}}
	row := 0
	for _, ref := range index.Cases {
		c, err := workspace.DecodePackagedCase(files[ref.Path])
		if err != nil {
			return evidence, empty, errSuitePublicProjection
		}
		runs, minPasses := c.RunPolicy()
		evidence.Evaluation.Cases = append(evidence.Evaluation.Cases, suiteevidence.EvidenceCaseRef{ID: c.ID, Path: ref.Path, Descriptor: suiteevidence.EvidenceDescriptor{MediaType: ref.Descriptor.MediaType, Digest: ref.Descriptor.Digest, Size: ref.Descriptor.Size}, Runs: runs, MinPasses: minPasses})
		result := suiteevidence.EvidenceCaseResult{ID: c.ID, Outcome: "pass", Runs: []suiteevidence.EvidenceRunResult{}}
		for run := 1; run <= runs; run++ {
			if row >= len(r.Cases) {
				return evidence, empty, errSuitePublicProof
			}
			private := r.Cases[row]
			row++
			observed := suiteevidence.EvidenceRunResult{Number: run, Status: "completed", Outcome: private.Verdict, Assertions: []suiteevidence.EvidenceAssertionResult{}}
			for _, a := range private.Assertions {
				assertion := suiteevidence.EvidenceAssertionResult{ID: a.ID, Type: a.Type, Outcome: a.Verdict}
				if a.Judge != nil {
					j := a.Judge
					configuration, e := suitePublicJudge(j.Model, j.ModelVersion, j.MinScore)
					if e != nil {
						return evidence, empty, e
					}
					models := []string{}
					for _, match := range j.ModelMatches {
						switch match {
						case "requested":
							models = append(models, j.Model)
						case "version-qualified":
							models = append(models, j.Model+"-"+j.ModelVersion)
						default:
							return evidence, empty, errSuitePublicProjection
						}
					}
					assertion.Judge = &suiteevidence.EvidenceJudgeResult{Configuration: configuration, ObservedModels: models, Scores: append([]int{}, j.Scores...), MinScore: j.MinScore}
				}
				observed.Assertions = append(observed.Assertions, assertion)
			}
			result.Runs = append(result.Runs, observed)
		}
		evidence.Results = append(evidence.Results, result)
	}
	if row != len(r.Cases) {
		return evidence, empty, errSuitePublicProof
	}
	return evidence, expected, nil
}
