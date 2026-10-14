package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Review retains the packaged selection and its source fingerprint, not proof
// of execution. No cluster configuration, placement, or Task is involved.
type suiteWorkspaceReview struct {
	root      string
	prepared  *workspace.Prepared
	suite     *workspace.NativeSuite
	inventory []suitePassingInventoryCase
	image     *workspace.ImageSuite
}

func (a *App) reviewSuiteWorkspace(ctx context.Context, root string) (*suiteWorkspaceReview, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("select an explicit authored workspace root; kmx create authors offline separately")
	}
	resolved, err := resolveSuiteWorkspace(root)
	if err != nil {
		return nil, err
	}
	state, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	prepared, err := workspace.Prepare(ctx, resolved, filepath.Join(state, "suites"))
	if err != nil {
		return nil, err
	}
	suite, err := workspace.Load(ctx, prepared)
	if err != nil {
		artifact, readErr := suiteoras.ReadArtifactLayout(ctx, prepared.Path, prepared.Descriptor.Digest.String())
		if readErr != nil {
			return nil, err
		}
		image, imageErr := workspace.LoadImageArtifact(ctx, artifact)
		if imageErr != nil {
			return nil, err
		}
		inventory, _, imageErr := imageInventory(image)
		if imageErr != nil {
			return nil, imageErr
		}
		return &suiteWorkspaceReview{root: resolved, prepared: prepared, image: image, inventory: inventory}, nil
	}
	if !suiteNativeOrkaInput(suite) {
		return nil, fmt.Errorf("console workspace review supports native Orka only; kagent evaluation is unavailable")
	}
	inventory, _, err := suitePassingInventory(suite)
	if err != nil {
		return nil, err
	}
	review := &suiteWorkspaceReview{root: resolved, prepared: prepared, suite: suite, inventory: inventory}
	if err := review.checkCurrent(ctx); err != nil {
		return nil, err
	}
	return review, nil
}

func (r *suiteWorkspaceReview) checkCurrent(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("workspace review required")
	}
	return workspace.CheckCurrent(ctx, r.root, r.prepared)
}

// Large public operands are explicitly marked as previews. Every case and every
// assertion still has its identity and definition digest displayed.
func suiteReviewPreview(value string) string {
	const limit = 2048
	if len(value) <= limit {
		return strconv.Quote(value)
	}
	prefix := value[:limit]
	for !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return strconv.Quote(prefix) + fmt.Sprintf(" [preview: %d bytes total; inspect full authored file]", len(value))
}

func (r *suiteWorkspaceReview) sourceLines(width int) []string {
	if r.image != nil {
		fields := []string{"IMAGE WORKSPACE / SOURCE REVIEW", "Root: " + r.root, "Member: " + r.image.MemberID, "Suite: " + r.image.Digest, "Execution: image-http; concrete inference comes from reviewed environment bindings.", fmt.Sprintf("Complete inventory: %d cases", len(r.inventory)), "Preparation builds/publishes an immutable candidate, places a fresh evaluation workload and executes every case. Evidence authorizes exact-placement reuse, not cross-target promotion."}
		var rows []string
		fields = append(fields, "Instructions (public content): "+suiteReviewPreview(string(r.image.Instructions)))
		for _, field := range fields {
			rows = append(rows, agentTUIIndentedText(field, width, 0)...)
		}
		return rows
	}
	fields := []string{
		"WORKSPACE / SOURCE REVIEW",
		"Root: " + r.root,
		"Member: " + r.suite.MemberID + " · Model: " + r.suite.Model,
		"Suite: " + r.suite.Digest,
		"Member digest: " + r.suite.MemberDigest,
		"Instructions digest: " + r.suite.InstructionsDigest,
		"Instructions (public content): " + suiteReviewPreview(string(r.suite.Instructions)),
		fmt.Sprintf("Complete inventory: %d cases (each reviewed next)", len(r.inventory)),
		"Cases are public content, not private recorded conversations.",
		"Scaffold examples are not quality proof; author cases appropriate to these instructions.",
		"Future eval executes one model Task per case and may place Provider/Agent resources. This review executes no Tasks.",
	}
	var rows []string
	for _, field := range fields {
		rows = append(rows, agentTUIIndentedText(field, width, 0)...)
	}
	return rows
}

func (r *suiteWorkspaceReview) digest() string {
	if r.image != nil {
		return r.image.Digest
	}
	return r.suite.Digest
}

func (r *suiteWorkspaceReview) caseLines(index, width int) []string {
	c := r.inventory[index]
	fields := []string{
		fmt.Sprintf("CASE REVIEW %d/%d · %s", index+1, len(r.inventory), c.Case.ID),
		"File: " + c.Metadata.Path,
		"Case digest: " + c.Metadata.CaseDigest,
		"Input digest: " + c.Metadata.InputDigest,
		"Input (public content): " + suiteReviewPreview(c.Case.Input),
	}
	for _, assertion := range agentruntime.EvaluationCaseAssertions(c.Case) {
		operand := assertion.Value
		if assertion.Type == "regex" {
			operand = assertion.Pattern
		}
		fields = append(fields, "Assertion: "+assertion.ID+" · "+assertion.Type+" · "+suiteReviewPreview(operand),
			"Definition digest: "+agentruntime.EvaluationAssertionDigest(assertion))
	}
	var rows []string
	for _, field := range fields {
		rows = append(rows, agentTUIIndentedText(field, width, 0)...)
	}
	return rows
}
