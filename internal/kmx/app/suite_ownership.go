package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const suiteCandidateRunMarker = "kaimahi.dev/suite-evaluation-run"

type orkaSuiteOwnership struct{ Suite, Digest, Member, Renderer, BindingDigest, RenderedDigest, EvaluationRun string }
type orkaPlacementIdentity struct {
	suite *orkaSuiteOwnership
}

func suiteNativeOwner(s suiteNativeSubject) *orkaSuiteOwnership {
	return &orkaSuiteOwnership{s.Suite, s.SuiteDigest, s.Member, s.Renderer, s.BindingDigest, s.RenderedDigest, s.EvaluationRun}
}
func (o *orkaSuiteOwnership) markers() map[string]any {
	markers := map[string]any{orkaSuiteMarkers[0]: o.Suite, orkaSuiteMarkers[1]: o.Digest, orkaSuiteMarkers[2]: o.Member, orkaSuiteMarkers[3]: o.Renderer, orkaSuiteMarkers[4]: o.BindingDigest, orkaSuiteMarkers[5]: o.RenderedDigest}
	if o.EvaluationRun != "" {
		markers[suiteCandidateRunMarker] = o.EvaluationRun
	}
	return markers
}
func suiteOnlyMarkers(ann map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range orkaSuiteReservedMarkers {
		if v, ok := ann[k]; ok {
			out[k] = v
		}
	}
	return out
}
func (o *orkaSuiteOwnership) valid() bool {
	return (o.EvaluationRun == "" || evaluationRunID(o.EvaluationRun)) && scaffold.ValidateObjectName(o.Suite) == nil && scaffold.ValidateObjectName(o.Member) == nil && o.Renderer == "kmx.native-orka/v1" && strings.HasPrefix(o.Digest, "sha256:") && validOrkaMarkerDigest(strings.TrimPrefix(o.Digest, "sha256:")) && validOrkaMarkerDigest(o.BindingDigest) && validOrkaMarkerDigest(o.RenderedDigest)
}
func (o *orkaSuiteOwnership) check(ann map[string]any) error {
	for _, k := range []string{orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker} {
		if _, ok := ann[k]; ok {
			return fmt.Errorf("legacy ownership blocks native placement")
		}
	}
	values := make([]string, 0, 6)
	for _, k := range orkaSuiteMarkers {
		v, ok := ann[k].(string)
		if !ok {
			return fmt.Errorf("missing or malformed native ownership")
		}
		values = append(values, v)
	}
	run := ""
	if value, present := ann[suiteCandidateRunMarker]; present {
		var ok bool
		run, ok = value.(string)
		if !ok || !evaluationRunID(run) {
			return fmt.Errorf("malformed native candidate ownership")
		}
	}
	if run != o.EvaluationRun {
		return fmt.Errorf("foreign native candidate ownership")
	}
	old := orkaSuiteOwnership{values[0], values[1], values[2], values[3], values[4], values[5], run}
	if !old.valid() || old.Suite != o.Suite || old.Member != o.Member || old.Renderer != o.Renderer {
		return fmt.Errorf("foreign or invalid native ownership")
	}
	return nil
}

var orkaSuiteMarkers = []string{"kaimahi.dev/suite", "kaimahi.dev/suite-digest", "kaimahi.dev/suite-member", "kaimahi.dev/suite-renderer", "kaimahi.dev/suite-binding-digest", "kaimahi.dev/suite-rendered-digest"}

var orkaSuiteReservedMarkers = append(append([]string(nil), orkaSuiteMarkers...), suiteCandidateRunMarker)

// Even malformed or null annotations reserve a resource. Never strip them
// before deciding whether a legacy caller may adopt, copy or retire it.
func hasOrkaSuiteReservation(annotations any) bool {
	for _, key := range orkaSuiteReservedMarkers {
		switch ann := annotations.(type) {
		case map[string]any:
			if _, ok := ann[key]; ok {
				return true
			}
		case map[string]string:
			if _, ok := ann[key]; ok {
				return true
			}
		}
	}
	return false
}

func inspectSuiteNativeLive(raw []byte, namespace, kind, name string, owner *orkaSuiteOwnership) (map[string]any, orkaIdentity, error) {
	var doc map[string]any
	var typed struct {
		Metadata struct {
			Name, Namespace, UID, ResourceVersion string
			Generation                            int64
			DeletionTimestamp                     *string
		}
	}
	if json.Unmarshal(raw, &doc) != nil || json.Unmarshal(raw, &typed) != nil {
		return nil, orkaIdentity{}, fmt.Errorf("%s/%s native identity is unknown", kind, name)
	}
	m := typed.Metadata
	_, specObject := doc["spec"].(map[string]any)
	if doc["kind"] != kind || m.Name != name || m.Namespace != namespace || m.UID == "" || m.ResourceVersion == "" || m.Generation < 1 || m.DeletionTimestamp != nil || !specObject {
		return nil, orkaIdentity{}, fmt.Errorf("%s/%s invalid or terminating native identity", kind, name)
	}
	meta, _ := doc["metadata"].(map[string]any)
	ann, _ := meta["annotations"].(map[string]any)
	if err := owner.check(ann); err != nil {
		return nil, orkaIdentity{}, fmt.Errorf("%s/%s: %w", kind, name, err)
	}
	return doc, orkaIdentity{Kind: kind, Name: name, UID: m.UID, Generation: m.Generation}, nil
}

func refuseOrkaSuiteDocument(doc map[string]any) error {
	meta, _ := doc["metadata"].(map[string]any)
	if hasOrkaSuiteReservation(meta["annotations"]) {
		return fmt.Errorf("%v/%s reserves native suite ownership; refusing legacy operation", doc["kind"], orkaObjectName(doc))
	}
	return nil
}
