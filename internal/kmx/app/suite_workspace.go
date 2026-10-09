package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	entries := []SuiteWorkspaceEntry{}
	for _, dir := range dirs {
		if !dir.IsDir() || strings.HasPrefix(dir.Name(), ".") {
			continue
		}
		source := filepath.Join(root, dir.Name())
		if _, err := os.Stat(filepath.Join(source, "agentsuite.json")); os.IsNotExist(err) {
			continue
		}
		entry := SuiteWorkspaceEntry{Name: dir.Name(), Source: source}
		pub, err := ReadSuitePublication(root, dir.Name())
		if err == nil {
			entry.Publication = &pub
		} else if !os.IsNotExist(err) {
			entry.Error = err.Error()
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func workspaceRecord(root, name, suffix string) (string, error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return "", errors.New("invalid workspace name")
	}
	return filepath.Join(root, ".kmx", name+suffix+".json"), nil
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
	source := filepath.Join(root, name)
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
	repository = strings.TrimSuffix(repository, "/")
	// Content-derived tags avoid silently overwriting another source publication.
	tag := strings.TrimPrefix(suite.LogicalDigest(), "sha256:")
	pushed, err := suiteoras.PushRegistry(ctx, source, repository+"/source:"+tag, plainHTTP, false)
	if err != nil {
		return result, err
	}
	result = SuitePublication{Suite: repository + "/source@" + pushed.Descriptor.Digest.String(), LogicalDigest: suite.LogicalDigest(), Platform: platform, Images: map[string]string{}}
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
			if err != nil || image.Deployment.SuiteDigest != pushed.Descriptor.Digest.String() || image.Deployment.Agent != member.Agent.ID {
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
			return old, nil
		}
		if options.DisableAttestations || len(old.Attested) > 0 {
			return result, errors.New("recorded image set could not be checked; inspect registry access/content before explicitly rebuilding")
		}
	}
	work, err := os.MkdirTemp("", "kmx-workspace-build-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(work)
	// Build the pulled snapshot, not a mutable directory that may be edited while
	// the registry operation or subsequent build is running.
	pulled, err := suiteoras.PullRegistry(ctx, result.Suite, filepath.Join(work, "source"), plainHTTP)
	if err != nil {
		return result, err
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
		builder := agentkit.New(agentkit.Options{SuiteReference: result.Suite, SuiteDigest: pushed.Descriptor.Digest.String(), Progress: a.Err, Builder: options.Builder, DisableAttestations: options.DisableAttestations, RequireAttestations: options.RequireAttestations})
		built, err := a.BuildSuite(ctx, pulled.Path, archive, agentsuite.BuildSelection{Agent: member.Agent.ID, Platform: platform.String()}, builder)
		if err != nil {
			return result, err
		}
		ref, err := suiteoras.PushImage(ctx, archive, repository+"/"+member.Agent.ID+":"+tag, platform, plainHTTP)
		if err != nil {
			return result, err
		}
		result.Images[member.Agent.ID] = ref
		result.Indexes[member.Agent.ID] = repository + "/" + member.Agent.ID + "@" + built.IndexDigest
		result.Attested[member.Agent.ID] = built.HasAttestations
	}
	return result, saveWorkspaceRecord(path, result)
}

// CheckWorkspacePublication prevents a source edit from silently lifting an
// older published revision under the same workspace row.
func CheckWorkspacePublication(root, name string, publication SuitePublication) error {
	if _, err := workspaceRecord(root, name, "-images"); err != nil {
		return err
	}
	suite, err := agentsuite.ResolveDeploymentSuite(filepath.Join(root, name), publication.Platform)
	if err != nil {
		return err
	}
	if suite.LogicalDigest() != publication.LogicalDigest {
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

func (a *App) ApplyWorkspaceLift(ctx context.Context, root, name string, pub SuitePublication, env imagelift.Environment, prepared *agentruntime.PreparedSuiteDeployment, reviewed string) (agentruntime.SuiteDeploymentReceipt, error) {
	var receipt agentruntime.SuiteDeploymentReceipt
	if prepared == nil || prepared.Summary().PlanDigest != reviewed {
		return receipt, errors.New("reviewed plan identity differs")
	}
	if err := CheckWorkspacePublication(root, name, pub); err != nil {
		return receipt, err
	}
	if err := env.Validate(); err != nil {
		return receipt, err
	}
	_, suiteDigest, pinned := strings.Cut(pub.Suite, "@")
	if !pinned || prepared.Summary().Instance != env.Name || prepared.Summary().ArtifactDigest != suiteDigest {
		return receipt, errors.New("publication or instance differs from reviewed plan")
	}
	if len(prepared.Summary().Members) != len(env.Members) {
		return receipt, errors.New("member bindings differ from reviewed plan")
	}
	// Reconstruct only the pure configuration projection to ensure the private
	// record stores the same inputs as the retained reviewed plan.
	source, err := agentsuite.ResolveDeploymentSuite(filepath.Join(root, name), pub.Platform)
	if err != nil {
		return receipt, err
	}
	projections := map[string]agentsuite.DeploymentMember{}
	for _, member := range source.Members() {
		projections[member.Agent.ID] = member
	}
	for _, member := range prepared.Summary().Members {
		binding, ok := env.Members[member.Agent]
		if !ok || binding.Name != member.Name || binding.Image != pub.Images[member.Agent] {
			return receipt, errors.New("member selection differs from publication or reviewed plan")
		}
		projection, ok := projections[member.Agent]
		if !ok || projection.BuildProfile.Execution == nil {
			return receipt, errors.New("reviewed member source missing")
		}
		child := env
		child.Members = nil
		child.Name = binding.Name
		child.Inputs = binding.Inputs
		child.Inference = binding.Inference
		child.Instructions = projection.Instructions
		record := agentsuite.ImageDeployment{SchemaVersion: agentsuite.SpecVersion, MediaType: agentsuite.ImageDeploymentMediaType, SuiteReference: pub.Suite, SuiteDigest: suiteDigest, Agent: member.Agent, Platform: pub.Platform, CompositionDigest: projection.CompositionDigest, BuildProfile: projection.BuildProfile.ID, Execution: *projection.BuildProfile.Execution, Inference: projection.Agent.Model.Capabilities}
		rendered, err := imagelift.Render(binding.Image, record, child)
		if err != nil {
			return receipt, err
		}
		if rendered.Digest != member.Digest {
			return receipt, errors.New("deployment bindings changed after plan review")
		}
	}
	// Distinguish deployments with the same friendly name on different targets.
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(env.ClusterUID+"\x00"+env.Namespace+"\x00"+env.Name)))[:16]
	path, err := workspaceRecord(root, name, "-"+env.Name+"-"+key+"-deployment")
	if err != nil {
		return receipt, err
	}
	record := WorkspaceDeployment{WorkspaceName: name, Environment: env, Publication: pub}
	if err := saveWorkspaceRecord(path, record); err != nil {
		return receipt, err
	}
	receipt, deployErr := prepared.Deploy(ctx, func(summary agentruntime.SuitePlanSummary) error {
		if summary.PlanDigest != reviewed || summary.Target.ClusterUID != env.ClusterUID || summary.Target.Context != env.Context || summary.Target.Namespace != env.Namespace {
			return errors.New("deployment target differs from reviewed plan")
		}
		return nil
	})
	record.Receipt = receipt
	return receipt, errors.Join(deployErr, saveWorkspaceRecord(path, record))
}

func DescribePublication(pub SuitePublication) string {
	return fmt.Sprintf("%s · %d image(s)", pub.Suite, len(pub.Images))
}
