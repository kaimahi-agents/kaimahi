package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
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
	if err := app.guardOrkaCreate(ctx, opt); err != nil {
		return agentruntime.DeployResult{}, err
	}
	crds := map[string][]byte{}
	for _, kind := range []string{"Agent", "Provider", "Task"} {
		name := strings.ToLower(kind) + "s.core.orka.ai"
		raw, err := app.orkaCapture(ctx, nil, "get", "crd", name, "-o", "json")
		if err != nil {
			return agentruntime.DeployResult{}, fmt.Errorf("cannot read installed %s CRD: %w", name, err)
		}
		crds[kind] = raw
	}
	validator, err := orkaschema.Installed(crds)
	if err != nil {
		return agentruntime.DeployResult{}, err
	}
	if err := validateOrkaBundle(bundle, validator); err != nil {
		return agentruntime.DeployResult{}, err
	}
	// Check the entire plan before writing the first resource. Recheck each
	// resource immediately before mutation; the actual replace is versioned.
	docs := []map[string]any{bundle.Provider, bundle.Agent}
	for _, doc := range docs {
		if _, err := app.inspectOrkaReconcile(ctx, opt.Namespace, doc, rendered); err != nil {
			return agentruntime.DeployResult{}, err
		}
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
			app.notef("Reusing %s/%s", id.Kind, id.Name)
		}
		if err != nil {
			return agentruntime.DeployResult{}, err
		}
		if err := app.waitOrkaReady(ctx, opt.Namespace, id); err != nil {
			return agentruntime.DeployResult{}, err
		}
		result.Receipt.Resources = append(result.Receipt.Resources, agentruntime.ResourceResult{Kind: id.Kind, Name: id.Name, Namespace: opt.Namespace, UID: id.UID, Generation: id.Generation, Outcome: check.outcome})
		if id.Kind == "Agent" {
			result.Ref = agentruntime.AgentRef{Runtime: a.ID(), Context: target.Context, Namespace: opt.Namespace, Kind: orkaPlural("Agent"), Name: id.Name, UID: id.UID}
		}
	}
	return result, nil
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
		doc := cloneOrkaDoc(desired)
		addOrkaMarker(doc, marker)
		body, _ := json.Marshal(doc)
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
	if owner, ok := annotations[orkaBundleMarker]; ok && owner != name {
		return check, fmt.Errorf("%s/%s belongs to another bundle; refusing reconciliation", kind, name)
	}
	// A resource with partial ownership metadata is not safe to adopt.
	if annotations[orkaBundleMarker] == nil && (annotations[orkaPortableMarker] != nil || annotations[orkaRenderedMarker] != nil) ||
		annotations[orkaBundleMarker] != nil && (annotations[orkaPortableMarker] == nil || annotations[orkaRenderedMarker] == nil) {
		return check, fmt.Errorf("%s/%s has incomplete ownership marker; refusing reconciliation", kind, name)
	}
	candidate := map[string]any{"apiVersion": desired["apiVersion"], "kind": kind, "metadata": meta, "spec": desired["spec"]}
	body, _ := json.Marshal(candidate)
	admittedRaw, err := a.orkaCapture(ctx, body, "-n", namespace, "replace", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return check, fmt.Errorf("cannot normalize %s/%s on server: %w", kind, name, err)
	}
	var admitted map[string]any
	if json.Unmarshal(admittedRaw, &admitted) != nil {
		return check, fmt.Errorf("invalid server dry-run response for %s/%s", kind, name)
	}
	same := reflect.DeepEqual(live["spec"], admitted["spec"]) && orkaRenderedMetadataEqual(meta, desired["metadata"].(map[string]any))
	if annotations[orkaBundleMarker] == nil && !same {
		return check, fmt.Errorf("%s/%s has no bundle marker and differs from rendered fields; refusing reconciliation", kind, name)
	}
	if same && annotations[orkaBundleMarker] != nil {
		check.outcome = agentruntime.ResourceReused
		return check, nil
	}
	if same {
		check.outcome = agentruntime.ResourceAdopted
	} else {
		check.outcome = agentruntime.ResourceUpdated
	}
	// Preserve every other metadata field (including the resourceVersion) and
	// replace only rendered annotations and spec. No update can lose a concurrent
	// change: the API server rejects stale resourceVersion on replace.
	replacement := cloneOrkaDoc(candidate)
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
	replacementMeta["annotations"] = currentAnnotations
	addOrkaMarker(replacement, marker)
	check.existing = live
	check.candidate = replacement
	return check, nil
}

func orkaRenderedMetadataEqual(existing, desired map[string]any) bool {
	// Only fields actually rendered belong to the bundle. Other annotations or
	// labels are independently managed; ownership markers are treated separately.
	for _, key := range []string{"annotations", "labels"} {
		wanted, _ := desired[key].(map[string]any)
		actual, _ := existing[key].(map[string]any)
		for k, v := range wanted {
			if !reflect.DeepEqual(actual[k], v) {
				return false
			}
		}
	}
	return true
}

func cloneOrkaDoc(doc map[string]any) map[string]any {
	raw, _ := json.Marshal(doc)
	var copy map[string]any
	_ = json.Unmarshal(raw, &copy)
	return copy
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
	}
	sort.Strings(paths)
	return paths
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
