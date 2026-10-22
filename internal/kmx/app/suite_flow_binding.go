package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	suiteflow "github.com/agent-primitives/suite/experimental/flow"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

var errSuiteFlow = errors.New("experimental flow preflight refused")

// SuiteFlowOptions selects independent destination expectations, never evidence authority.
type SuiteFlowOptions struct {
	Workspace, Destination, Bindings, Evidence, Out, Flow, ArtifactReference string
	PlanOnly, Update                                                         bool
}
type suiteFlowReference struct {
	PlacementRef    string `yaml:"placementRef"`
	ModelServingRef string `yaml:"modelServingRef"`
}
type suiteFlowBinding struct {
	SchemaVersion string                        `yaml:"schemaVersion"`
	SuiteDigest   string                        `yaml:"suiteDigest"`
	Runtime       string                        `yaml:"runtime"`
	Members       map[string]suiteFlowReference `yaml:"members"`
	Approval      struct {
		Invokes []struct{ From, To string }   `yaml:"invokes"`
		Steps   []struct{ Flow, Step string } `yaml:"steps"`
	} `yaml:"approval"`
}
type suiteFlowMember struct {
	suite     workspace.NativeSuite
	reference suiteFlowReference
	subject   suiteNativeSubject
}
type suiteFlowSelection struct {
	suite, flowDigest, bindingDigest, operation string
	flow                                        suiteflow.FlowV1Alpha1
	members                                     []suiteFlowMember
	target                                      suiteNativeTarget
	candidates                                  []suiteFlowCandidateProjection
}

func readSuiteFlowBinding(ctx context.Context, filename string) (*suiteFlowBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, name, _, err := openSessionReferenceDirectory(filename)
	if err != nil {
		return nil, errSuiteFlow
	}
	defer root.Close()
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Size() > 64<<10 {
		return nil, errSuiteFlow
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, errSuiteFlow
	}
	defer dir.Close()
	file, err := openKagentFile(dir, name)
	if err != nil {
		return nil, errSuiteFlow
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errSuiteFlow
	}
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	after, statErr := file.Stat()
	current, pathErr := root.Lstat(name)
	if err != nil || statErr != nil || pathErr != nil || len(raw) > 64<<10 || int64(len(raw)) != opened.Size() || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) || !os.SameFile(after, current) {
		return nil, errSuiteFlow
	}
	var node yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&node) != nil || len(node.Content) != 1 || !validFlowBindingNode(node.Content[0], 0) {
		return nil, errSuiteFlow
	}
	var extra yaml.Node
	if decoder.Decode(&extra) != io.EOF {
		return nil, errSuiteFlow
	}
	var out suiteFlowBinding
	decoder = yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if decoder.Decode(&out) != nil || out.SchemaVersion != "kmx.suite-binding/v1alpha1" || out.Runtime != "orka" || out.Members == nil || out.Approval.Invokes == nil || out.Approval.Steps == nil {
		return nil, errSuiteFlow
	}
	return &out, ctx.Err()
}
func validFlowBindingNode(n *yaml.Node, depth int) bool {
	if depth > 8 || n.Anchor != "" || len(n.Content) > 256 {
		return false
	}
	switch n.Kind {
	case yaml.MappingNode:
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Value == "<<" || seen[k.Value] || !validFlowBindingNode(k, depth+1) || !validFlowBindingNode(n.Content[i+1], depth+1) {
				return false
			}
			seen[k.Value] = true
		}
		return n.Tag == "!!map"
	case yaml.SequenceNode:
		for _, child := range n.Content {
			if !validFlowBindingNode(child, depth+1) {
				return false
			}
		}
		return n.Tag == "!!seq"
	case yaml.ScalarNode:
		return n.Tag == "!!str" && n.Value != "" && secretshapes.Match(n.Value) == nil
	}
	return false
}

func supportedSuiteFlow(inv *workspace.FlowInventory, opt SuiteFlowOptions) bool {
	if inv == nil || len(inv.Members) != 2 || len(inv.Flow.Steps) != 2 || inv.Flow.ID != opt.Flow || inv.Flow.Mode != "pipeline" || !suitePrefixedDigest(inv.Digest) || !suitePrefixedDigest(inv.FlowDigest) || len(inv.Flow.EdgeRequirements) != 0 || opt.Evidence != "" || opt.Out != "" || opt.Update || opt.ArtifactReference != "" {
		return false
	}
	for _, step := range inv.Flow.Steps {
		if step.Approval == "required" {
			return false
		}
	}
	for i := range inv.Members {
		m := &inv.Members[i]
		if !suiteNativeOrkaInput(m) || m.RequireUnprofiled() != nil || m.RequirePolicyFree() != nil || len(m.RequiredImages) != 0 || m.Digest != inv.Digest {
			return false
		}
		for _, file := range m.Cases {
			c, err := workspace.DecodePackagedCase(file.Bytes)
			if err != nil {
				return false
			}
			for _, a := range c.Assertions {
				if a.Type != "contains" && a.Type != "notContains" && a.Type != "regex" {
					return false
				}
			}
		}
	}
	return true
}

func sameFlowTarget(t config.Destination, selected config.Destination) bool {
	return t.Context != "" && t.Namespace != "" && t.Context == selected.Context && t.Namespace == selected.Namespace && (t.Runtime == "" || t.Runtime == "orka") && t.Bindings == ""
}
func (a *App) prepareSuiteFlow(ctx context.Context, inv *workspace.FlowInventory, opt SuiteFlowOptions) (selection *suiteFlowSelection, err error) {
	defer func() {
		if ctx.Err() != nil {
			selection = nil
			err = ctx.Err()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a == nil || a.Cfg == nil || a.Run == nil || !supportedSuiteFlow(inv, opt) {
		return nil, errSuiteFlow
	}
	binding, err := readSuiteFlowBinding(ctx, opt.Bindings)
	if err != nil {
		return nil, err
	}
	if binding.SuiteDigest != inv.Digest || len(binding.Members) != len(inv.Members) || len(binding.Approval.Invokes) != 0 || len(binding.Approval.Steps) != 0 {
		return nil, errSuiteFlow
	}
	destinations, err := config.LoadDestinations(opt.Workspace)
	if err != nil {
		return nil, errSuiteFlow
	}
	selected, err := destinations.Target(opt.Destination)
	if err != nil || !sameFlowTarget(selected, selected) || selected.Context != a.Cfg.KubeContext {
		return nil, errSuiteFlow
	}
	for _, name := range []string{destinations.EvalTarget, selected.EvaluationTarget} {
		if name != "" {
			t, err := destinations.Target(name)
			if err != nil || !sameFlowTarget(t, selected) {
				return nil, errSuiteFlow
			}
		}
	}
	references := make([]suiteFlowReference, len(inv.Members))
	for i, m := range inv.Members {
		ref, ok := binding.Members[m.MemberID]
		t, err := destinations.Target(ref.PlacementRef)
		provider := strings.TrimPrefix(ref.ModelServingRef, "provider:")
		if !ok || err != nil || !sameFlowTarget(t, selected) || !strings.HasPrefix(ref.ModelServingRef, "provider:") || a.validateSuiteNativeSelectors(t.Namespace, provider) != nil || t.Inference != "" && t.Inference != ref.ModelServingRef || selected.Inference != "" && selected.Inference != ref.ModelServingRef {
			return nil, errSuiteFlow
		}
		references[i] = ref
	}
	// Clone only already-validated, unprofiled native values; no wire reconstruction.
	raw, err := json.Marshal(inv)
	if err != nil {
		return nil, errSuiteFlow
	}
	var frozen workspace.FlowInventory
	if json.Unmarshal(raw, &frozen) != nil {
		return nil, errSuiteFlow
	}
	s := &suiteFlowSelection{suite: inv.Digest, flowDigest: inv.FlowDigest, flow: frozen.Flow}
	s.operation, err = randomHex(16)
	if err != nil {
		return nil, errSuiteFlow
	}
	for i, m := range frozen.Members {
		target, observed, runtime, err := a.readSuiteNativeInputs(ctx, selected.Namespace, strings.TrimPrefix(references[i].ModelServingRef, "provider:"))
		if err != nil {
			return nil, errSuiteFlow
		}
		subject := suiteNativeSubject{Suite: m.Name, SuiteDigest: m.Digest, Member: m.MemberID, Model: m.Model, Renderer: m.RendererVersion, Target: target, Binding: observed, Runtime: runtime}
		if err := a.verifyReleaseServingModel(ctx, subject); err != nil {
			return nil, errSuiteFlow
		}
		if i > 0 && target != s.target {
			return nil, errSuiteFlow
		}
		s.target = target
		s.members = append(s.members, suiteFlowMember{m, references[i], subject})
	}
	s.bindingDigest, err = s.semanticDigest()
	if err != nil {
		return nil, errSuiteFlow
	}
	if err := s.prepareCandidates(); err != nil {
		return nil, err
	}
	if err := s.revalidate(ctx, a); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *suiteFlowSelection) semanticDigest() (string, error) {
	type member struct {
		ID, Model, Protocol, Renderer, MemberDigest, Instructions, Composition, BuildProfile string
		Reference                                                                            suiteFlowReference
		Binding                                                                              suiteNativeBinding
		Runtime                                                                              suiteNativeRuntime
	}
	frame := struct {
		Target  struct{ ClusterUID, Namespace, NamespaceUID string }
		Members []member
	}{}
	frame.Target.ClusterUID = s.target.ClusterUID
	frame.Target.Namespace = s.target.Namespace
	frame.Target.NamespaceUID = s.target.NamespaceUID
	for _, m := range s.members {
		binding, runtime := m.subject.Binding, m.subject.Runtime
		binding.Provider.ResourceVersion = ""
		binding.Provider.Generation = 0
		binding.Secret.ResourceVersion = ""
		runtime.ControllerGeneration = 0
		runtime.ControllerReplicas = 0
		frame.Members = append(frame.Members, member{m.suite.MemberID, m.suite.Model, "openai-compatible", m.suite.RendererVersion, m.suite.MemberDigest, m.suite.InstructionsDigest, m.suite.CompositionDigest, m.suite.BuildProfileDigest, m.reference, binding, runtime})
	}
	return suiteNativeDigest("kmx.suite-binding/v1alpha1", frame)
}
func (s *suiteFlowSelection) revalidate(ctx context.Context, a *App) (err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, m := range s.members {
		if a.verifySuiteNativeInputs(ctx, m.subject) != nil || a.verifyReleaseServingModel(ctx, m.subject) != nil {
			return errSuiteFlow
		}
	}
	return ctx.Err()
}
