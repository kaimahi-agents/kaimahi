// Package agentkit implements the experimental AgentKit sandbox builder.
package agentkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/opencontainers/go-digest"
	agentkitconfig "github.com/sozercan/agentkit/pkg/agentkit/config"
	"github.com/sozercan/agentkit/pkg/agentkit/effective"
	"github.com/sozercan/agentkit/pkg/utils"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

const OCIArchiveMediaType = "application/vnd.oci.image.layout.v1.tar"

var imageReferencePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?(?::[0-9]+)?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+@sha256:[a-f0-9]{64}$`)

// Options bind the provider-neutral build plan to AgentKit and BuildKit.
type Options struct {
	ModelBaseURL    string
	ModelAPIKeyEnv  string
	Runtime         string
	BuildkitAddress string
	Verbose         bool
	Progress        io.Writer
	exporter        ociExporter
}

// Builder uses AgentKit's current monolithic adapter image. It is explicitly
// experimental because AgentKit does not yet compose runtime-base and harness
// images independently.
type Builder struct {
	options Options
}

var _ agentsuite.SandboxBuilder = (*Builder)(nil)

// New returns an experimental AgentKit builder.
func New(options Options) *Builder {
	if options.Runtime == "" {
		options.Runtime = "pydantic-ai"
	}
	if options.BuildkitAddress == "" {
		options.BuildkitAddress = os.Getenv("BUILDKIT_HOST")
	}
	if options.exporter == nil {
		exporter := buildkitExporter{
			address:  options.BuildkitAddress,
			verbose:  options.Verbose,
			progress: options.Progress,
		}
		if options.BuildkitAddress == "" || options.BuildkitAddress == managedBuildkitAddress {
			exporter.address = managedBuildkitAddress
			exporter.manager = managedBuildkitManager{
				verbose:     options.Verbose,
				diagnostics: options.Progress,
			}
		}
		options.exporter = exporter
	}
	return &Builder{options: options}
}

func (b *Builder) Build(
	ctx context.Context,
	plan agentsuite.SandboxPlan,
	dst io.Writer,
) (agentsuite.BuildResult, error) {
	if b == nil {
		return agentsuite.BuildResult{}, errors.New("AgentKit builder is required")
	}
	if dst == nil {
		return agentsuite.BuildResult{}, errors.New("AgentKit build output is required")
	}
	if err := b.validate(plan); err != nil {
		return agentsuite.BuildResult{}, err
	}

	cfg := b.agentConfig(plan)
	agent := effective.FromConfig(&cfg, string(plan.Instructions))
	platform := plan.Composition.Platform
	if err := b.options.exporter.ExportOCI(ctx, agentImage{
		Agent:        agent,
		AdapterRef:   plan.Harness.ImageRef,
		OS:           platform.OS,
		Architecture: platform.Architecture,
	}, dst); err != nil {
		return agentsuite.BuildResult{}, fmt.Errorf("build experimental AgentKit image: %w", err)
	}
	return agentsuite.BuildResult{
		MediaType: OCIArchiveMediaType,
		Warnings: []string{
			"experimental AgentKit output is not AgentSuite-conformant: the harness image is treated as a monolithic AgentKit adapter and the runtime-base image is not composed",
		},
	}, nil
}

func (b *Builder) validate(plan agentsuite.SandboxPlan) error {
	var errs []error
	parsedURL, err := url.Parse(b.options.ModelBaseURL)
	if err != nil || parsedURL.Scheme != "http" && parsedURL.Scheme != "https" ||
		parsedURL.Host == "" || parsedURL.User != nil || parsedURL.Fragment != "" {
		errs = append(errs, errors.New("experimental AgentKit builder requires an absolute http(s) --model-base-url without credentials or fragment"))
	}
	if plan.Agent.Model.Protocol != "openai-compatible" {
		errs = append(errs, fmt.Errorf("experimental AgentKit builder does not support model protocol %q", plan.Agent.Model.Protocol))
	}
	if plan.Agent.Model.EndpointEnv != "" {
		errs = append(errs, errors.New("experimental AgentKit builder cannot represent model.endpointEnv"))
	}
	if len(plan.Agent.Model.SecretRefs) != 0 {
		errs = append(errs, errors.New("experimental AgentKit builder cannot represent model.secretRefs"))
	}
	if len(plan.Agent.Invokes) != 0 {
		errs = append(errs, errors.New("experimental AgentKit builder cannot represent agent invocation edges"))
	}
	if len(plan.ToolProviders) != 0 {
		errs = append(errs, errors.New("experimental AgentKit builder does not yet support ToolProviders"))
	}
	if plan.RuntimeBase.ImageRef == "" {
		errs = append(errs, errors.New("resolved runtime-base image reference is required"))
	}
	if err := validateDigestReference("resolved harness", plan.Harness.ImageRef); err != nil {
		errs = append(errs, err)
	}
	if plan.Composition.Platform.String() != "linux/amd64" && plan.Composition.Platform.String() != "linux/arm64" {
		errs = append(errs, fmt.Errorf("experimental AgentKit builder does not support platform %s", plan.Composition.Platform))
	}
	cfg := b.agentConfig(plan)
	if err := cfg.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("AgentKit configuration is invalid: %w", err))
	}
	return errors.Join(errs...)
}

func validateDigestReference(name, value string) error {
	if !imageReferencePattern.MatchString(value) {
		return fmt.Errorf("%s must be a registry-qualified, digest-addressed image reference", name)
	}
	before, encoded, ok := strings.Cut(value, "@")
	if !ok || before == "" {
		return fmt.Errorf("%s must be a registry-qualified, digest-addressed image reference", name)
	}
	parsed, err := digest.Parse(encoded)
	if err != nil || parsed.Algorithm() != digest.SHA256 {
		return fmt.Errorf("%s must use a valid sha256 digest", name)
	}
	return nil
}

func (b *Builder) agentConfig(plan agentsuite.SandboxPlan) agentkitconfig.AgentConfig {
	return agentkitconfig.AgentConfig{
		APIVersion: utils.APIv1alpha1,
		Kind:       utils.KindAgent,
		Metadata: agentkitconfig.Metadata{
			Name: plan.Agent.ID,
		},
		Runtime: b.options.Runtime,
		Model: agentkitconfig.Model{
			Provider:  plan.Agent.Model.Protocol,
			BaseURL:   b.options.ModelBaseURL,
			Name:      plan.Agent.Model.Model,
			APIKeyEnv: b.options.ModelAPIKeyEnv,
		},
		Instructions: agentkitconfig.Source{Inline: string(plan.Instructions)},
		Expose:       agentkitconfig.Expose{OpenAI: true},
	}
}
