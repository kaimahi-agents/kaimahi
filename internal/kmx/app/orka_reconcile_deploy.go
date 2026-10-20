package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const (
	orkaBundleMarker   = "kaimahi.dev/bundle"
	orkaPortableMarker = "kaimahi.dev/portable-digest"
	orkaRenderedMarker = "kaimahi.dev/rendered-digest"
	orkaOriginMarker   = "kaimahi.dev/origin"
)

type orkaReconcileCheck struct {
	existing, candidate map[string]any
	outcome             agentruntime.ResourceOutcome
	id                  orkaIdentity
}

// applyOrkaReconcile performs the one write an inspection decided on: create
// an absent resource, replace an adopted or updated one under its
// resourceVersion, refresh stale markers, or nothing for a reused resource.
// Callers inspect immediately beforehand, so the decision is current.
func (app *App) applyOrkaReconcile(ctx context.Context, namespace string, check orkaReconcileCheck) (orkaIdentity, error) {
	id := check.id
	switch check.outcome {
	case agentruntime.ResourceCreated:
		return app.createOrkaObject(ctx, namespace, check.candidate)
	case agentruntime.ResourceUpdated:
		if check.outcome == agentruntime.ResourceUpdated {
			app.notef("Updating %s/%s; differing rendered fields: %s", id.Kind, id.Name, strings.Join(orkaChangedFields(check.existing, check.candidate), ", "))
		}
		return app.replaceOrkaReconcile(ctx, namespace, check.candidate, id)
	case agentruntime.ResourceReused:
		app.notef("Reusing %s/%s", id.Kind, id.Name)
	}
	return id, nil
}

func (app *App) planOrkaPlacement(ctx context.Context, identity orkaPlacementIdentity, bundle *scaffold.OrkaBundle, namespace string) ([]orkaReconcileCheck, error) {
	if bundle.Task != nil {
		return nil, fmt.Errorf("Orka reconcile refuses Task/%s: Tasks are one-shot executions", orkaObjectName(bundle.Task))
	}
	crds := map[string][]byte{}
	for _, kind := range []string{"Agent", "Provider", "Task"} {
		name := strings.ToLower(kind) + "s.core.orka.ai"
		raw, err := app.orkaCapture(ctx, nil, "get", "crd", name, "-o", "json")
		if err != nil {
			return nil, fmt.Errorf("cannot read installed %s CRD: %w", name, err)
		}
		crds[kind] = raw
	}
	validator, err := app.installedOrkaSchema(crds)
	if err != nil {
		return nil, err
	}
	if err := validateOrkaBundle(bundle, validator); err != nil {
		return nil, err
	}
	// Deploy separately reinspects immediately before each mutation: another
	// writer may change these read-only decisions after preflight.
	var checks []orkaReconcileCheck
	for _, doc := range []map[string]any{bundle.Provider, bundle.Agent} {
		check, err := app.inspectOrkaPlacement(ctx, namespace, doc, identity)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func (a *App) inspectOrkaPlacement(ctx context.Context, namespace string, desired map[string]any, identity orkaPlacementIdentity) (orkaReconcileCheck, error) {
	if err := refuseOrkaSuiteDocument(desired); err != nil {
		return orkaReconcileCheck{}, err
	}
	kind, name := desired["kind"].(string), orkaObjectName(desired)
	check := orkaReconcileCheck{id: orkaIdentity{Kind: kind, Name: name}}
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", orkaPlural(kind), name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return check, fmt.Errorf("cannot inspect %s/%s: %w", kind, name, err)
	}
	marker := identity.suite.markers()
	if len(raw) == 0 {
		check.outcome = agentruntime.ResourceCreated
		doc, err := cloneOrkaDoc(desired)
		if err != nil {
			return check, fmt.Errorf("prepare %s/%s create: %w", kind, name, err)
		}
		addOrkaMarker(doc, marker)
		body, err := json.Marshal(doc)
		if err != nil {
			return check, fmt.Errorf("encode %s/%s create: %w", kind, name, err)
		}
		if _, err := a.orkaCapture(ctx, body, "-n", namespace, "create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json"); err != nil {
			return check, fmt.Errorf("%s/%s strict server create preflight failed: %w", kind, name, err)
		}
		check.candidate = doc
		return check, nil
	}
	var live map[string]any
	if json.Unmarshal(raw, &live) != nil {
		return check, fmt.Errorf("invalid %s/%s response", kind, name)
	}
	check.existing = live
	meta, _ := live["metadata"].(map[string]any)
	uid, _ := meta["uid"].(string)
	version, _ := meta["resourceVersion"].(string)
	generation, _ := meta["generation"].(float64)
	if live["kind"] != kind || meta["name"] != name || meta["namespace"] != namespace || uid == "" || version == "" || generation < 1 || meta["deletionTimestamp"] != nil {
		return check, fmt.Errorf("%s/%s has invalid or terminating identity; refusing reconciliation", kind, name)
	}
	check.id.UID, check.id.Generation = uid, int64(generation)
	annotations, _ := meta["annotations"].(map[string]any)
	_, check.id, err = inspectSuiteNativeLive(raw, namespace, kind, name, identity.suite)
	if err != nil {
		return check, err
	}
	candidate := map[string]any{"apiVersion": desired["apiVersion"], "kind": kind, "metadata": meta, "spec": desired["spec"]}
	body, err := json.Marshal(candidate)
	if err != nil {
		return check, fmt.Errorf("encode %s/%s server dry-run: %w", kind, name, err)
	}
	admittedRaw, err := a.orkaCapture(ctx, body, "-n", namespace, "replace", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return check, fmt.Errorf("cannot normalize %s/%s on server: %w", kind, name, err)
	}
	var admitted map[string]any
	if json.Unmarshal(admittedRaw, &admitted) != nil {
		return check, fmt.Errorf("invalid server dry-run response for %s/%s", kind, name)
	}
	admittedMeta, _ := admitted["metadata"].(map[string]any)
	if admitted["kind"] != kind || admittedMeta["name"] != name || admittedMeta["namespace"] != namespace || admittedMeta["uid"] != uid || admittedMeta["resourceVersion"] != version || admitted["spec"] == nil {
		return check, fmt.Errorf("server dry-run returned a different or invalid %s/%s; refusing reconciliation", kind, name)
	}
	same := reflect.DeepEqual(live["spec"], admitted["spec"]) && orkaRenderedMetadataEqual(meta, desired["metadata"].(map[string]any))
	if same && reflect.DeepEqual(identity.suite.markers(), suiteOnlyMarkers(annotations)) {
		check.outcome = agentruntime.ResourceReused
		return check, nil
	}
	check.outcome = agentruntime.ResourceUpdated
	// Preserve other metadata (including resourceVersion), overlay rendered
	// fields and ownership, and let the API server reject stale replacements.
	replacement, err := cloneOrkaDoc(candidate)
	if err != nil {
		return check, fmt.Errorf("prepare %s/%s replace: %w", kind, name, err)
	}
	replacement["spec"] = admitted["spec"]
	replacementMeta := replacement["metadata"].(map[string]any)
	currentAnnotations, _ := replacementMeta["annotations"].(map[string]any)
	if currentAnnotations == nil {
		currentAnnotations = map[string]any{}
	}
	wantedAnnotations, _ := desired["metadata"].(map[string]any)["annotations"].(map[string]any)
	for k, v := range wantedAnnotations {
		if k != orkaOriginMarker {
			currentAnnotations[k] = v
		}
	}
	if _, rendered := wantedAnnotations["kaimahi.dev/description"]; !rendered {
		delete(currentAnnotations, "kaimahi.dev/description")
	}
	replacementMeta["annotations"] = currentAnnotations
	wantedLabels, _ := desired["metadata"].(map[string]any)["labels"].(map[string]any)
	if len(wantedLabels) > 0 {
		currentLabels, _ := replacementMeta["labels"].(map[string]any)
		if currentLabels == nil {
			currentLabels = map[string]any{}
		}
		for k, v := range wantedLabels {
			currentLabels[k] = v
		}
		replacementMeta["labels"] = currentLabels
	}
	addOrkaMarker(replacement, marker)
	if identity.suite != nil {
		body, err := json.Marshal(replacement)
		if err != nil {
			return check, err
		}
		final, err := a.orkaCapture(ctx, body, "-n", namespace, "replace", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
		if err != nil {
			return check, fmt.Errorf("%s/%s final marked admission failed: %w", kind, name, err)
		}
		admittedFinal, _, err := inspectSuiteNativeLive(final, namespace, kind, name, identity.suite)
		finalMeta, _ := admittedFinal["metadata"].(map[string]any)
		finalAnnotations, _ := finalMeta["annotations"].(map[string]any)
		// Admission may advance generation or managedFields. Bind identity,
		// normalized spec and reviewed metadata, not server-managed metadata.
		if err != nil || finalMeta["uid"] != uid || finalMeta["resourceVersion"] != version || !reflect.DeepEqual(admittedFinal["spec"], replacement["spec"]) || !orkaRenderedMetadataEqual(finalMeta, replacementMeta) || !reflect.DeepEqual(identity.suite.markers(), suiteOnlyMarkers(finalAnnotations)) {
			return check, fmt.Errorf("%s/%s final marked admission changed candidate", kind, name)
		}
	}
	check.candidate = replacement
	return check, nil
}

func validOrkaMarkerDigest(value any) bool {
	digest, ok := value.(string)
	if !ok || len(digest) != 64 {
		return false
	}
	for _, ch := range digest {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func orkaRenderedMetadataEqual(existing, desired map[string]any) bool {
	// Only rendered fields and kmx's generated description belong to the bundle:
	// a stale description is removed even when it is absent from the new render.
	// Other annotations and labels are independently managed.
	for _, key := range []string{"annotations", "labels"} {
		wanted, _ := desired[key].(map[string]any)
		actual, _ := existing[key].(map[string]any)
		for k, v := range wanted {
			if key == "annotations" && k == orkaOriginMarker {
				continue
			}
			if !reflect.DeepEqual(actual[k], v) {
				return false
			}
		}
	}
	wantedAnnotations, _ := desired["annotations"].(map[string]any)
	actualAnnotations, _ := existing["annotations"].(map[string]any)
	if _, wanted := wantedAnnotations["kaimahi.dev/description"]; !wanted && actualAnnotations["kaimahi.dev/description"] != nil {
		return false
	}
	return true
}

func cloneOrkaDoc(doc map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var copied map[string]any
	if err := json.Unmarshal(raw, &copied); err != nil {
		return nil, err
	}
	return copied, nil
}

func addOrkaMarker(doc, marker map[string]any) {
	meta := doc["metadata"].(map[string]any)
	annotations, _ := meta["annotations"].(map[string]any)
	if annotations == nil {
		annotations = map[string]any{}
		meta["annotations"] = annotations
	}
	for k, v := range marker {
		annotations[k] = v
	}
}

// Report paths, not field values: live objects are untrusted and may contain
// credentials even if the rendered Provider is credential-free.
func orkaChangedFields(live, candidate map[string]any) []string {
	var paths []string
	var walk func(string, any, any)
	walk = func(path string, old, new any) {
		a, aok := old.(map[string]any)
		b, bok := new.(map[string]any)
		if aok && bok {
			keys := map[string]bool{}
			for k := range a {
				keys[k] = true
			}
			for k := range b {
				keys[k] = true
			}
			for k := range keys {
				walk(path+"."+k, a[k], b[k])
			}
			return
		}
		if !reflect.DeepEqual(old, new) {
			paths = append(paths, path)
		}
	}
	walk("spec", live["spec"], candidate["spec"])
	oldMeta, _ := live["metadata"].(map[string]any)
	newMeta, _ := candidate["metadata"].(map[string]any)
	for _, field := range []string{"annotations", "labels"} {
		oldFields, _ := oldMeta[field].(map[string]any)
		newFields, _ := newMeta[field].(map[string]any)
		for k, v := range newFields {
			if k == orkaBundleMarker || k == orkaPortableMarker || k == orkaRenderedMarker || k == orkaOriginMarker {
				continue
			}
			if !reflect.DeepEqual(oldFields[k], v) {
				paths = append(paths, "metadata."+field+"."+k)
			}
		}
		if field == "annotations" && oldFields["kaimahi.dev/description"] != nil && newFields["kaimahi.dev/description"] == nil {
			paths = append(paths, "metadata.annotations.kaimahi.dev/description")
		}
	}
	slices.Sort(paths)
	return paths
}

// orkaAnnotationPointer escapes an annotation key for use inside a JSON Patch
// path (RFC 6901): "~" and then "/" must both escape, in that order.
func orkaAnnotationPointer(key string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(key)
}

func (a *App) replaceOrkaReconcile(ctx context.Context, namespace string, doc map[string]any, expected orkaIdentity) (orkaIdentity, error) {
	body, err := json.Marshal(doc)
	if err != nil {
		return orkaIdentity{}, err
	}
	raw, err := a.orkaCapture(ctx, body, "-n", namespace, "replace", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return orkaIdentity{}, fmt.Errorf("replace %s/%s refused (possibly concurrent change); rerun deploy: %w", expected.Kind, expected.Name, err)
	}
	var object orkaObject
	if json.Unmarshal(raw, &object) != nil || object.Kind != expected.Kind || object.Metadata.Name != expected.Name || object.Metadata.Namespace != namespace || object.Metadata.UID != expected.UID || object.Metadata.Generation < 1 || object.Metadata.DeletionTimestamp != nil {
		return orkaIdentity{}, fmt.Errorf("replace %s/%s returned a different or invalid identity; refusing continuation", expected.Kind, expected.Name)
	}
	expected.Generation = object.Metadata.Generation
	return expected, nil
}
