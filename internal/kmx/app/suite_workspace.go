package app

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/opencontainers/go-digest"
	"oras.land/oras-go/v2/registry"
)

// CreateSuiteMember authors a workspace offline, without loading target settings.
func (a *App) CreateSuiteMember(ctx context.Context, root string, opt workspace.CreateOptions) error {
	if err := workspace.Create(ctx, root, opt); err != nil {
		return err
	}
	_, err := fmt.Fprintf(a.Out, "Created native-Orka AgentSuite workspace %s with member %s (not deployed)\n", root, opt.Name)
	return err
}

// PushSuiteWorkspace transfers the prepared OCI graph, never re-packing it at
// publication. The native execution profile requires no member images.
func (a *App) PushSuiteWorkspace(ctx context.Context, root, reference, layout string, plainHTTP, force bool, provenance SuitePushProvenance) (oras.PushResult, error) {
	return a.pushSuiteWorkspace(ctx, root, reference, layout, plainHTTP, force, provenance, nil)
}

// expectedS binds publication to the full packaged artifact, not merely its
// public graph or source spelling. Nil is the explicit unevaluated path.
func (a *App) pushSuiteWorkspace(ctx context.Context, root, reference, layout string, plainHTTP, force bool, provenance SuitePushProvenance, expectedS *digest.Digest) (oras.PushResult, error) {
	var expected digest.Digest
	if expectedS != nil {
		expected = *expectedS
		if err := expected.Validate(); err != nil || expected.Algorithm() != digest.SHA256 {
			return oras.PushResult{}, fmt.Errorf("expected suite identity must be a valid SHA256 digest")
		}
	}
	if layout == "" {
		parsed, err := registry.ParseReference(reference)
		if err != nil {
			return oras.PushResult{}, fmt.Errorf("parse AgentSuite registry reference: %w", err)
		}
		if err := parsed.ValidateReferenceAsTag(); err != nil {
			return oras.PushResult{}, fmt.Errorf("AgentSuite registry push requires a tag: %w", err)
		}
	}
	state, err := config.StateDir()
	if err != nil {
		return oras.PushResult{}, err
	}
	prepared, err := workspace.Prepare(ctx, root, filepath.Join(state, "suites"))
	if err != nil {
		return oras.PushResult{}, err
	}
	if expectedS != nil && prepared.Descriptor.Digest != expected {
		return oras.PushResult{}, fmt.Errorf("prepared suite does not match the expected publication identity")
	}
	var options oras.PushOptions
	var sourceWarning string
	if layout == "" && provenance.Enabled {
		inputs, err := workspace.SourceFiles(ctx, root, prepared)
		if err != nil {
			return oras.PushResult{}, err
		}
		options, sourceWarning = suitePushOptions(ctx, root, provenance, inputs)
	}
	if err := workspace.CheckCurrent(ctx, root, prepared); err != nil {
		return oras.PushResult{}, err
	}
	var result oras.PushResult
	if layout != "" {
		result, err = oras.PushLayout(ctx, prepared.Path, layout, reference, prepared.Descriptor)
	} else {
		result, err = oras.PushLayoutRegistry(ctx, prepared.Path, reference, plainHTTP, force, prepared.Descriptor, options)
	}
	if err != nil {
		return oras.PushResult{}, err
	}
	if result.Descriptor.Digest != prepared.Descriptor.Digest {
		return oras.PushResult{}, fmt.Errorf("publication returned a different prepared suite identity")
	}
	if sourceWarning != "" && result.Provenance != nil {
		result.Warnings = append(result.Warnings, sourceWarning)
	}
	if _, err := fmt.Fprintf(a.Out, "Pushed native-Orka AgentSuite %s to %s (%s)\nRequired member images: []\n", prepared.Name, result.Path, result.Descriptor.Digest); err != nil {
		return result, err
	}
	if layout == "" {
		parsed, err := registry.ParseReference(reference)
		if err != nil {
			return result, err
		}
		_, err = fmt.Fprintf(a.Out, "Pin this AgentSuite: %s/%s@%s\n", parsed.Registry, parsed.Repository, result.Descriptor.Digest)
		return result, err
	}
	return result, nil
}
