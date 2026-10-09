// Package agentkit implements the experimental AgentKit sandbox builder.
package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

const (
	OCIArchiveMediaType = "application/vnd.oci.image.layout.v1.tar"
	agentKitFrontend    = "ghcr.io/orka-agents/agentkit/agentkit@sha256:8899d3ab38bdd8020b4ab21de128002bbc66ffd65111d7097daa8f8fd21805d9"
	// TODO: Source the harness runtime from AgentSuite once its portable contract represents it.
	agentKitRuntime = "pydantic-ai"
)

// Options bind the provider-neutral build plan to AgentKit and Docker buildx.
type Options struct {
	ModelBaseURL   string
	ModelAPIKeyEnv string
	Verbose        bool
	Progress       io.Writer
	exporter       ociExporter
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
	if options.exporter == nil {
		options.exporter = buildxExporter{
			verbose:  options.Verbose,
			progress: options.Progress,
		}
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

	agentkitFile, err := b.agentkitFile(plan)
	if err != nil {
		return agentsuite.BuildResult{}, fmt.Errorf("render AgentKit build input: %w", err)
	}
	platform := plan.Composition.Platform
	if err := b.options.exporter.ExportOCI(ctx, agentImage{
		AgentkitFile: agentkitFile,
		Name:         plan.Agent.ID,
		AdapterRef:   plan.Harness.ImageRef,
		Platform:     platform.String(),
		SourceEpoch:  plan.BuildProfile.SourceEpoch,
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
		parsedURL.Hostname() == "" || parsedURL.User != nil || parsedURL.RawQuery != "" ||
		parsedURL.ForceQuery || parsedURL.Fragment != "" {
		errs = append(errs, errors.New("experimental AgentKit builder requires an absolute http(s) --model-base-url without credentials, query, or fragment"))
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
	return errors.Join(errs...)
}

func validateDigestReference(name, value string) error {
	if err := agentsuite.ValidateImageReference(value); err != nil {
		return fmt.Errorf("%s must be a registry-qualified, digest-addressed image reference: %w", name, err)
	}
	return nil
}

func (b *Builder) agentkitFile(plan agentsuite.SandboxPlan) ([]byte, error) {
	config := agentConfig{
		APIVersion: "v1alpha1",
		Kind:       "Agent",
		Metadata: agentMetadata{
			Name: plan.Agent.ID,
		},
		Runtime: agentKitRuntime,
		Model: agentModel{
			Provider:  plan.Agent.Model.Protocol,
			BaseURL:   b.options.ModelBaseURL,
			Name:      plan.Agent.Model.Model,
			APIKeyEnv: b.options.ModelAPIKeyEnv,
		},
		Instructions: string(plan.Instructions),
		Expose:       agentExpose{OpenAI: true},
	}
	body, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, len(agentKitFrontend)+len(body)+11)
	result = append(result, "#syntax="...)
	result = append(result, agentKitFrontend...)
	result = append(result, '\n')
	result = append(result, body...)
	result = append(result, '\n')
	return result, nil
}

type agentConfig struct {
	APIVersion   string        `json:"apiVersion"`
	Kind         string        `json:"kind"`
	Metadata     agentMetadata `json:"metadata"`
	Runtime      string        `json:"runtime"`
	Model        agentModel    `json:"model"`
	Instructions string        `json:"instructions"`
	Expose       agentExpose   `json:"expose"`
}

type agentMetadata struct {
	Name string `json:"name"`
}

type agentModel struct {
	Provider  string `json:"provider"`
	BaseURL   string `json:"baseURL"`
	Name      string `json:"name"`
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
}

type agentExpose struct {
	OpenAI bool `json:"openai"`
}
