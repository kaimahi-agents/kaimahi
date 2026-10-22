package suiteview

import (
	"context"
	"io"

	suitecore "github.com/agent-primitives/suite/core"
	suiteflow "github.com/agent-primitives/suite/experimental/flow"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/localartifact"
)

// FlowV1Alpha1View contains copied experimental definitions and every portable
// member recipe in suite order. It grants no execution or approval authority.
type FlowV1Alpha1View struct {
	Artifact   ArtifactIdentity
	Suite      SuiteIdentity
	FlowDigest string
	Flow       suiteflow.FlowV1Alpha1
	Members    []MemberRecipe
}

// LoadLayoutFlowV1Alpha1 explicitly selects the experimental flow contract on
// one local OCI artifact. An omitted digest requires an unambiguous root.
func LoadLayoutFlowV1Alpha1(ctx context.Context, layout, manifestDigest string) (FlowV1Alpha1View, error) {
	if err := validateFlowSelection(ctx, manifestDigest); err != nil {
		return FlowV1Alpha1View{}, err
	}
	artifact, err := localartifact.ReadLayoutFlowV1Alpha1(ctx, layout, manifestDigest)
	if err != nil {
		return FlowV1Alpha1View{}, refusalFor(ctx, err)
	}
	return projectFlowV1Alpha1(ctx, artifact)
}

// LoadArchiveFlowV1Alpha1 freezes a bounded uncompressed local OCI archive and
// projects the original graph. It never selects a member subset.
func LoadArchiveFlowV1Alpha1(ctx context.Context, source io.ReaderAt, size int64, manifestDigest string) (FlowV1Alpha1View, error) {
	if err := validateFlowSelection(ctx, manifestDigest); err != nil {
		return FlowV1Alpha1View{}, err
	}
	artifact, err := localartifact.ReadArchiveFlowV1Alpha1(ctx, source, size, manifestDigest)
	if err != nil {
		return FlowV1Alpha1View{}, refusalFor(ctx, err)
	}
	return projectFlowV1Alpha1(ctx, artifact)
}

func validateFlowSelection(ctx context.Context, digest string) error {
	if err := ctx.Err(); err != nil {
		return &Refusal{Code: RefusalCanceled, cause: err}
	}
	if digest != "" && !suitecore.IsValidDigest(digest) {
		return &Refusal{Code: RefusalSelection}
	}
	return nil
}

func projectFlowV1Alpha1(ctx context.Context, artifact *localartifact.Artifact) (FlowV1Alpha1View, error) {
	graph := artifact.FlowV1Alpha1()
	if graph == nil {
		return FlowV1Alpha1View{}, &Refusal{Code: RefusalArtifact}
	}
	files := artifact.Files()
	var coreSuite suitecore.Suite
	if suitecore.Unmarshal(files["agentsuite.json"], &coreSuite) != nil {
		return FlowV1Alpha1View{}, &Refusal{Code: RefusalArtifact}
	}
	descriptor := artifact.Descriptor()
	view := FlowV1Alpha1View{Artifact: ArtifactIdentity{MediaType: descriptor.MediaType, Digest: descriptor.Digest.String(), Size: descriptor.Size}, FlowDigest: graph.FlowDigest(), Flow: graph.Flow()}
	for _, ref := range coreSuite.Agents {
		recipe, err := ProjectMember(ctx, files, ref.ID)
		if err != nil {
			return FlowV1Alpha1View{}, err
		}
		recipe.Artifact = view.Artifact
		view.Suite = recipe.Suite
		view.Members = append(view.Members, recipe)
	}
	if err := ctx.Err(); err != nil {
		return FlowV1Alpha1View{}, refusalFor(ctx, err)
	}
	return view, nil
}
