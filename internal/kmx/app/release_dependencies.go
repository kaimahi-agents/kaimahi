package app

import (
	"context"
	"reflect"

	suitecore "github.com/agent-primitives/suite/core"
	godigest "github.com/opencontainers/go-digest"
)

// This proof is born with the target, not reconstructed from a promotion-source
// receipt. Private metadata only: no Secret values or Provider spec body.
type releaseTargetDependencies struct {
	Version            int                `json:"version"`
	Target             ReleaseLiftTarget  `json:"target"`
	Binding            suiteNativeBinding `json:"binding"`
	Runtime            suiteNativeRuntime `json:"runtime"`
	ProviderSpecDigest string             `json:"providerSpecDigest"`
}

func (a *App) readReleaseTargetDependencies(ctx context.Context, s suiteNativeSubject) (*releaseTargetDependencies, error) {
	p := s.Binding.Provider
	raw, err := a.orkaCapture(ctx, nil, "-n", p.Namespace, "get", "providers.core.orka.ai", p.Name, "-o", "json")
	var observed struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name, Namespace, UID, ResourceVersion string
			Generation                            int64
			DeletionTimestamp                     *string
		} `json:"metadata"`
		Spec map[string]any `json:"spec"`
	}
	if err != nil || !statusDecode(raw, &observed) || observed.Kind != "Provider" || observed.Spec == nil || observed.Metadata.DeletionTimestamp != nil ||
		observed.Metadata.Name != p.Name || observed.Metadata.Namespace != p.Namespace || observed.Metadata.UID != p.UID || observed.Metadata.ResourceVersion != p.ResourceVersion || observed.Metadata.Generation != p.Generation {
		return nil, errReleaseUpdateCompatibility
	}
	// The complete spec read is tied to the same strict prepared Provider RV;
	// bracket Secret/runtime/target identity as well, not merely a stable hash.
	if err := a.verifySuiteNativeInputs(ctx, s); err != nil {
		return nil, errReleaseUpdateCompatibility
	}
	canonical, err := suitecore.Marshal(observed.Spec)
	if err != nil {
		return nil, errReleaseUpdateCompatibility
	}
	proof := &releaseTargetDependencies{Version: 1, Target: releaseLiftTarget(s.Target), Binding: s.Binding, Runtime: s.Runtime, ProviderSpecDigest: godigest.FromBytes(canonical).String()}
	if !validReleaseTargetDependencies(proof) {
		return nil, errReleaseUpdateCompatibility
	}
	return proof, nil
}

func validReleaseTargetDependencies(d *releaseTargetDependencies) bool {
	if d == nil || d.Version != 1 || !statusTargetValid(d.Target) || !suitecore.IsValidDigest(d.ProviderSpecDigest) ||
		d.Binding.Provider.UID == "" || d.Binding.Provider.Generation < 1 || d.Binding.Provider.ResourceVersion == "" ||
		d.Binding.Secret.UID == "" || d.Binding.Secret.ResourceVersion == "" || !d.Binding.Secret.KeyPresent {
		return false
	}
	// Leave the existing immutable journal's receipt/summary/outcome headroom
	// intact. Reject before reserving the shared record or sending an object.
	raw, err := suitecore.Marshal(d)
	return err == nil && len(raw) <= 8<<10
}

func sameReleaseTargetDependencies(a, b *releaseTargetDependencies) bool {
	if !validReleaseTargetDependencies(a) || !validReleaseTargetDependencies(b) {
		return false
	}
	old, fresh := *a, *b
	// Only BETWEEN operations: context aliases supply access, and an inference
	// Provider status update may change RV without changing its stable identity.
	// No Secret RV, runtime, generation, reference or spec exception is allowed.
	old.Target.Context, fresh.Target.Context = "", ""
	old.Binding.Provider.ResourceVersion, fresh.Binding.Provider.ResourceVersion = "", ""
	return reflect.DeepEqual(old, fresh)
}
