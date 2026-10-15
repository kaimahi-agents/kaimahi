package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// SuiteCandidateCleanupOptions grants no execution, placement or export authority.
// Empty RunID lists only; deletion accepts exactly one full lowercase run ID.
type SuiteCandidateCleanupOptions struct{ Namespace, RunID string }

type suiteCleanupRoot struct {
	resource                  suiteCandidateResource
	owner                     *orkaSuiteOwnership
	physical, resourceVersion string
	terminating               bool
}
type suiteCleanupInventory struct {
	roots     []suiteCleanupRoot
	rows      map[string][]suiteCandidateCleanupMetadataRow
	uncertain string // One bounded validated Kind/name locator; never API payload.
}

func suiteCleanupOwner(markers map[string]any) *orkaSuiteOwnership {
	values := make([]string, 0, 7)
	for _, key := range orkaSuiteReservedMarkers {
		value, ok := markers[key].(string)
		if !ok {
			return nil
		}
		values = append(values, value)
	}
	owner := &orkaSuiteOwnership{values[0], values[1], values[2], values[3], values[4], values[5], values[6]}
	if !owner.valid() {
		return nil
	}
	return owner
}

// The six bounded inventories are observations, never CREATE acknowledgements.
func (a *App) discoverSuiteCandidates(ctx context.Context, target suiteNativeTarget, retained ...[]suiteCandidateResource) (suiteCleanupInventory, error) {
	inventory := suiteCleanupInventory{rows: map[string][]suiteCandidateCleanupMetadataRow{}}
	count := 0
	owners := map[string]*orkaSuiteOwnership{}
	for _, kind := range []string{"Provider", "Agent", "Task", "Job", "ConfigMap", "Pod"} {
		rows, err := a.suiteCandidateCleanupInventoryTarget(ctx, target, kind)
		if err != nil {
			return inventory, errSuiteCandidateCleanup
		}
		count += len(rows)
		if count > suiteCandidateCleanupObjects {
			return inventory, errSuiteCandidateCleanup
		}
		inventory.rows[kind] = rows
		if kind != "Provider" && kind != "Agent" && kind != "Task" {
			continue
		}
		for _, row := range rows {
			if _, ok := row.Markers[suiteCandidateRunMarker]; !ok && strings.HasPrefix(row.Name, "kmx-eval-") {
				inventory.uncertain = kind + "/" + row.Name
			}
			if len(row.Markers) == 0 {
				continue
			}
			// Canonical owned resources aren't candidate leftovers.
			if _, candidate := row.Markers[suiteCandidateRunMarker]; !candidate {
				continue
			}
			owner := suiteCleanupOwner(row.Markers)
			if owner == nil || row.Legacy || row.Generation < 1 {
				inventory.uncertain = kind + "/" + row.Name
				continue
			}
			physical, err := suiteCandidatePhysicalName(owner.Digest, owner.EvaluationRun)
			if err != nil || kind != "Task" && row.Name != physical || kind == "Task" && (row.AgentName != physical || row.AgentNamespace != target.Namespace || row.TaskType != "ai") {
				inventory.uncertain = kind + "/" + row.Name
				continue
			}
			if old := owners[owner.EvaluationRun]; old != nil && !reflect.DeepEqual(old, owner) {
				inventory.uncertain = kind + "/" + row.Name
			}
			owners[owner.EvaluationRun] = owner
			terminating := row.DeletionTimestamp != nil
			if terminating {
				if _, err := time.Parse(time.RFC3339Nano, *row.DeletionTimestamp); err != nil {
					inventory.uncertain = kind + "/" + row.Name
					continue
				}
			}
			inventory.roots = append(inventory.roots, suiteCleanupRoot{suiteCandidateResource{Kind: kind, Name: row.Name, UID: row.UID, Generation: row.Generation, SpecDigest: row.SpecDigest}, owner, physical, row.ResourceVersion, terminating})
		}
	}
	// Orka descendants do NOT inherit the full run annotation. A typed owner
	// link without its live anchor cannot establish full-run attribution.
	for _, kind := range []string{"Job", "ConfigMap", "Pod"} {
		parentKind, version := "Task", "core.orka.ai/v1alpha1"
		if kind == "Pod" {
			parentKind, version = "Job", "batch/v1"
		}
		for _, row := range inventory.rows[kind] {
			anchored, potential := false, row.TaskLabel != ""
			for _, owner := range row.Owners {
				if owner.Kind != parentKind || owner.APIVersion != version {
					continue
				}
				potential = true
				for _, known := range retained {
					for _, parent := range known {
						if parent.Kind == parentKind && owner.UID == parent.UID && owner.Name == parent.Name && owner.Controller && len(row.Owners) == 1 {
							anchored = true
						}
					}
				}
				for _, parent := range inventory.rows[parentKind] {
					if owner.UID == parent.UID && owner.Name == parent.Name && owner.Controller && len(row.Owners) == 1 {
						anchored = true
					}
				}
			}
			if potential && !anchored {
				inventory.uncertain = kind + "/" + row.Name
			}
		}
	}
	sort.Slice(inventory.roots, func(i, j int) bool {
		a, b := inventory.roots[i], inventory.roots[j]
		return a.resource.Kind+"/"+a.resource.Name < b.resource.Kind+"/"+b.resource.Name
	})
	return inventory, nil
}

func reportSuiteCandidates(out io.Writer, target suiteNativeTarget, inventory suiteCleanupInventory) error {
	if _, err := fmt.Fprintf(out, "Candidate discovery: context=%q namespace=%q; read-only, no liveness proof\n", target.Context, target.Namespace); err != nil {
		return err
	}
	for _, root := range inventory.roots {
		if _, err := fmt.Fprintf(out, "  run=%s %s/%s uid=%s generation=%d spec=%s rv=%s\n", root.owner.EvaluationRun, root.resource.Kind, root.resource.Name, root.resource.UID, root.resource.Generation, root.resource.SpecDigest, root.resourceVersion); err != nil {
			return err
		}
	}
	if inventory.uncertain != "" {
		_, err := fmt.Fprintf(out, "WARNING: uncertain candidate inventory at %s; ownership or descendant closure cannot be verified.\n", inventory.uncertain)
		return err
	}
	return nil
}

func (a *App) candidateCleanupTarget(ctx context.Context, namespace string) (suiteNativeTarget, error) {
	target := suiteNativeTarget{Context: a.Cfg.KubeContext, Namespace: namespace}
	if target.Context == "" || scaffold.ValidateNamespace(namespace) != nil {
		return target, errSuiteCandidateCleanup
	}
	uid, terminating, err := a.readLiftClusterUID(ctx)
	if err != nil || terminating || !verificationIdentity(uid) {
		return target, errSuiteCandidateCleanup
	}
	target.ClusterUID = uid
	raw, err := a.orkaCapture(ctx, nil, "get", "namespace", namespace, "--ignore-not-found=true", "-o", "json")
	var ns struct {
		Kind     string
		Metadata struct {
			Name, UID         string
			DeletionTimestamp *string
		}
	}
	if err != nil || json.Unmarshal(raw, &ns) != nil || ns.Kind != "Namespace" || ns.Metadata.Name != namespace || !verificationIdentity(ns.Metadata.UID) || ns.Metadata.DeletionTimestamp != nil {
		return target, errSuiteCandidateCleanup
	}
	target.NamespaceUID = ns.Metadata.UID
	return target, nil
}

// CleanupSuiteCandidates lists or confirms a frozen, deletion-only UID snapshot.
// No source, inference, Secret, runtime readiness or local persistence is used.
func (a *App) CleanupSuiteCandidates(ctx context.Context, opt SuiteCandidateCleanupOptions) error {
	if a == nil || a.Cfg == nil || a.Run == nil || opt.RunID != "" && !evaluationRunID(opt.RunID) {
		return errSuiteCandidateCleanup
	}
	if opt.Namespace == "" {
		opt.Namespace = OrkaNamespace
	}
	worker := *a
	runner := *a.Run
	worker.Run = &runner
	cfg := *a.Cfg
	worker.Cfg, worker.guarded = &cfg, false
	ctx, cancel := context.WithTimeout(ctx, suiteCandidateCleanupBudget)
	defer cancel()
	worker.Run.Context = ctx
	target, err := worker.candidateCleanupTarget(ctx, opt.Namespace)
	if err != nil {
		return errSuiteCandidateCleanup
	}
	inventory, err := worker.discoverSuiteCandidates(ctx, target)
	if err != nil {
		return fmt.Errorf("%w: candidate inventory incomplete or unreadable", errSuiteCandidateCleanup)
	}
	reportOut := worker.Err
	if opt.RunID == "" {
		reportOut = worker.Out
	}
	if err := reportSuiteCandidates(reportOut, target, inventory); err != nil {
		return err
	}
	if opt.RunID == "" {
		return nil
	}
	if inventory.uncertain != "" {
		return fmt.Errorf("%w: uncertain candidate inventory at %s; inspect with eval cleanup before selecting a run", errSuiteCandidateCleanup, inventory.uncertain)
	}
	selected := map[string]suiteCleanupRoot{}
	roots := []suiteCandidateResource{}
	for _, root := range inventory.roots {
		if root.owner.EvaluationRun == opt.RunID {
			selected[root.resource.Kind+"/"+root.resource.Name] = root
			roots = append(roots, root.resource)
		}
	}
	// No selected roots is not a proof that a crashed run's descendants are gone.
	if len(roots) == 0 {
		return fmt.Errorf("%w: no roots for selected run; use eval cleanup to list observable runs, not proof of orphan absence", errSuiteCandidateCleanup)
	}
	// Freeze child UID chains before consent; replacements during confirmation
	// must fail the engine's pre-effect closure check, not become initial proof.
	known := append([]suiteCandidateResource{}, roots...)
	if _, err := worker.suiteCandidateCleanupChildrenTarget(ctx, target, &known, nil); err != nil {
		return fmt.Errorf("%w: pre-confirmation child closure unreadable or changed", errSuiteCandidateCleanup)
	}
	if _, err := fmt.Fprintf(worker.Err, "Cleanup preview: run=%s context=%q clusterUID=%s namespace=%q namespaceUID=%s roots=%d\nWARNING: this may interrupt an active run; liveness is unknown. Only these root UIDs are authorized; descendants are GC-only.\n", opt.RunID, target.Context, target.ClusterUID, target.Namespace, target.NamespaceUID, len(roots)); err != nil {
		return err
	}
	raw, err := worker.orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return errSuiteCandidateCleanup
	}
	kube, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return errSuiteCandidateCleanup
	}
	command := worker.InvocationCommand
	if command == "" {
		command = "kmx eval cleanup " + opt.RunID + " --to-context " + shellArg(target.Context) + " --to-namespace " + shellArg(target.Namespace)
	}
	if err := guard.CheckContext(ctx, kube, guard.Request{Action: "delete the displayed candidate run root UIDs and verify child closure", Context: target.Context, Source: cfg.ContextSource, Namespaces: target.Namespace, Confirm: cfg.Confirm, Command: command, MustBeKnown: true, RequireConfirmation: true}, worker.Err, worker.Stdin); err != nil {
		return err
	}
	worker.guarded = true
	retained := known
	checkInventory := func(ctx context.Context) error {
		fresh, err := worker.discoverSuiteCandidates(ctx, target, retained)
		if err != nil || fresh.uncertain != "" {
			return errSuiteCandidateCleanup
		}
		for _, root := range fresh.roots {
			if root.owner.EvaluationRun != opt.RunID {
				continue
			}
			old, ok := selected[root.resource.Kind+"/"+root.resource.Name]
			comparable := root.resource
			if root.terminating && comparable.Generation == old.resource.Generation+1 {
				comparable.Generation = old.resource.Generation
			}
			if !ok || !reflect.DeepEqual(old.resource, comparable) || !reflect.DeepEqual(old.owner, root.owner) {
				return errSuiteCandidateCleanup
			}
		}
		return nil
	}
	// Retain Task/Job UID chains before any root disappears; never fabricate ACKs.
	authority := suiteCandidateCleanupAuthority{target: target, roots: roots, known: known, inventoryCheck: checkInventory,
		readRoot: func(ctx context.Context, r suiteCandidateResource) (map[string]any, bool, bool, error) {
			old := selected[r.Kind+"/"+r.Name]
			return worker.suiteCandidateCleanupRootIdentity(ctx, target, old.owner, old.physical, r)
		},
		children: func(ctx context.Context, known *[]suiteCandidateResource) (bool, error) {
			retained = *known
			if err := checkInventory(ctx); err != nil {
				return false, err
			}
			gone, err := worker.suiteCandidateCleanupChildrenTarget(ctx, target, known, nil)
			retained = *known
			return gone, err
		},
	}
	result, err := worker.cleanupSuiteCandidateAuthority(ctx, authority)
	if err != nil || !result.AllGone {
		return fmt.Errorf("%w: %s; inspect the selected run before another cleanup", errSuiteCandidateCleanup, result.Reason)
	}
	_, err = fmt.Fprintln(worker.Err, "Candidate cleanup: verified selected roots and attributable children absent; runtime/model logs are not erased.")
	return err
}

func (a *App) reportCandidateStartup(ctx context.Context, target suiteNativeTarget) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	worker := a.withRunContext(ctx)
	inventory, err := worker.discoverSuiteCandidates(ctx, target)
	if err != nil {
		fmt.Fprintln(a.Err, "WARNING: candidate discovery incomplete or unknown; continuing a fresh evaluation without deleting leftovers.")
		return
	}
	reportSuiteCandidates(a.Err, target, inventory)
	if len(inventory.roots) > 0 {
		fmt.Fprintln(a.Err, "WARNING: candidate leftovers may belong to active runs; continuing fresh evaluation, no startup deletion.")
	}
}
