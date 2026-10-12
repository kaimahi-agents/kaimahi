package app

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
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
func (a *App) PushSuiteWorkspace(ctx context.Context, root, reference, layout string, plainHTTP, force bool) error {
	if layout == "" {
		parsed, err := registry.ParseReference(reference)
		if err != nil {
			return fmt.Errorf("parse AgentSuite registry reference: %w", err)
		}
		if err := parsed.ValidateReferenceAsTag(); err != nil {
			return fmt.Errorf("AgentSuite registry push requires a tag: %w", err)
		}
	}
	state, err := config.StateDir()
	if err != nil {
		return err
	}
	prepared, err := workspace.Prepare(ctx, root, filepath.Join(state, "suites"))
	if err != nil {
		return err
	}
	if err := workspace.CheckCurrent(ctx, root, prepared); err != nil {
		return err
	}
	var result oras.PushResult
	if layout != "" {
		result, err = oras.PushLayout(ctx, prepared.Path, layout, reference, prepared.Descriptor)
	} else {
		result, err = oras.PushLayoutRegistry(ctx, prepared.Path, reference, plainHTTP, force, prepared.Descriptor)
	}
	if err != nil {
		return err
	}
	if result.Descriptor.Digest != prepared.Descriptor.Digest {
		return fmt.Errorf("publication returned a different prepared suite identity")
	}
	if _, err := fmt.Fprintf(a.Out, "Pushed native-Orka AgentSuite %s to %s (%s)\nRequired member images: []\n", prepared.Name, result.Path, result.Descriptor.Digest); err != nil {
		return err
	}
	if layout == "" {
		parsed, err := registry.ParseReference(reference)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(a.Out, "Pin this AgentSuite: %s/%s@%s\n", parsed.Registry, parsed.Repository, result.Descriptor.Digest)
		return err
	}
	return nil
}
