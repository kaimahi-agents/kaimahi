package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const (
	orkaBundleMarker   = "kaimahi.dev/bundle"
	orkaPortableMarker = "kaimahi.dev/portable-digest"
	orkaRenderedMarker = "kaimahi.dev/rendered-digest"
)

type orkaReconcileCheck struct {
	existing, candidate map[string]any
	outcome             agentruntime.ResourceOutcome
	id                  orkaIdentity
	// markerRefresh distinguishes a reused outcome that still needs a write:
	// rendered fields are unchanged, but the ownership markers hold a stale
	// portable or rendered digest and must be rewritten under a resourceVersion
	// precondition, exactly like every other reconciled write. The refresh is a
	// JSON Patch that touches only the two digest annotations, never the spec.
	markerRefresh               bool
	markerRefreshVersion        string
	markerRefreshPortableDigest string
	markerRefreshRenderedDigest string
}

// reconcileOrka applies ownership annotations outside the immutable rendered
// documents: including a digest in the documents it hashes is self-referential.
// A receipt is returned only after both resources observe current-generation Ready.
func (a orkaRuntimeAdapter) reconcileOrka(ctx context.Context, rendered agentruntime.RenderedBundle, bundle *scaffold.OrkaBundle, opt CreateOptions) (agentruntime.DeployResult, error) {
	if bundle.Task != nil {
		return agentruntime.DeployResult{}, fmt.Errorf("Orka reconcile refuses Task/%s: Tasks are one-shot executions", orkaObjectName(bundle.Task))
	}
	if opt.DryRun {
		return agentruntime.DeployResult{}, fmt.Errorf("Orka reconcile requires an online deployment, not create --dry-run")
	}
	app := a.app
	if err := app.guardOrkaMutation(ctx, opt, "create or update owned Orka Provider and Agent in "+opt.Namespace); err != nil {
		return agentruntime.DeployResult{}, err
	}
	_, err := app.planOrkaReconcile(ctx, rendered, bundle, opt.Namespace)
	if err != nil {
		return agentruntime.DeployResult{}, err
	}
	key := bundle.Provider["spec"].(map[string]any)["secretRef"].(map[string]any)["key"].(string)
	if err := app.orkaProviderSecretPresent(ctx, opt.Namespace, opt.Secret, key); err != nil {
		return agentruntime.DeployResult{}, err
	}
	target := agentruntime.DeployTarget{Runtime: a.ID(), Namespace: opt.Namespace}
	if app.Cfg != nil {
		target.Context = app.Cfg.KubeContext
	}
	receipt := agentruntime.DeployReceipt{Bundle: opt.Name, PortableDigest: rendered.PortableDigest(), RenderedDigest: rendered.RenderedDigest(), Target: target}
	result := agentruntime.DeployResult{Receipt: receipt}
	docs := []map[string]any{bundle.Provider, bundle.Agent}
	for _, doc := range docs {
		check, err := app.inspectOrkaReconcile(ctx, opt.Namespace, doc, rendered)
		if err != nil {
			return agentruntime.DeployResult{}, err
		}
		id := check.id
		switch check.outcome {
		case agentruntime.ResourceCreated:
			id, err = app.createOrkaObject(ctx, opt.Namespace, check.candidate)
		case agentruntime.ResourceAdopted, agentruntime.ResourceUpdated:
			if check.outcome == agentruntime.ResourceUpdated {
				app.notef("Updating %s/%s; differing rendered fields: %s", id.Kind, id.Name, strings.Join(orkaChangedFields(check.existing, check.candidate), ", "))
			}
			id, err = app.replaceOrkaReconcile(ctx, opt.Namespace, check.candidate, id)
		case agentruntime.ResourceReused:
			if check.markerRefresh {
				app.notef("Refreshing stale ownership markers on %s/%s", id.Kind, id.Name)
				id, err = app.patchOrkaMarkers(ctx, opt.Namespace, id, check.markerRefreshVersion, check.markerRefreshPortableDigest, check.markerRefreshRenderedDigest)
			} else {
				app.notef("Reusing %s/%s", id.Kind, id.Name)
			}
		}
		if err != nil {
			return agentruntime.DeployResult{}, err
		}
		if err := app.waitOrkaReady(ctx, opt.Namespace, id); err != nil {
			return agentruntime.DeployResult{}, err
		}
		if err := app.verifyOrkaReconcile(ctx, opt.Namespace, doc, rendered, id); err != nil {
			return agentruntime.DeployResult{}, err
		}
		result.Receipt.Resources = append(result.Receipt.Resources, agentruntime.ResourceResult{Kind: id.Kind, Name: id.Name, Namespace: opt.Namespace, UID: id.UID, Generation: id.Generation, Outcome: check.outcome})
		if id.Kind == "Agent" {
			result.Ref = agentruntime.AgentRef{Runtime: a.ID(), Context: target.Context, Namespace: opt.Namespace, Kind: orkaPlural("Agent"), Name: id.Name, UID: id.UID}
		}
	}
	// An earlier resource can change while a later resource becomes Ready.
	for i, doc := range docs {
		id := result.Receipt.Resources[i]
		if err := app.verifyOrkaReconcile(ctx, opt.Namespace, doc, rendered, orkaIdentity{Kind: id.Kind, Name: id.Name, UID: id.UID, Generation: id.Generation}); err != nil {
			return agentruntime.DeployResult{}, err
		}
	}
	return result, nil
}

// planOrkaReconcile performs the exact installed-schema, server-admission and
// ownership decisions Deploy uses, for every resource before the first write.
// Inspect is intentionally read-only (server dry-runs do not persist objects).
func (app *App) planOrkaReconcile(ctx context.Context, rendered agentruntime.RenderedBundle, bundle *scaffold.OrkaBundle, namespace string) ([]orkaReconcileCheck, error) {
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
	validator, err := orkaschema.Installed(crds)
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
		check, err := app.inspectOrkaReconcile(ctx, namespace, doc, rendered)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func (a *App) verifyOrkaReconcile(ctx context.Context, namespace string, desired map[string]any, rendered agentruntime.RenderedBundle, id orkaIdentity) error {
	check, err := a.inspectOrkaReconcile(ctx, namespace, desired, rendered)
	if err != nil {
		return err
	}
	if check.outcome != agentruntime.ResourceReused || check.markerRefresh || check.id != id {
		return fmt.Errorf("%s/%s changed while waiting for Ready; refusing deployment receipt", id.Kind, id.Name)
	}
	return a.verifyOrkaReadyNow(ctx, namespace, id)
}

func (a *App) inspectOrkaReconcile(ctx context.Context, namespace string, desired map[string]any, rendered agentruntime.RenderedBundle) (orkaReconcileCheck, error) {
	kind, name := desired["kind"].(string), orkaObjectName(desired)
	check := orkaReconcileCheck{id: orkaIdentity{Kind: kind, Name: name}}
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", orkaPlural(kind), name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return check, fmt.Errorf("cannot inspect %s/%s: %w", kind, name, err)
	}
	marker := map[string]any{orkaBundleMarker: orkaObjectName(desired), orkaPortableMarker: rendered.PortableDigest(), orkaRenderedMarker: rendered.RenderedDigest()}
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
	meta, _ := live["metadata"].(map[string]any)
	uid, _ := meta["uid"].(string)
	version, _ := meta["resourceVersion"].(string)
	generation, _ := meta["generation"].(float64)
	if live["kind"] != kind || meta["name"] != name || meta["namespace"] != namespace || uid == "" || version == "" || generation < 1 || meta["deletionTimestamp"] != nil {
		return check, fmt.Errorf("%s/%s has invalid or terminating identity; refusing reconciliation", kind, name)
	}
	check.id.UID, check.id.Generation = uid, int64(generation)
	annotations, _ := meta["annotations"].(map[string]any)
	if owner, marked := annotations[orkaBundleMarker]; marked {
		if owner != name {
			return check, fmt.Errorf("%s/%s belongs to another bundle; refusing reconciliation", kind, name)
		}
		if !validOrkaMarkerDigest(annotations[orkaPortableMarker]) || !validOrkaMarkerDigest(annotations[orkaRenderedMarker]) {
			return check, fmt.Errorf("%s/%s has incomplete ownership marker; refusing reconciliation", kind, name)
		}
	} else if annotations[orkaPortableMarker] != nil || annotations[orkaRenderedMarker] != nil {
		return check, fmt.Errorf("%s/%s has incomplete ownership marker; refusing reconciliation", kind, name)
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
	if annotations[orkaBundleMarker] == nil && !same {
		return check, fmt.Errorf("%s/%s has no bundle marker and differs from rendered fields; refusing reconciliation", kind, name)
	}
	if same && annotations[orkaBundleMarker] != nil {
		if annotations[orkaPortableMarker] == marker[orkaPortableMarker] && annotations[orkaRenderedMarker] == marker[orkaRenderedMarker] {
			check.outcome = agentruntime.ResourceReused
			return check, nil
		}
		// A stale digest can accompany identical rendered fields (for example,
		// a portable comment edit). Reuse refreshes only the ownership markers,
		// via a JSON Patch guarded by a resourceVersion test precondition, so a
		// concurrent write is refused rather than silently overwritten. Spec and
		// other metadata are already known unchanged, so the patch never bumps
		// generation.
		check.outcome = agentruntime.ResourceReused
		check.markerRefresh = true
		check.markerRefreshVersion = version
		check.markerRefreshPortableDigest = rendered.PortableDigest()
		check.markerRefreshRenderedDigest = rendered.RenderedDigest()
		return check, nil
	}
	if same {
		check.outcome = agentruntime.ResourceAdopted
	} else {
		check.outcome = agentruntime.ResourceUpdated
	}
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
		currentAnnotations[k] = v
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
	check.existing = live
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
			if k == orkaBundleMarker || k == orkaPortableMarker || k == orkaRenderedMarker {
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

// patchOrkaMarkers rewrites only the two digest annotations on an otherwise
// unchanged resource, guarded by a resourceVersion test precondition so a
// concurrent change is refused rather than silently overwritten. The
// response's generation must equal the caller's expectation: any change
// there means the spec moved between inspect and write, which this patch
// must never cause and must not paper over.
func (a *App) patchOrkaMarkers(ctx context.Context, namespace string, expected orkaIdentity, resourceVersion, portableDigest, renderedDigest string) (orkaIdentity, error) {
	patch, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": resourceVersion},
		{"op": "replace", "path": "/metadata/annotations/" + orkaAnnotationPointer(orkaPortableMarker), "value": portableDigest},
		{"op": "replace", "path": "/metadata/annotations/" + orkaAnnotationPointer(orkaRenderedMarker), "value": renderedDigest},
	})
	if err != nil {
		return orkaIdentity{}, err
	}
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "patch", orkaPlural(expected.Kind), expected.Name, "--type=json", "-p", string(patch), "-o", "json")
	if err != nil {
		return orkaIdentity{}, fmt.Errorf("patch %s/%s markers refused (possibly concurrent change); rerun deploy: %w", expected.Kind, expected.Name, err)
	}
	var object orkaObject
	if json.Unmarshal(raw, &object) != nil || object.Kind != expected.Kind || object.Metadata.Name != expected.Name || object.Metadata.Namespace != namespace || object.Metadata.UID != expected.UID || object.Metadata.Generation != expected.Generation || object.Metadata.DeletionTimestamp != nil {
		return orkaIdentity{}, fmt.Errorf("patch %s/%s markers returned a different or invalid identity; refusing continuation", expected.Kind, expected.Name)
	}
	return expected, nil
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
