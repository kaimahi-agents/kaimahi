// Package agentkit implements the experimental AgentKit sandbox builder.
package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/distribution/reference"
	kmxsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const (
	OCIArchiveMediaType = kmxsuite.OCIArchiveMediaType
	agentKitFrontend    = "ghcr.io/orka-agents/agentkit/agentkit@sha256:8899d3ab38bdd8020b4ab21de128002bbc66ffd65111d7097daa8f8fd21805d9"
	// TODO: Source the harness runtime from AgentSuite once its portable contract represents it.
	agentKitRuntime = "pydantic-ai"
	agentKitHarness = "ghcr.io/orka-agents/agentkit/serve-pydantic-ai"
)

var modelAPIKeyEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateModelAPIKeyEnv accepts an optional environment variable name, never a
// key value. Errors omit the input so a pasted credential cannot reach output.
func ValidateModelAPIKeyEnv(value string) error {
	if secretshapes.Match(value) != nil {
		return errors.New("--model-api-key-env requires an environment variable name, not a credential value")
	}
	if value != "" && !modelAPIKeyEnvName.MatchString(value) {
		return errors.New("--model-api-key-env must be an environment variable name matching [A-Za-z_][A-Za-z0-9_]*")
	}
	return nil
}

// Options bind the provider-neutral build plan to AgentKit and Docker buildx.
type Options struct {
	SuiteReference      string
	SuiteDigest         string
	ModelBaseURL        string
	ModelAPIKeyEnv      string
	Verbose             bool
	Progress            io.Writer
	Builder             string
	DisableAttestations bool
	RequireAttestations bool
	exporter            ociExporter
}

// Builder uses AgentKit's current monolithic adapter image. It is explicitly
// experimental because AgentKit does not yet compose runtime-base and harness
// images independently.
type Builder struct {
	options Options
}

var _ kmxsuite.SandboxBuilder = (*Builder)(nil)

// New returns an experimental AgentKit builder.
func New(options Options) *Builder {
	if options.exporter == nil {
		options.exporter = buildxExporter{
			verbose:             options.Verbose,
			progress:            options.Progress,
			builder:             options.Builder,
			disableAttestations: options.DisableAttestations,
			requireAttestations: options.RequireAttestations,
		}
	}
	return &Builder{options: options}
}

func (b *Builder) Build(
	ctx context.Context,
	plan kmxsuite.SandboxPlan,
	dst io.Writer,
) (kmxsuite.BuildResult, error) {
	if b == nil {
		return kmxsuite.BuildResult{}, errors.New("AgentKit builder is required")
	}
	if dst == nil {
		return kmxsuite.BuildResult{}, errors.New("AgentKit build output is required")
	}
	if err := b.validate(plan); err != nil {
		return kmxsuite.BuildResult{}, err
	}

	agentkitFile, err := b.agentkitFile(plan)
	if err != nil {
		return kmxsuite.BuildResult{}, fmt.Errorf("render AgentKit build input: %w", err)
	}
	label := ""
	if b.options.SuiteReference != "" {
		if plan.BuildProfile.Execution == nil {
			return kmxsuite.BuildResult{}, errors.New("source-linked image requires an execution contract")
		}
		label, err = kmxsuite.EncodeImageDeployment(kmxsuite.ImageDeployment{
			SchemaVersion: suitecore.SpecVersion, MediaType: kmxsuite.ImageDeploymentMediaType,
			SuiteDigest: b.options.SuiteDigest,
			Agent:       plan.Agent.ID, Platform: plan.Composition.Platform, CompositionDigest: plan.CompositionDigest,
			BuildProfile: plan.BuildProfile.ID, BuildProfileDigest: plan.BuildProfileDigest, Execution: *plan.BuildProfile.Execution, Inference: plan.Agent.Model.Capabilities,
		})
		if err != nil {
			return kmxsuite.BuildResult{}, err
		}
	}
	mounted := plan.Agent.Model.Capabilities != nil
	if mounted {
		// Use buildx's Dockerfile frontend for the config-mounted profile. There
		// are no RUN steps and no generated model configuration in the build.
		agentkitFile = []byte("FROM " + plan.Harness.ImageRef + "\n" +
			"COPY --chown=0:0 --chmod=0444 instructions.txt /agent/instructions.txt\n" +
			"USER 1000:1000\nWORKDIR /\n" +
			"ENV PATH=/opt/agentkit/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin AGENTKIT_BIND=127.0.0.1 PYTHONUNBUFFERED=1\n" +
			"ENTRYPOINT [\"/opt/agentkit/bin/agentkit-serve\"]\n" +
			"CMD [\"--config\",\"" + kmxsuite.AgentKitConfigPath + "\",\"--protocol\",\"openai\"]\n" +
			"EXPOSE 8080\nLABEL " + kmxsuite.ImageDeploymentLabel + "=" + strconv.Quote(label) + "\n")
	} else if label != "" {
		// The pinned AgentKit frontend accepts metadata labels and emits them in
		// the image configuration. Labels are digest-bound publisher declarations.
		agentkitFile, err = b.agentkitFileWithLabel(plan, label)
		if err != nil {
			return kmxsuite.BuildResult{}, err
		}
	}
	// The caller owns staging and inspection before publishing identities.
	platform := plan.Composition.Platform
	exported, err := b.options.exporter.ExportOCI(ctx, agentImage{
		MountedConfig: mounted, Instructions: append([]byte(nil), plan.Instructions...),
		AgentkitFile: agentkitFile,
		Name:         plan.Agent.ID,
		AdapterRef:   plan.Harness.ImageRef,
		Platform:     platform.String(),
		SourceEpoch:  plan.BuildProfile.SourceEpoch,
	}, dst)
	if err != nil {
		return kmxsuite.BuildResult{}, fmt.Errorf("build experimental AgentKit image: %w", err)
	}
	result := kmxsuite.BuildResult{
		MediaType:             OCIArchiveMediaType,
		AttestationsRequested: exported.AttestationsRequested,
		RequireAttestations:   b.options.RequireAttestations,
		Warnings:              exported.Warnings,
	}
	result.Warnings = append(result.Warnings, "experimental AgentKit output is not AgentSuite-conformant: the harness image is treated as a monolithic AgentKit adapter and the runtime-base image is not composed")
	return result, nil
}

func (b *Builder) validate(plan kmxsuite.SandboxPlan) error {
	if plan.RuntimeProfile != nil || plan.Composition.RuntimeProfile != "" || len(plan.Suite.RuntimeProfiles) != 0 {
		return errors.New("experimental AgentKit builder does not support profiled compositions; a qualified runtime build adapter is required")
	}
	var errs []error
	if execution := plan.BuildProfile.Execution; execution != nil {
		errs = append(errs, suitecore.ValidateExecutionContract(*execution))
		if execution.Port != 8080 || execution.HealthPath != "/healthz" {
			errs = append(errs, errors.New("AgentKit builder supports only port 8080 and /healthz"))
		}
		if plan.Agent.Model.Capabilities == nil && execution.Configuration != "" {
			errs = append(errs, errors.New("exact-model AgentKit builds do not implement mounted configuration"))
		}
		inputs := map[string]bool{}
		for _, input := range execution.Inputs {
			inputs[input.Environment] = input.Secret
			switch input.Environment {
			case "AGENTKIT_BIND":
				if input.Secret {
					errs = append(errs, errors.New("AgentKit bind input must be non-secret"))
				}
			case "AGENTKIT_AUTH_TOKEN":
				if !input.Secret {
					errs = append(errs, errors.New("AgentKit authentication input must be secret"))
				}
			default:
				if plan.Agent.Model.Capabilities != nil || b.options.ModelAPIKeyEnv == "" || input.Environment != b.options.ModelAPIKeyEnv || !input.Secret {
					errs = append(errs, errors.New("execution input is not consumed by the pinned AgentKit adapter"))
				}
			}
		}
		if plan.Agent.Model.Capabilities != nil {
			bind, ok := inputs["AGENTKIT_BIND"]
			if !ok || bind || !inputs["AGENTKIT_AUTH_TOKEN"] {
				errs = append(errs, errors.New("mounted AgentKit serving requires non-secret AGENTKIT_BIND and secret AGENTKIT_AUTH_TOKEN inputs"))
			}
		} else if b.options.ModelAPIKeyEnv != "" && !inputs[b.options.ModelAPIKeyEnv] {
			errs = append(errs, errors.New("execution declaration must include the configured model credential environment as a secret input"))
		}
	}
	if b.options.SuiteReference != "" && (suitecore.ValidateImageReference(b.options.SuiteReference) != nil || !suitecore.IsValidDigest(b.options.SuiteDigest) || !strings.HasSuffix(b.options.SuiteReference, "@"+b.options.SuiteDigest)) {
		errs = append(errs, errors.New("published source reference must pin the exact suite manifest digest"))
	}
	if b.options.DisableAttestations && b.options.RequireAttestations {
		errs = append(errs, errors.New("--require-attestations cannot be used with --attestations=false"))
	}
	if err := ValidateModelAPIKeyEnv(b.options.ModelAPIKeyEnv); err != nil {
		errs = append(errs, err)
	}
	if b.options.ModelAPIKeyEnv == "AGENTKIT_AUTH_TOKEN" || b.options.ModelAPIKeyEnv == "AGENTKIT_BIND" {
		errs = append(errs, errors.New("model credential environment must not alias AgentKit serving inputs"))
	}
	if plan.Agent.Model.Capabilities != nil {
		// The qualified digest below is a single amd64 manifest, not a multi-platform index.
		if plan.Composition.Platform.String() != "linux/amd64" {
			errs = append(errs, errors.New("mounted AgentKit configuration supports only the qualified linux/amd64 harness"))
		}
		if b.options.ModelBaseURL != "" || b.options.ModelAPIKeyEnv != "" {
			errs = append(errs, errors.New("capability-based builds take model and credentials only at deployment; omit model build flags"))
		}
		if b.options.SuiteReference == "" || plan.BuildProfile.Execution == nil || plan.BuildProfile.Execution.Configuration != suitecore.AgentKitMountedConfig {
			errs = append(errs, errors.New("capability-based builds require --suite-ref and agentkit-v0-mounted-v1 configuration"))
		}
		if plan.Harness.ImageRef != "ghcr.io/orka-agents/agentkit/serve-pydantic-ai@sha256:8c4c17dc3d778c02097ad7e1ea88e9084b3f1aad6c36458829ff9cec9b4d043c" {
			errs = append(errs, errors.New("mounted configuration requires the qualified pinned pydantic-ai harness"))
		}
		errs = append(errs, plan.Agent.Model.Capabilities.Validate())
	} else {
		parsedURL, err := url.Parse(b.options.ModelBaseURL)
		if err != nil || parsedURL.Scheme != "http" && parsedURL.Scheme != "https" ||
			parsedURL.Hostname() == "" || parsedURL.User != nil || parsedURL.RawQuery != "" ||
			parsedURL.ForceQuery || parsedURL.Fragment != "" {
			errs = append(errs, errors.New("experimental AgentKit builder requires an absolute http(s) --model-base-url without credentials, query, or fragment"))
		}
	}
	if plan.Agent.Model.Protocol != "openai-compatible" {
		errs = append(errs, fmt.Errorf("experimental AgentKit builder does not support model protocol %q", plan.Agent.Model.Protocol))
	}
	if plan.Agent.Policy != nil {
		errs = append(errs, errors.New("experimental AgentKit builder cannot enforce a declared policy"))
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
	} else {
		// Digest validation has already parsed the reference successfully.
		harness, _ := reference.ParseNormalizedNamed(plan.Harness.ImageRef)
		if reference.TrimNamed(harness).Name() != agentKitHarness {
			errs = append(errs, fmt.Errorf("experimental AgentKit builder requires harness repository %s for runtime %s", agentKitHarness, agentKitRuntime))
		}
	}
	if plan.Composition.Platform.String() != "linux/amd64" && plan.Composition.Platform.String() != "linux/arm64" {
		errs = append(errs, fmt.Errorf("experimental AgentKit builder does not support platform %s", plan.Composition.Platform))
	}
	return errors.Join(errs...)
}

func validateDigestReference(name, value string) error {
	if err := suitecore.ValidateImageReference(value); err != nil {
		return fmt.Errorf("%s must be a registry-qualified, digest-addressed image reference: %w", name, err)
	}
	return nil
}

func (b *Builder) agentkitFile(plan kmxsuite.SandboxPlan) ([]byte, error) {
	return b.agentkitFileWithLabel(plan, "")
}

func (b *Builder) agentkitFileWithLabel(plan kmxsuite.SandboxPlan, label string) ([]byte, error) {
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
	if label != "" {
		config.Metadata.Labels = map[string]string{kmxsuite.ImageDeploymentLabel: label}
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
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
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
