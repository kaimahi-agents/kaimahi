package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/localartifact"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.yaml.in/yaml/v3"
	"oras.land/oras-go/v2/content"
)

const maxFlowFile = 4 << 20

// FlowInventory adds stored experimental definitions to a complete native
// inventory. Neither storage validation nor these copies authorize execution.
type FlowInventory struct {
	NativeInventory
	Flow       agentsuite.FlowV1Alpha1
	FlowDigest string
}

// PrepareFlow explicitly selects the experimental contract before ingestion.
func PrepareFlow(ctx context.Context, root, cache string) (*Prepared, error) {
	return prepare(ctx, root, cache, true)
}

func canonicalFlow(raw []byte) ([]byte, error) {
	if len(raw) > maxFlowFile {
		return nil, errors.New("flow exceeds 4 MiB")
	}
	var flow agentsuite.FlowV1Alpha1
	if agentsuite.Unmarshal(raw, &flow) != nil {
		return nil, errors.New("invalid flow JSON")
	}
	// JSON escaping can hide a detector boundary (for example newline becomes
	// a word-character n); inspect decoded text before any staging or packaging.
	for _, c := range flow.Cases {
		if err := safeText(c.Input); err != nil {
			return nil, err
		}
		for _, a := range c.Assertions {
			if err := safeText(a.Check.Value + "\n" + a.Check.Pattern); err != nil {
				return nil, err
			}
		}
	}
	// Canonicalize original structure, not typed values: required/null/omitted
	// fields must reach the shared contract validator without reinterpretation.
	canonical, err := agentsuite.Marshal(json.RawMessage(raw))
	if err != nil {
		return nil, errors.New("invalid flow JSON")
	}
	if err := safeText(string(canonical)); err != nil {
		return nil, err
	}
	return canonical, nil
}

func attachFlow(files map[string][]byte, raw []byte) error {
	canonical, err := canonicalFlow(raw)
	if err != nil {
		return err
	}
	f, err := agentsuite.Digest(canonical)
	if err != nil {
		return err
	}
	var root agentsuite.Suite
	if err := agentsuite.Unmarshal(files["agentsuite.json"], &root); err != nil {
		return err
	}
	root.Extensions = []agentsuite.Extension{{Name: agentsuite.FlowExtensionV1Alpha1, Critical: true, Digest: f}}
	files["agentsuite.json"], err = agentsuite.Marshal(root)
	if err != nil {
		return err
	}
	files[agentsuite.FlowPathV1Alpha1] = canonical
	return nativeBoundsMode(files, true)
}

func validateFlowFiles(ctx context.Context, files map[string][]byte) (*agentsuite.FlowV1Alpha1Graph, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := nativeBoundsMode(files, true); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp("", "kmx-flow-validation-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	if err := writeFiles(stage, files); err != nil {
		return nil, err
	}
	graph, err := agentsuite.ValidateDirectoryFlowV1Alpha1(stage)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return graph, err
}

func readPreparedFlow(ctx context.Context, p *Prepared) (*localartifact.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil || !agentsuite.IsValidDigest(p.Fingerprint) {
		return nil, errors.New("valid prepared artifact identity is required")
	}
	root, err := safeAbsolute(p.Path)
	if err != nil {
		return nil, err
	}
	if err := privateTree(root, false); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	raw, err := readBounded(r, "index.json")
	r.Close()
	if err != nil {
		return nil, err
	}
	var index ocispec.Index
	if err := agentsuite.Unmarshal(raw, &index); err != nil {
		return nil, err
	}
	// General readers coalesce identical root descriptors; private prepared
	// caches retain the stricter exactly-one-index-entry invariant.
	if len(index.Manifests) != 1 {
		return nil, errors.New("prepared layout must contain exactly one root")
	}
	a, err := localartifact.ReadLayoutFlowV1Alpha1(ctx, root, p.Descriptor.Digest.String())
	if err != nil {
		return nil, err
	}
	if !content.Equal(a.Descriptor(), p.Descriptor) || a.Descriptor().ArtifactType != p.Descriptor.ArtifactType {
		return nil, errors.New("prepared descriptor does not match layout root")
	}
	name, err := validateMultiMode(a.Files(), true)
	if err != nil {
		return nil, err
	}
	if name != p.Name {
		return nil, errors.New("prepared name does not match artifact")
	}
	return a, ctx.Err()
}

// LoadFlowInventory verifies one original prepared artifact and all its members.
func LoadFlowInventory(ctx context.Context, p *Prepared) (*FlowInventory, error) {
	a, err := readPreparedFlow(ctx, p)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, verificationFailed(err)
	}
	return flowInventory(ctx, a)
}

// LoadFlowArtifactInventory reads a verified flow artifact without source/cache
// records; complete native regeneration still checks every packaged file.
func LoadFlowArtifactInventory(ctx context.Context, a *suiteoras.Artifact) (*FlowInventory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a == nil || a.FlowV1Alpha1() == nil {
		return nil, errors.New("verified experimental flow artifact is required")
	}
	if _, err := validateMultiMode(a.Files(), true); err != nil {
		return nil, verificationFailed(err)
	}
	return flowInventory(ctx, a)
}

func flowInventory(ctx context.Context, a *localartifact.Artifact) (*FlowInventory, error) {
	inventory, err := loadInventoryFiles(ctx, a.Files(), recipeArtifactIdentity(a.Descriptor()))
	if err != nil {
		return nil, err
	}
	graph := a.FlowV1Alpha1()
	return &FlowInventory{NativeInventory: *inventory, Flow: graph.Flow(), FlowDigest: graph.FlowDigest()}, ctx.Err()
}

// FlowCreateOptions selects reviewed member inputs and a public experimental
// flow document. All file paths are literal; member paths are members-file relative.
type FlowCreateOptions struct{ Name, MembersFile, FlowFile string }

type flowMemberInput struct {
	ID           string   `json:"id"`
	Model        string   `json:"model"`
	Instructions string   `json:"instructions"`
	Cases        []string `json:"cases"`
}

// CreateFlow authors a complete offline workspace, not an execution plan.
// Every input and the regenerated public graph is checked before destination writes.
func CreateFlow(ctx context.Context, root string, opt FlowCreateOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := safeText(opt.Name); err != nil {
		return err
	}
	if err := scaffold.ValidateName(opt.Name); err != nil {
		return err
	}
	membersPath, err := safeAbsolute(opt.MembersFile)
	if err != nil {
		return err
	}
	input, err := os.OpenRoot(filepath.Dir(membersPath))
	if err != nil {
		return err
	}
	defer input.Close()
	raw, err := readBounded(input, filepath.Base(membersPath))
	if err != nil {
		return err
	}
	var document struct {
		SchemaVersion string            `json:"schemaVersion"`
		Members       []flowMemberInput `json:"members"`
	}
	if safeText(string(raw)) != nil || agentsuite.Unmarshal(raw, &document) != nil {
		return errors.New("invalid members JSON")
	}
	if document.SchemaVersion != "kmx.members/v1alpha1" || len(document.Members) < 2 || len(document.Members) > maxMembers {
		return errors.New("members require kmx.members/v1alpha1 and two to 16 rows")
	}
	flowPath, err := safeAbsolute(opt.FlowFile)
	if err != nil {
		return err
	}
	flowRoot, err := os.OpenRoot(filepath.Dir(flowPath))
	if err != nil {
		return err
	}
	flow, err := readLimited(flowRoot, filepath.Base(flowPath), maxFlowFile)
	flowRoot.Close()
	if err != nil {
		return err
	}
	if _, err := canonicalFlow(flow); err != nil {
		return err
	}
	w := sourceWorkspace{SchemaVersion: 2, Name: opt.Name, BuildProfile: "build-profiles/default.json", Flow: agentsuite.FlowPathV1Alpha1}
	files := map[string][]byte{w.BuildProfile: defaultProfile, w.Flow: flow}
	seen := map[string]bool{}
	caseCount := 0
	for _, row := range document.Members {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := safeText(row.ID + "\n" + row.Model + "\n" + row.Instructions + "\n" + strings.Join(row.Cases, "\n")); err != nil {
			return err
		}
		if seen[row.ID] || len(row.Cases) == 0 {
			return errors.New("members require unique IDs and nonempty cases")
		}
		seen[row.ID] = true
		if err := confined(input, row.Instructions); err != nil {
			return err
		}
		m, instructions, err := createInput(CreateOptions{Name: row.ID, Model: row.Model, InstructionsFile: filepath.Join(filepath.Dir(membersPath), row.Instructions)})
		if err != nil {
			return err
		}
		memberPath := "agents/" + row.ID + "/member.yaml"
		w.Members = append(w.Members, memberPath)
		files[memberPath], err = yaml.Marshal(m)
		if err != nil {
			return err
		}
		files["agents/"+row.ID+"/instructions.md"] = instructions
		for _, name := range row.Cases {
			caseCount++
			if caseCount > maxCases {
				return errors.New("workspace requires at most 128 total cases")
			}
			dest := "eval/" + row.ID + "/" + path.Base(name)
			if !caseName(path.Base(name)) {
				return errors.New("cases require YAML filenames")
			}
			if _, exists := files[dest]; exists {
				return errors.New("duplicate member case filename")
			}
			files[dest], err = readBounded(input, name)
			if err != nil {
				return err
			}
			if err := nativeBoundsMode(files, true); err != nil {
				return err
			}
		}
	}
	files["workspace.yaml"], err = yaml.Marshal(w)
	if err != nil {
		return err
	}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(tmp, "kmx-flow-create-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := writeFiles(stage, files); err != nil {
		return err
	}
	if _, err := loadSourceMode(ctx, stage, true); err != nil {
		return err
	}
	return createSourceFiles(ctx, root, files)
}
