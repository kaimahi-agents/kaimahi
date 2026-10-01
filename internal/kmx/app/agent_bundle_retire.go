package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// RetireAgentBundleOptions identifies the target whose bundle-owned resources
// should be removed. The explicit target never falls back to current-context.
type RetireAgentBundleOptions struct {
	BundleDir, ToContext, ToNamespace string
	Plan, DeleteAdopted               bool
}

type retireDecision struct {
	kind, name, uid, version, action, reason string
	portableDigest, renderedDigest           string
	hasOrigin                                bool
	forceReleaseForAgent                     bool
}

type bundleRetireReceipt struct {
	Bundle string `json:"bundle"`
	Target struct {
		Context    string `json:"context"`
		Namespace  string `json:"namespace"`
		ClusterUID string `json:"clusterUID"`
		Agent      string `json:"agent"`
	} `json:"target"`
	At        time.Time              `json:"at"`
	Complete  bool                   `json:"complete"`
	Resources []retireDecisionRecord `json:"resources"`
}
type retireDecisionRecord struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	UID    string `json:"uid,omitempty"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

func bundleRetireReceiptPath(bundle, contextName, namespace, clusterUID string) string {
	key := sha256.Sum256([]byte(contextName + "\x00" + namespace + "\x00" + clusterUID))
	return filepath.Join(bundle, "receipts", fmt.Sprintf("retire-%x.json", key))
}

func resolveRetireNamespace(bundle, contextName, explicit string, selection bundleLiftSelection) (string, error) {
	namespace := explicit
	if namespace == "" && selection.Context == contextName {
		namespace = selection.Namespace
	}
	if namespace == "" {
		targets, err := loadBundleReceiptTargets(bundle)
		if err != nil {
			return "", err
		}
		for _, target := range targets {
			if target.Context != contextName {
				continue
			}
			if namespace != "" && namespace != target.Namespace {
				return "", fmt.Errorf("ambiguous recorded namespaces for context %s; specify --to-namespace", contextName)
			}
			namespace = target.Namespace
		}
	}
	if namespace == "" {
		namespace = OrkaNamespace
	}
	if scaffold.ValidateNamespace(namespace) != nil {
		return "", fmt.Errorf("invalid destination namespace")
	}
	return namespace, nil
}

func (a *App) RetireAgentBundle(opt RetireAgentBundleOptions) error {
	ctx := a.operationContext()
	if a.Cfg == nil || a.Run == nil {
		return fmt.Errorf("retire requires configured kubectl and streams")
	}
	bundle, err := resolveOrkaPath(opt.BundleDir)
	if err != nil {
		return fmt.Errorf("resolve bundle: %w", err)
	}
	if err := scaffold.RefuseKeyShapes(bundle); err != nil {
		return fmt.Errorf("refusing credential-shaped bundle path")
	}
	if err := checkLiftBundle(bundle); err != nil {
		return err
	}
	if err := checkLiftReceiptsDir(bundle); err != nil {
		return err
	}
	name, _, _, err := readBundlePortableAgent(bundle)
	if err != nil {
		return err
	}
	selectionPath, err := bundleLiftSelectionPath(bundle)
	if err != nil {
		return err
	}
	selection, err := loadBundleLiftSelection(selectionPath)
	if err != nil {
		return err
	}
	contextName := opt.ToContext
	if contextName == "" {
		contextName = selection.Context
	}
	if contextName == "" {
		return fmt.Errorf("retire requires --to-context (no target remembered for this bundle)")
	}
	namespace, err := resolveRetireNamespace(bundle, contextName, opt.ToNamespace, selection)
	if err != nil {
		return err
	}
	worker := *a
	cfg := *a.Cfg
	cfg.KubeContext, cfg.ContextSource = contextName, config.SourceFlag
	worker.Cfg = &cfg
	worker.guarded = false
	raw, err := worker.orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot read destination context metadata: %w", err)
	}
	kube, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return fmt.Errorf("cannot decode destination context metadata")
	}
	cluster, err := liftContextCluster(kube, contextName)
	if err != nil {
		return err
	}
	uid, err := worker.liftClusterUID(ctx)
	if err != nil {
		return err
	}
	if selection.Context == contextName && selection.ClusterUID != uid {
		return fmt.Errorf("stale remembered target: context %s now identifies another cluster", contextName)
	}
	// A historical lift receipt also binds the physical cluster when the
	// remembered selection was cleared by a previous retirement.
	targets, err := loadBundleReceiptTargets(bundle)
	if err != nil {
		return err
	}
	known := false
	match := false
	for _, target := range targets {
		if target.Context == contextName && target.Namespace == namespace {
			known = true
			if target.ReceiptFile == filepath.Base(bundleReceiptPath(bundle, contextName, namespace, uid)) {
				match = true
			}
		}
	}
	if known && !match {
		return fmt.Errorf("target changed: context %s namespace %s does not match this bundle's recorded cluster", contextName, namespace)
	}
	worker.notef("Retire destination: context %s, cluster %s, namespace %s", contextName, cluster, namespace)
	retired, err := worker.alreadyRetiredBundle(ctx, bundle, contextName, namespace, uid, name)
	if err != nil {
		return err
	}
	if retired {
		if !opt.Plan {
			if err := clearRetiredSelection(selectionPath, selection, contextName, namespace, uid); err != nil {
				return err
			}
		}
		worker.notef("Agent/%s and Provider/%s already retired; no changes", name, name)
		return nil
	}
	var partial bundleRetireReceipt
	if raw, err := os.ReadFile(bundleRetireReceiptPath(bundle, contextName, namespace, uid)); err == nil {
		if json.Unmarshal(raw, &partial) != nil {
			return fmt.Errorf("invalid retirement receipt for this target")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read retirement receipt: %w", err)
	}
	decisions := make([]retireDecision, 0, 2)
	var inspectionErr error
	for _, kind := range []string{"Agent", "Provider"} {
		decision, err := worker.inspectRetireObject(ctx, namespace, kind, name, opt.DeleteAdopted)
		if err != nil && !partial.Complete {
			for _, r := range partial.Resources {
				if r.Kind == kind && r.Name == name && r.Action == "release" && r.UID != "" {
					unmarked, checkErr := worker.inspectPreviouslyReleased(ctx, namespace, kind, name, r.UID)
					if checkErr == nil && unmarked {
						decision = retireDecision{kind: kind, name: name, uid: r.UID, action: "previously released", reason: r.Reason}
						err = nil
					}
				}
			}
		}
		if err != nil && inspectionErr == nil {
			inspectionErr = err
		}
		decisions = append(decisions, decision)
	}
	if inspectionErr == nil && (decisions[0].action == "release" || decisions[0].action == "previously released") {
		if decisions[1].action == "delete" {
			decisions[1].action = "release"
			decisions[1].forceReleaseForAgent = true
			decisions[1].reason = "Agent survives; Provider must remain available"
		} else if decisions[1].action == "absent" {
			inspectionErr = fmt.Errorf("Agent/%s would survive but Provider/%s is absent; refusing retirement", name, name)
		}
	}
	if inspectionErr == nil {
		record, ok := readBundleReceiptAt(bundleReceiptPath(bundle, contextName, namespace, uid))
		for _, d := range decisions {
			if d.action == "absent" {
				continue
			}
			matchedUID := false
			if ok && record.Receipt.Bundle == name && record.Receipt.Target.Context == contextName && record.Receipt.Target.Namespace == namespace {
				for _, r := range record.Receipt.Resources {
					if r.Kind == d.kind && r.Name == name && r.Namespace == namespace && r.UID == d.uid {
						matchedUID = true
						break
					}
				}
			}
			if !matchedUID {
				inspectionErr = fmt.Errorf("%s/%s has no matching lift receipt for its UID; cannot prove this local bundle owns it", d.kind, name)
			} else if d.action != "previously released" && (d.portableDigest != record.Receipt.PortableDigest || d.renderedDigest != record.Receipt.RenderedDigest) {
				inspectionErr = fmt.Errorf("%s/%s markers differ from the last successful lift; rerun lift, then retire", d.kind, name)
			}
			if inspectionErr != nil {
				break
			}
		}
	}
	if inspectionErr != nil {
		for _, d := range decisions {
			worker.notef("%s/%s: refused: no changes while ownership inspection is incomplete", d.kind, d.name)
		}
		worker.notef("Refusal: %v", inspectionErr)
		return inspectionErr
	}
	if err := worker.verifyRetireDependents(ctx, namespace, name, decisions[1].action == "delete"); err != nil {
		return err
	}
	for _, d := range decisions {
		worker.notef("%s/%s: %s (%s)", d.kind, d.name, d.action, d.reason)
	}
	if opt.Plan {
		worker.notef("Plan only: no resources, receipt or remembered target written")
		return nil
	}
	if err := worker.guardOrkaMutation(ctx, CreateOptions{Name: name, Namespace: namespace}, "retire owned Orka Agent and rendered Provider in "+namespace); err != nil {
		return err
	}
	if err := worker.verifyRetireDependents(ctx, namespace, name, decisions[1].action == "delete"); err != nil {
		return err
	}
	receipt := bundleRetireReceipt{Bundle: name, At: time.Now().UTC()}
	receipt.Target.Context, receipt.Target.Namespace, receipt.Target.ClusterUID, receipt.Target.Agent = contextName, namespace, uid, name
	for _, d := range decisions {
		action := d.action
		if action == "previously released" {
			action = "release"
		}
		receipt.Resources = append(receipt.Resources, retireDecisionRecord{Kind: d.kind, Name: d.name, UID: d.uid, Action: action, Reason: d.reason})
	}
	// Persist the UID-bound decisions before the first mutation so a failed
	// second operation can recognize an object already released by this run.
	if err := writeBundleRetireReceipt(bundle, receipt); err != nil {
		return fmt.Errorf("retirement not started: cannot save recovery record: %w", err)
	}
	for _, d := range decisions {
		if d.action == "delete" || d.action == "release" {
			if err := worker.applyRetireDecision(ctx, namespace, d); err != nil {
				return fmt.Errorf("retirement partially applied; inspect the target, resolve the blocker, then rerun retire: %w", err)
			}
		}
	}
	receipt.Complete = true
	if err := writeBundleRetireReceipt(bundle, receipt); err != nil {
		return fmt.Errorf("resources retired but receipt was not saved; inspect the target, then rerun retire: %w", err)
	}
	return clearRetiredSelection(selectionPath, selection, contextName, namespace, uid)
}

func clearRetiredSelection(path string, selection bundleLiftSelection, contextName, namespace, uid string) error {
	if selection.Context == contextName && selection.Namespace == namespace && selection.ClusterUID == uid {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("resources retired and receipt saved but remembered target was not cleared; rerun retire after repairing local state: %w", err)
		}
	}
	return nil
}

func (a *App) verifyRetireDependents(ctx context.Context, namespace, name string, deleteProvider bool) error {
	dependents, err := a.retireAgentDependents(ctx, namespace, name)
	if err != nil {
		a.notef("Agent/%s: refused: dependent inventory incomplete: %v", name, err)
		a.notef("Provider/%s: refused: dependent inventory incomplete", name)
		return fmt.Errorf("retire refused: dependent inventory incomplete: %w", err)
	}
	if len(dependents) > 0 {
		a.notef("Agent/%s: refused: dependents still reference it", name)
		a.notef("Provider/%s: refused: Agent remains in use", name)
		for _, d := range dependents {
			a.notef("  %s", d)
		}
		return fmt.Errorf("retire refused: %d dependent(s) still reference Agent/%s", len(dependents), name)
	}
	if deleteProvider {
		refs, err := a.retireProviderDependents(ctx, namespace, name)
		if err != nil {
			a.notef("Provider/%s: refused: dependent inventory incomplete: %v", name, err)
			return fmt.Errorf("retire refused: Provider dependent inventory incomplete: %w", err)
		}
		if len(refs) > 0 {
			a.notef("Provider/%s: refused: other resources still reference it", name)
			for _, d := range refs {
				a.notef("  %s", d)
			}
			return fmt.Errorf("retire refused: %d resource(s) still reference Provider/%s", len(refs), name)
		}
	}
	return nil
}

func (a *App) alreadyRetiredBundle(ctx context.Context, bundle, contextName, namespace, uid, name string) (bool, error) {
	raw, err := os.ReadFile(bundleRetireReceiptPath(bundle, contextName, namespace, uid))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read retirement receipt: %w", err)
	}
	var receipt bundleRetireReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.Bundle != name || receipt.Target.Context != contextName || receipt.Target.Namespace != namespace || receipt.Target.ClusterUID != uid || receipt.Target.Agent != name {
		return false, fmt.Errorf("invalid retirement receipt for this target")
	}
	if !receipt.Complete {
		return false, nil
	}
	for _, kind := range []string{"Agent", "Provider"} {
		live, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", orkaPlural(kind), name, "--ignore-not-found=true", "-o", "json")
		if err != nil {
			return false, fmt.Errorf("cannot inspect %s/%s: %w", kind, name, err)
		}
		if len(live) == 0 {
			continue
		}
		var doc struct {
			Metadata struct {
				UID         string            `json:"uid"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if json.Unmarshal(live, &doc) != nil {
			return false, fmt.Errorf("invalid %s/%s response", kind, name)
		}
		released := false
		for _, r := range receipt.Resources {
			if r.Kind == kind && r.Name == name && r.UID == doc.Metadata.UID && r.Action == "release" {
				released = true
			}
		}
		if !released || doc.Metadata.Annotations[orkaBundleMarker] != "" || doc.Metadata.Annotations[orkaPortableMarker] != "" || doc.Metadata.Annotations[orkaRenderedMarker] != "" || doc.Metadata.Annotations["kaimahi.dev/origin"] != "" {
			return false, nil
		}
	}
	return true, nil
}

func (a *App) inspectPreviouslyReleased(ctx context.Context, namespace, kind, name, uid string) (bool, error) {
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", orkaPlural(kind), name, "--ignore-not-found=true", "-o", "json")
	if err != nil || len(raw) == 0 {
		return false, err
	}
	var doc struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name        string         `json:"name"`
			Namespace   string         `json:"namespace"`
			UID         string         `json:"uid"`
			Annotations map[string]any `json:"annotations"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return false, fmt.Errorf("invalid %s/%s response", kind, name)
	}
	if doc.Kind != kind || doc.Metadata.Name != name || doc.Metadata.Namespace != namespace || doc.Metadata.UID != uid {
		return false, nil
	}
	for _, key := range []string{orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker, orkaOriginMarker} {
		if _, exists := doc.Metadata.Annotations[key]; exists {
			return false, nil
		}
	}
	return true, nil
}

func (a *App) inspectRetireObject(ctx context.Context, namespace, kind, name string, deleteAdopted bool) (retireDecision, error) {
	d := retireDecision{kind: kind, name: name, action: "absent", reason: "object does not exist"}
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", orkaPlural(kind), name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return d, fmt.Errorf("cannot inspect %s/%s: %w", kind, name, err)
	}
	if len(raw) == 0 {
		return d, nil
	}
	var obj struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name              string            `json:"name"`
			Namespace         string            `json:"namespace"`
			UID               string            `json:"uid"`
			ResourceVersion   string            `json:"resourceVersion"`
			DeletionTimestamp *string           `json:"deletionTimestamp"`
			Annotations       map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &obj) != nil || obj.Kind != kind || obj.Metadata.Name != name || obj.Metadata.Namespace != namespace || obj.Metadata.UID == "" || obj.Metadata.ResourceVersion == "" || obj.Metadata.DeletionTimestamp != nil {
		return d, fmt.Errorf("invalid or terminating %s/%s identity; refusing retirement", kind, name)
	}
	owner := obj.Metadata.Annotations[orkaBundleMarker]
	if owner != name {
		return d, fmt.Errorf("%s/%s is not owned by this bundle (bundle marker differs or is missing)", kind, name)
	}
	if !validOrkaMarkerDigest(obj.Metadata.Annotations[orkaPortableMarker]) || !validOrkaMarkerDigest(obj.Metadata.Annotations[orkaRenderedMarker]) {
		return d, fmt.Errorf("%s/%s has incomplete ownership markers", kind, name)
	}
	d.uid, d.version = obj.Metadata.UID, obj.Metadata.ResourceVersion
	d.portableDigest, d.renderedDigest = obj.Metadata.Annotations[orkaPortableMarker], obj.Metadata.Annotations[orkaRenderedMarker]
	origin, hasOrigin := obj.Metadata.Annotations[orkaOriginMarker]
	d.hasOrigin = hasOrigin
	switch origin {
	case "created":
		d.action, d.reason = "delete", "origin created"
	case "", "adopted":
		d.action = "release"
		if origin == "" {
			d.reason = "legacy object without origin marker; creation cannot be proved"
		} else {
			d.reason = "origin adopted"
		}
		if deleteAdopted {
			d.action = "delete"
			d.reason += "; explicit --delete-adopted"
		}
	default:
		return d, fmt.Errorf("%s/%s has invalid origin marker; refusing retirement", kind, name)
	}
	return d, nil
}

func (a *App) applyRetireDecision(ctx context.Context, namespace string, d retireDecision) error {
	// An immediately repeated read narrows the interval between classification
	// and mutation; resourceVersion and UID preconditions prevent stale writes.
	again, err := a.inspectRetireObject(ctx, namespace, d.kind, d.name, d.action == "delete")
	if err != nil {
		return err
	}
	if again.uid != d.uid || again.version != d.version || again.portableDigest != d.portableDigest || again.renderedDigest != d.renderedDigest || again.hasOrigin != d.hasOrigin ||
		(again.action != d.action && !(d.forceReleaseForAgent && d.action == "release" && again.action == "delete")) {
		return fmt.Errorf("%s/%s changed after retirement inspection", d.kind, d.name)
	}
	if d.action == "delete" {
		err = a.deleteRetireObject(ctx, namespace, d)
	} else {
		ops := []map[string]any{{"op": "test", "path": "/metadata/resourceVersion", "value": d.version}, {"op": "test", "path": "/metadata/uid", "value": d.uid}}
		for _, key := range []string{orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker, "kaimahi.dev/origin"} {
			if key == orkaOriginMarker && !d.hasOrigin {
				continue
			}
			ops = append(ops, map[string]any{"op": "remove", "path": "/metadata/annotations/" + orkaAnnotationPointer(key)})
		}
		raw, _ := json.Marshal(ops)
		_, err = a.orkaCapture(ctx, nil, "-n", namespace, "patch", orkaPlural(d.kind), d.name, "--type=json", "-p", string(raw))
	}
	if err != nil {
		return fmt.Errorf("%s %s/%s: %w", d.action, d.kind, d.name, err)
	}
	return nil
}

func writeBundleRetireReceipt(bundle string, receipt bundleRetireReceipt) error {
	if err := checkLiftReceiptsDir(bundle); err != nil {
		return err
	}
	dir := filepath.Join(bundle, "receipts")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateAgentFile(bundleRetireReceiptPath(bundle, receipt.Target.Context, receipt.Target.Namespace, receipt.Target.ClusterUID), append(raw, '\n'))
}
