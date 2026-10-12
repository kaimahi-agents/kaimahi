package app

import (
	"context"
	"fmt"
	"strings"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const (
	orkaSuiteMarker         = "kaimahi.dev/suite"
	orkaSuiteDigestMarker   = "kaimahi.dev/suite-digest"
	orkaSuiteMemberMarker   = "kaimahi.dev/suite-member"
	orkaSuiteRendererMarker = "kaimahi.dev/suite-renderer"
	orkaSuiteBindingMarker  = "kaimahi.dev/suite-binding-digest"
	orkaSuiteRenderedMarker = "kaimahi.dev/suite-rendered-digest"
)

var orkaSuiteMarkerKeys = []string{orkaSuiteMarker, orkaSuiteDigestMarker, orkaSuiteMemberMarker, orkaSuiteRendererMarker, orkaSuiteBindingMarker, orkaSuiteRenderedMarker}

// Ownership is presence-based, not value-based: null and malformed markers
// cannot make an object available to another ownership domain.
func orkaHasSuiteMarkers[V any](annotations map[string]V) bool {
	for _, key := range orkaSuiteMarkerKeys {
		if _, present := annotations[key]; present {
			return true
		}
	}
	return false
}

func orkaHasBundleMarkers(annotations map[string]any) bool {
	for _, key := range []string{orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker} {
		if _, present := annotations[key]; present {
			return true
		}
	}
	return false
}

func refuseSuiteOrkaOwnership(doc map[string]any) error {
	meta, _ := doc["metadata"].(map[string]any)
	annotations, _ := meta["annotations"].(map[string]any)
	if orkaHasSuiteMarkers(annotations) {
		return fmt.Errorf("%s/%s has suite ownership markers; refusing legacy operation", doc["kind"], meta["name"])
	}
	return nil
}

type orkaSuiteOwnership struct{ Suite, Digest, Member, Renderer, BindingDigest, RenderedDigest string }

func (owner orkaSuiteOwnership) markers() map[string]any {
	return map[string]any{orkaSuiteMarker: owner.Suite, orkaSuiteDigestMarker: owner.Digest, orkaSuiteMemberMarker: owner.Member, orkaSuiteRendererMarker: owner.Renderer, orkaSuiteBindingMarker: owner.BindingDigest, orkaSuiteRenderedMarker: owner.RenderedDigest}
}

func validOrkaSuiteDigest(value any) bool {
	digest, ok := value.(string)
	return ok && strings.HasPrefix(digest, "sha256:") && validOrkaMarkerDigest(strings.TrimPrefix(digest, "sha256:"))
}

func (owner orkaSuiteOwnership) validate() error {
	if strings.TrimSpace(owner.Suite) == "" || strings.TrimSpace(owner.Member) == "" || owner.Renderer != "kmx.native-orka/v1" || !validOrkaSuiteDigest(owner.Digest) || !validOrkaMarkerDigest(owner.BindingDigest) || !validOrkaMarkerDigest(owner.RenderedDigest) {
		return fmt.Errorf("invalid or incomplete suite ownership identity")
	}
	return nil
}

// The legacy wrappers retain their RenderedBundle identity. Suite callers
// supply S/B/D directly, without representing any of them as a portable digest.
type orkaPlacementIdentity struct {
	portableDigest, renderedDigest string
	suite                          *orkaSuiteOwnership
}

func legacyOrkaPlacementIdentity(rendered agentruntime.RenderedBundle) orkaPlacementIdentity {
	return orkaPlacementIdentity{portableDigest: rendered.PortableDigest(), renderedDigest: rendered.RenderedDigest()}
}

func (identity orkaPlacementIdentity) markers(name string) map[string]any {
	if identity.suite != nil {
		return identity.suite.markers()
	}
	return map[string]any{orkaBundleMarker: name, orkaPortableMarker: identity.portableDigest, orkaRenderedMarker: identity.renderedDigest}
}

func orkaMarkersEqual(annotations, markers map[string]any) bool {
	for key, want := range markers {
		if annotations[key] != want {
			return false
		}
	}
	return true
}

func (identity orkaPlacementIdentity) inspectOwner(kind, name string, annotations map[string]any) (bool, error) {
	if identity.suite != nil {
		if orkaHasBundleMarkers(annotations) {
			return false, fmt.Errorf("%s/%s has legacy bundle ownership markers; refusing suite reconciliation", kind, name)
		}
		if !orkaHasSuiteMarkers(annotations) {
			return false, fmt.Errorf("%s/%s has no suite ownership marker; refusing suite adoption", kind, name)
		}
		if !validOrkaSuiteDigest(annotations[orkaSuiteDigestMarker]) || !validOrkaMarkerDigest(annotations[orkaSuiteBindingMarker]) || !validOrkaMarkerDigest(annotations[orkaSuiteRenderedMarker]) {
			return false, fmt.Errorf("%s/%s has incomplete suite ownership markers; refusing reconciliation", kind, name)
		}
		for key, want := range map[string]string{orkaSuiteMarker: identity.suite.Suite, orkaSuiteMemberMarker: identity.suite.Member, orkaSuiteRendererMarker: identity.suite.Renderer} {
			if annotations[key] != want {
				return false, fmt.Errorf("%s/%s has foreign or incomplete suite ownership; refusing reconciliation", kind, name)
			}
		}
		return true, nil
	}
	if orkaHasSuiteMarkers(annotations) {
		return false, fmt.Errorf("%s/%s has suite ownership markers; refusing legacy reconciliation", kind, name)
	}
	if owner, marked := annotations[orkaBundleMarker]; marked {
		if owner != name {
			return false, fmt.Errorf("%s/%s belongs to another bundle; refusing reconciliation", kind, name)
		}
		if !validOrkaMarkerDigest(annotations[orkaPortableMarker]) || !validOrkaMarkerDigest(annotations[orkaRenderedMarker]) {
			return false, fmt.Errorf("%s/%s has incomplete ownership marker; refusing reconciliation", kind, name)
		}
		return true, nil
	}
	if orkaHasBundleMarkers(annotations) {
		return false, fmt.Errorf("%s/%s has incomplete ownership marker; refusing reconciliation", kind, name)
	}
	return false, nil
}

func (a *App) inspectSuiteOrkaReconcile(ctx context.Context, ns string, desired map[string]any, owner orkaSuiteOwnership) (orkaReconcileCheck, error) {
	if err := owner.validate(); err != nil {
		return orkaReconcileCheck{}, err
	}
	return a.inspectOrkaPlacement(ctx, ns, desired, orkaPlacementIdentity{suite: &owner})
}

func (a *App) planSuiteOrkaReconcile(ctx context.Context, bundle *scaffold.OrkaBundle, ns string, owner orkaSuiteOwnership) ([]orkaReconcileCheck, error) {
	if err := owner.validate(); err != nil {
		return nil, err
	}
	return a.planOrkaPlacement(ctx, bundle, ns, orkaPlacementIdentity{suite: &owner})
}

func (a *App) verifySuiteOrkaReconcile(ctx context.Context, ns string, desired map[string]any, owner orkaSuiteOwnership, id orkaIdentity) error {
	if err := owner.validate(); err != nil {
		return err
	}
	return a.verifyOrkaPlacement(ctx, ns, desired, orkaPlacementIdentity{suite: &owner}, id)
}
