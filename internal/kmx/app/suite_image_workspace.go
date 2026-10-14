package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"io"
	"oras.land/oras-go/v2/registry"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/agentkit"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/imagelift"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// SuitePublication associates an exact published suite with its built members.
// This local workspace record is experimental, not a standardized OCI referrer.
type SuitePublication struct {
	Release       string              `json:"release,omitempty"`
	Fingerprint   string              `json:"fingerprint"`
	Indexes       map[string]string   `json:"indexes,omitempty"`
	Attested      map[string]bool     `json:"attested,omitempty"`
	Suite         string              `json:"suite"`
	LogicalDigest string              `json:"logicalDigest"`
	Images        map[string]string   `json:"images"`
	Platform      agentsuite.Platform `json:"platform"`
}

type SuiteWorkspaceEntry struct {
	Name        string
	Source      string
	Publication *SuitePublication
	Error       string
}

func ListSuiteWorkspace(root string) ([]SuiteWorkspaceEntry, error) {
	summary, err := workspace.Describe(root)
	if err != nil {
		return nil, err
	}
	entry := SuiteWorkspaceEntry{Name: summary.Name, Source: root}
	pub, err := ReadSuitePublication(root, summary.Name)
	if err == nil {
		entry.Publication = &pub
	} else if !os.IsNotExist(err) {
		entry.Error = err.Error()
	}
	return []SuiteWorkspaceEntry{entry}, nil
}

func workspaceState(root string) (string, error) {
	abs, err := workspace.Discover(root, root)
	if err != nil {
		return "", err
	}
	state, err := config.StateDir()
	if err != nil {
		return "", err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(abs)))
	return filepath.Join(state, "suite-images", key), nil
}

func workspaceRecord(root, name, suffix string) (string, error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return "", errors.New("invalid workspace name")
	}
	state, err := workspaceState(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(state, name+suffix+".json"), nil
}

func ReadSuitePublication(root, name string) (SuitePublication, error) {
	var value SuitePublication
	path, err := workspaceRecord(root, name, "-images")
	if err != nil {
		return value, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(data, &value)
	if err == nil && (len(value.Images) == 0 || !strings.Contains(value.Suite, "@sha256:")) {
		err = errors.New("incomplete suite publication")
	}
	return value, err
}

func saveWorkspaceRecord(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateAgentFile(path, data)
}

// PublishSuiteWorkspace builds every member before recording a complete image
// set. Model/endpoint bindings never enter the build request or its cache key.
type SuiteBuildOptions struct {
	Builder             string
	DisableAttestations bool
	RequireAttestations bool
	Provenance          SuitePushProvenance
	Force               bool
}

func (a *App) PublishSuiteWorkspace(ctx context.Context, root, name, repository string, plainHTTP bool, options SuiteBuildOptions) (SuitePublication, error) {
	var result SuitePublication
	if options.RequireAttestations && options.DisableAttestations {
		return result, errors.New("required attestations cannot be disabled")
	}
	path, err := workspaceRecord(root, name, "-images")
	if err != nil {
		return result, err
	}
	summary, err := workspace.Describe(root)
	if err != nil {
		return result, err
	}
	if summary.Name != name || summary.Execution != workspace.ImageExecution {
		return result, errors.New("image publication requires the selected image-http workspace")
	}
	state, err := config.StateDir()
	if err != nil {
		return result, err
	}
	prepared, err := workspace.Prepare(ctx, root, filepath.Join(state, "suites"))
	if err != nil {
		return result, err
	}
	work, err := os.MkdirTemp("", "kmx-workspace-build-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(work)
	pulled, err := suiteoras.Pull(ctx, prepared.Path, prepared.Descriptor.Digest.String(), filepath.Join(work, "source"))
	if err != nil {
		return result, err
	}
	source := pulled.Path
	platform := agentsuite.Platform{OS: "linux", Architecture: "amd64"}
	suite, err := agentsuite.ResolveDeploymentSuite(source, platform)
	if err != nil {
		return result, err
	}
	for _, member := range suite.Members() {
		if member.Agent.Model.Capabilities == nil {
			return result, errors.New("workspace builds require capability-based inference")
		}
	}
	parsed, err := registry.ParseReference(repository)
	if err != nil {
		return result, err
	}
	if err := parsed.ValidateReferenceAsTag(); err != nil {
		return result, err
	}
	repo := parsed.Registry + "/" + parsed.Repository
	tag := prepared.Descriptor.Digest.Encoded()
	var pushOptions suiteoras.PushOptions
	if options.Provenance.Enabled {
		inputs, err := workspace.SourceFiles(ctx, root, prepared)
		if err != nil {
			return result, err
		}
		pushOptions, _ = suitePushOptions(ctx, root, options.Provenance, inputs)
	}
	if err := workspace.CheckCurrent(ctx, root, prepared); err != nil {
		return result, err
	}
	result = SuitePublication{Fingerprint: prepared.Fingerprint, Suite: repo + "@" + prepared.Descriptor.Digest.String(), LogicalDigest: suite.LogicalDigest(), Platform: platform, Images: map[string]string{}}
	finish := func(pub SuitePublication) (SuitePublication, error) {
		if err := workspace.CheckCurrent(ctx, root, prepared); err != nil {
			return pub, err
		}
		// A content-addressed source tag is an intermediate upload, not the user
		// release pointer. Required images are already complete at this point.
		if _, err := suiteoras.PushLayoutRegistry(ctx, prepared.Path, repo+":source-"+tag, plainHTTP, false, prepared.Descriptor, pushOptions); err != nil {
			return pub, err
		}
		release, err := suiteoras.PublishImageReleaseRegistry(ctx, repository, pub.Suite, pub.Images, pub.Indexes, platform, plainHTTP, options.Force)
		if err != nil {
			return pub, err
		}
		pub.Release = release
		return pub, saveWorkspaceRecord(path, pub)
	}
	result.Indexes = map[string]string{}
	result.Attested = map[string]bool{}
	if old, err := ReadSuitePublication(root, name); err == nil && old.Suite == result.Suite && old.LogicalDigest == result.LogicalDigest && len(old.Images) == len(suite.Members()) {
		// Verify the recorded graph is still reachable rather than trusting a cache
		// flag after registry pruning or a failed earlier upload.
		complete := true
		for _, member := range suite.Members() {
			if !options.DisableAttestations && !old.Attested[member.Agent.ID] {
				complete = false
				break
			}
			if old.Attested[member.Agent.ID] {
				indexed, err := suiteoras.ResolveImageRegistryTransport(ctx, old.Indexes[member.Agent.ID], platform, plainHTTP)
				if err != nil || indexed.Reference != old.Images[member.Agent.ID] {
					complete = false
					break
				}
			}
			image, err := suiteoras.ResolveImageRegistryTransport(ctx, old.Images[member.Agent.ID], platform, plainHTTP)
			if err != nil || image.Deployment.SuiteDigest != prepared.Descriptor.Digest.String() || image.Deployment.Agent != member.Agent.ID {
				complete = false
				break
			}
			if err := agentsuite.ValidateImageSource(source, image.Deployment); err != nil {
				complete = false
				break
			}
		}
		if complete {
			if err := CheckWorkspacePublication(root, name, old); err != nil {
				return result, err
			}
			return finish(old)
		}
		if options.DisableAttestations || len(old.Attested) > 0 {
			return result, errors.New("recorded image set could not be checked; inspect registry access/content before explicitly rebuilding")
		}
	}
	snapshot, err := agentsuite.ResolveDeploymentSuite(pulled.Path, platform)
	if err != nil {
		return result, err
	}
	if snapshot.LogicalDigest() != suite.LogicalDigest() {
		return result, errors.New("source changed during publication; retry from the new source revision")
	}
	for _, member := range suite.Members() {
		archive := filepath.Join(work, member.Agent.ID+".oci.tar")
		builder := agentkit.New(agentkit.Options{SuiteReference: result.Suite, SuiteDigest: prepared.Descriptor.Digest.String(), Progress: a.Err, Builder: options.Builder, DisableAttestations: options.DisableAttestations, RequireAttestations: options.RequireAttestations})
		built, err := a.BuildSuite(ctx, pulled.Path, archive, agentsuite.BuildSelection{Agent: member.Agent.ID, Platform: platform.String()}, builder)
		if err != nil {
			return result, err
		}
		ref, err := suiteoras.PushImage(ctx, archive, repo+"/"+member.Agent.ID+":"+tag, platform, plainHTTP)
		if err != nil {
			return result, err
		}
		result.Images[member.Agent.ID] = ref
		result.Indexes[member.Agent.ID] = repo + "/" + member.Agent.ID + "@" + built.IndexDigest
		result.Attested[member.Agent.ID] = built.HasAttestations
	}
	return finish(result)
}

// CheckWorkspacePublication prevents a source edit from silently lifting an
// older published revision under the same workspace row.
func CheckWorkspacePublication(root, name string, publication SuitePublication) error {
	if _, err := workspaceRecord(root, name, "-images"); err != nil {
		return err
	}
	state, err := config.StateDir()
	if err != nil {
		return err
	}
	prepared, err := workspace.Prepare(context.Background(), root, filepath.Join(state, "suites"))
	if err != nil {
		return err
	}
	_, digest, _ := strings.Cut(publication.Suite, "@")
	if prepared.Name != name || prepared.Descriptor.Digest.String() != digest {
		return errors.New("local source differs from publication; build/publish the updated suite before lift")
	}
	return nil
}

func ReadSuiteEnvironment(path string) (imagelift.Environment, error) {
	f, err := os.Open(path)
	if err != nil {
		return imagelift.Environment{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return imagelift.Environment{}, err
	}
	return imagelift.DecodeEnvironment(data)
}

// BindSuitePublication consumes explicit per-member target settings. Image refs
// may be omitted from the template; conflicting supplied refs are rejected.
func BindSuitePublication(pub SuitePublication, env imagelift.Environment) (imagelift.Environment, error) {
	if len(env.Members) != len(pub.Images) || env.Platform != pub.Platform {
		return env, errors.New("environment must select the published platform and every member")
	}
	for id, image := range pub.Images {
		member, ok := env.Members[id]
		if !ok || member.Image != "" && member.Image != image {
			return env, errors.New("environment member image differs from publication")
		}
		member.Image = image
		env.Members[id] = member
	}
	return env, env.Validate()
}

type WorkspaceDeployment struct {
	WorkspaceName string                              `json:"workspaceName"`
	Environment   imagelift.Environment               `json:"environment"`
	Publication   SuitePublication                    `json:"publication"`
	Receipt       agentruntime.SuiteDeploymentReceipt `json:"receipt"`
}

func (a *App) ApplyWorkspaceLift(ctx context.Context, root, name string, pub SuitePublication, prepared *PreparedReleaseLift, reviewed string) (ReleaseLiftResult, error) {
	if err := CheckWorkspacePublication(root, name, pub); err != nil {
		return ReleaseLiftResult{}, err
	}
	_, digest, pinned := strings.Cut(pub.Suite, "@")
	if prepared == nil || !pinned || prepared.Summary().SuiteDigest != digest {
		return ReleaseLiftResult{}, ErrReleaseLiftApply
	}
	return a.ApplyReleaseLift(ctx, prepared, reviewed)
}

func DescribePublication(pub SuitePublication) string {
	return fmt.Sprintf("%s · %d image(s)", pub.Suite, len(pub.Images))
}
