// `kmx agent status` — a read-only report of one portable bundle's
// deployment state across every destination it is known about. It writes
// nothing: no receipt, no remembered selection, no cluster resource.
//
// Targets come from the bundle's own receipts (one per destination this
// bundle was ever lifted to), the remembered last-selected target, or an
// explicit --to-context. An explicit --to-context that names no known receipt is
// still read live; the report shows the cluster identity it actually finds
// and says plainly that there is no local receipt for it.
//
// A receipt's filename is sha256(context + "\x00" + namespace + "\x00" +
// clusterUID) (see writeLiftReceipt); it never stores that UID in its body.
// Before reading any object, a recorded target's live cluster identity is
// recomputed into that same filename and checked for existence — the only
// way to recover the UID a receipt was written for. A context that now
// resolves to a different cluster is target changed, not silently compared
// against the wrong destination.
//
// Every cluster read goes through the same sanitized orkaCapture lift and
// reconcile already use: raw kubectl stderr never reaches a report. A single
// target that cannot be read — unreachable cluster, missing ownership
// marker, foreign bundle — is reported as that target's State, never as a
// command failure: this command exits zero even when every target is
// unknown. Only a bundle it cannot read at all (missing or invalid
// agent.yaml) is an error.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// BundleStatusOptions selects a bundle and, optionally, one destination
// independent of any recorded history.
type BundleStatusOptions struct {
	BundleDir, Context, Namespace, Output string
}

const (
	bundleStateNotDeployed = "not deployed"
	bundleStateInSync      = "in sync"
	bundleStateBehind      = "behind"
	bundleStateDrifted     = "drifted"
	bundleStateForeign     = "belongs to another bundle"
	bundleStateChanged     = "target changed"
	bundleStateUnknown     = "unknown"
)

// bundleStatusTarget is one destination this report observes.
type bundleStatusTarget struct {
	Context, Namespace string
	// Recorded is true when this context+namespace has any local history at
	// all — a receipt or the remembered selection.
	Recorded bool
	// HasReceipt is true when at least one receipt file exists for this
	// exact context+namespace, which is what makes the cluster-identity
	// check against its filename possible.
	HasReceipt bool
	// ReceiptFile distinguishes physical clusters that reused a context name.
	ReceiptFile string
	// ClusterUID is the remembered selection's own recorded cluster identity.
	// It is set only when this target came from the selection fallback with
	// no covering receipt: a receipt-covered target is checked against its
	// own filename instead, which is the stronger, write-time proof.
	ClusterUID string
}

// bundleResourceStatus is what status reads directly off one live object,
// independent of whether it turns out to belong to this bundle. PortableDigest
// and RenderedDigest are that object's own ownership-marker annotations,
// exposed for both Provider and Agent so a caller can see whether the two
// objects agree, not only whether the Agent does.
type bundleResourceStatus struct {
	Found          bool   `json:"found"`
	UID            string `json:"uid,omitempty"`
	Generation     int64  `json:"generation,omitempty"`
	Ready          bool   `json:"ready"`
	Marked         bool   `json:"marked"`
	MarkerOwner    string `json:"markerOwner,omitempty"`
	PortableDigest string `json:"portableDigest,omitempty"`
	RenderedDigest string `json:"renderedDigest,omitempty"`
}

// bundleTargetStatus is one target's reported state. Exactly one State value
// summarizes the whole target; ChangedFields and Behind are populated only
// for their matching State.
type bundleTargetStatus struct {
	Context            string `json:"context"`
	Namespace          string `json:"namespace"`
	Recorded           bool   `json:"recorded"`
	NoReceipt          bool   `json:"noReceipt,omitempty"`
	ReceiptID          string `json:"receiptID,omitempty"`
	ObservedClusterUID string `json:"observedClusterUID,omitempty"`
	State              string `json:"state"`
	Detail             string `json:"detail,omitempty"`
	Behind             int    `json:"behind,omitempty"`
	DeployedCommit     string `json:"deployedCommit,omitempty"`
	// Internal identity for the console diff; never part of status JSON.
	deployedCommitFull string
	GitNote            string   `json:"gitNote,omitempty"`
	LiveDigest         string   `json:"liveDigest,omitempty"`
	ChangedFields      []string `json:"changedFields,omitempty"`
	// Evaluation is the recorded evaluate result for the deployed revision
	// when it is the bundle's current digest and case set: pass, fail or
	// unknown; otherwise none.
	Evaluation string               `json:"evaluation"`
	Gate       string               `json:"gate"`
	Provider   bundleResourceStatus `json:"provider"`
	Agent      bundleResourceStatus `json:"agent"`
}

type bundleStatusReport struct {
	Bundle         string               `json:"bundle"`
	PortableDigest string               `json:"portableDigest"`
	GitCommit      string               `json:"gitCommit"`
	Targets        []bundleTargetStatus `json:"targets"`
}

// BundleStatus prints one bundle's deployment state and never writes
// anything: no receipt, no remembered selection, no cluster resource.
func (a *App) BundleStatus(opt BundleStatusOptions) error {
	format, err := bundleStatusFormat(opt.Output)
	if err != nil {
		return err
	}
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		return err
	}
	if format == "json" {
		return a.printBundleStatusJSON(report)
	}
	a.printBundleStatusTable(report)
	return nil
}

func bundleStatusFormat(output string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(output))
	if format == "" {
		format = "table"
	}
	if format != "table" && format != "json" {
		return "", fmt.Errorf("agent status output %q is not supported — use table or json", output)
	}
	return format, nil
}

// bundleStatusReport builds the full report. Only a bundle it cannot read at
// all returns an error; every per-target observation failure is folded into
// that target's own State instead.
func (a *App) bundleStatusReport(opt BundleStatusOptions) (bundleStatusReport, error) {
	if strings.TrimSpace(opt.Namespace) != "" && strings.TrimSpace(opt.Context) == "" {
		return bundleStatusReport{}, fmt.Errorf("--to-namespace requires --to-context")
	}
	ctx := a.operationContext()
	if a.Cfg == nil || a.Run == nil {
		return bundleStatusReport{}, fmt.Errorf("status requires configured kubectl and streams")
	}
	bundle, err := resolveOrkaPath(opt.BundleDir)
	if err != nil {
		return bundleStatusReport{}, fmt.Errorf("resolve bundle: %w", err)
	}
	name, _, portableDigest, err := readBundlePortableAgent(bundle)
	if err != nil {
		return bundleStatusReport{}, err
	}
	gitCommit := liftAgentCommit(ctx, bundle, portableDigest)

	targets, err := bundleStatusTargets(bundle, opt)
	if err != nil {
		return bundleStatusReport{}, err
	}

	report := bundleStatusReport{Bundle: name, PortableDigest: portableDigest, GitCommit: gitCommit, Targets: []bundleTargetStatus{}}
	casesDigest := currentBundleCasesDigest(bundle)
	for _, target := range targets {
		observed := a.observeBundleTarget(ctx, bundle, name, portableDigest, target)
		observed.Evaluation = bundleEvaluationStatus(bundle, observed, portableDigest, casesDigest)
		if observed.ObservedClusterUID == "" {
			observed.Gate = "unknown: destination cluster UID unavailable"
		} else {
			required, failed := evaluateBundleLiftGate(bundle, name, portableDigest, bundleGateTarget{ClusterUID: observed.ObservedClusterUID, Namespace: target.Namespace}, "")
			switch {
			case failed != "":
				observed.Gate = "refused: " + failed
			case required:
				observed.Gate = "pass"
			default:
				observed.Gate = "not required"
			}
		}
		report.Targets = append(report.Targets, observed)
	}
	return report, nil
}

// bundleStatusTargets resolves which destinations to observe: an explicit
// --to-context names one cluster, matched against known receipts and the
// remembered selection to attach recorded history. Otherwise every
// known receipt is observed, plus the remembered selection if it names a
// target no receipt already covers.
func bundleStatusTargets(bundle string, opt BundleStatusOptions) ([]bundleStatusTarget, error) {
	recorded, err := loadBundleReceiptTargets(bundle)
	if err != nil {
		return nil, err
	}
	selectionPath, err := bundleLiftSelectionPath(bundle)
	if err != nil {
		return nil, err
	}
	selection, err := loadBundleLiftSelection(selectionPath)
	if err != nil {
		return nil, err
	}

	if explicit := strings.TrimSpace(opt.Context); explicit != "" {
		namespace := strings.TrimSpace(opt.Namespace)
		if namespace == "" {
			var matches []bundleStatusTarget
			for _, target := range recorded {
				if target.Context == explicit {
					matches = append(matches, target)
				}
			}
			if selection.Context == explicit && !selectionHasReceipt(bundle, matches, selection) {
				matches = append(matches, bundleStatusTarget{Context: explicit, Namespace: selection.Namespace, Recorded: true, ClusterUID: selection.ClusterUID})
			}
			if len(matches) > 0 {
				sortBundleStatusTargets(matches)
				return matches, nil
			}
			namespace = OrkaNamespace
		}
		var matches []bundleStatusTarget
		for _, target := range recorded {
			if target.Context == explicit && target.Namespace == namespace {
				matches = append(matches, target)
			}
		}
		if selection.Context == explicit && selection.Namespace == namespace && !selectionHasReceipt(bundle, matches, selection) {
			matches = append(matches, bundleStatusTarget{Context: explicit, Namespace: namespace, Recorded: true, ClusterUID: selection.ClusterUID})
		}
		if len(matches) > 0 {
			return matches, nil
		}
		// No local history names this destination: still read it live, but
		// the report shows the cluster it actually finds and says plainly
		// there is no receipt for it.
		return []bundleStatusTarget{{Context: explicit, Namespace: namespace}}, nil
	}

	if len(recorded) > 0 {
		if selection.Context != "" && !selectionHasReceipt(bundle, recorded, selection) {
			recorded = append(recorded, bundleStatusTarget{Context: selection.Context, Namespace: selection.Namespace, Recorded: true, ClusterUID: selection.ClusterUID})
		}
		sortBundleStatusTargets(recorded)
		return recorded, nil
	}
	if selection.Context != "" {
		return []bundleStatusTarget{{Context: selection.Context, Namespace: selection.Namespace, Recorded: true, ClusterUID: selection.ClusterUID}}, nil
	}
	return nil, nil
}

func selectionHasReceipt(bundle string, targets []bundleStatusTarget, selection bundleLiftSelection) bool {
	wanted := filepath.Base(bundleReceiptPath(bundle, selection.Context, selection.Namespace, selection.ClusterUID))
	for _, target := range targets {
		if target.Context == selection.Context && target.Namespace == selection.Namespace && target.ReceiptFile == wanted {
			return true
		}
	}
	return false
}

func sortBundleStatusTargets(targets []bundleStatusTarget) {
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Context != targets[j].Context {
			return targets[i].Context < targets[j].Context
		}
		if targets[i].Namespace != targets[j].Namespace {
			return targets[i].Namespace < targets[j].Namespace
		}
		return targets[i].ReceiptFile < targets[j].ReceiptFile
	})
}

// loadBundleReceiptTargets reads every receipt this bundle has written. A
// receipt that cannot be parsed is skipped rather than failing the whole
// report: local history being partly unreadable is not a reason to refuse
// reporting on the rest of it.
func loadBundleReceiptTargets(bundle string) ([]bundleStatusTarget, error) {
	dir := filepath.Join(bundle, "receipts")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read bundle receipts: %w", err)
	}
	var targets []bundleStatusTarget
	for _, entry := range entries {
		// Evaluation receipts share the directory but record no deployment.
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasPrefix(entry.Name(), "eval-") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var wrapped bundleLiftReceipt
		if json.Unmarshal(raw, &wrapped) != nil {
			continue
		}
		target := wrapped.Receipt.Target
		if target.Context == "" || target.Namespace == "" {
			continue
		}
		targets = append(targets, bundleStatusTarget{Context: target.Context, Namespace: target.Namespace, Recorded: true, HasReceipt: true, ReceiptFile: entry.Name()})
	}
	return targets, nil
}

// bundleReceiptPath recomputes the exact filename writeLiftReceipt would use
// for this context, namespace and cluster UID. Its existence is the only
// local record of which UID a destination had when this bundle was last
// lifted there: the receipt body never stores it.
func bundleReceiptPath(bundle, context, namespace, clusterUID string) string {
	key := sha256.Sum256([]byte(context + "\x00" + namespace + "\x00" + clusterUID))
	return filepath.Join(bundle, "receipts", fmt.Sprintf("%x.json", key))
}

// readBundleReceiptAt reads and parses one receipt file, also proving its
// contents actually name the context and namespace its path implies: the
// path alone (a hash) cannot be misread as proof by itself, only as a
// candidate to open and check.
func readBundleReceiptAt(path string) (bundleLiftReceipt, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return bundleLiftReceipt{}, false
	}
	var receipt bundleLiftReceipt
	if json.Unmarshal(raw, &receipt) != nil {
		return bundleLiftReceipt{}, false
	}
	if receipt.Receipt.Target.Context == "" || receipt.Receipt.Target.Namespace == "" {
		return bundleLiftReceipt{}, false
	}
	return receipt, true
}

// observeBundleTarget reads one destination's live state and compares it to
// what agent.yaml renders today. Every failure here becomes this target's
// State, never a returned error.
func (a *App) observeBundleTarget(ctx context.Context, bundle, name, portableDigest string, target bundleStatusTarget) bundleTargetStatus {
	result := bundleTargetStatus{Context: target.Context, Namespace: target.Namespace, Recorded: target.Recorded, NoReceipt: !target.HasReceipt}
	if target.HasReceipt {
		result.ReceiptID = strings.TrimSuffix(target.ReceiptFile, ".json")
	}

	worker := *a
	cfg := *a.Cfg
	cfg.KubeContext, cfg.ContextSource = target.Context, config.SourceFlag
	worker.Cfg = &cfg

	uid, err := worker.liftClusterUID(ctx)
	if err != nil {
		result.State = bundleStateUnknown
		result.Detail = "cannot confirm destination cluster identity"
		return result
	}
	result.ObservedClusterUID = uid

	// A receipt-covered target proves identity from its own filename (the
	// stronger, write-time proof); a target that only has the remembered
	// selection is checked against its own recorded UID instead, before any
	// object is read either way.
	if target.ClusterUID != "" && target.ClusterUID != uid {
		result.State = bundleStateChanged
		result.Detail = fmt.Sprintf("context %s no longer identifies the cluster this bundle's remembered selection for namespace %s was written for", target.Context, target.Namespace)
		return result
	}
	if target.HasReceipt {
		path := bundleReceiptPath(bundle, target.Context, target.Namespace, uid)
		if filepath.Base(path) != target.ReceiptFile {
			result.State = bundleStateChanged
			result.Detail = fmt.Sprintf("context %s no longer identifies the cluster this bundle's receipt for namespace %s was written for", target.Context, target.Namespace)
			return result
		}
		receipt, ok := readBundleReceiptAt(path)
		if !ok || receipt.Receipt.Target.Context != target.Context || receipt.Receipt.Target.Namespace != target.Namespace {
			result.State = bundleStateChanged
			result.Detail = fmt.Sprintf("context %s no longer identifies the cluster this bundle's receipt for namespace %s was written for", target.Context, target.Namespace)
			return result
		}
	}

	providerRaw, err := worker.orkaCapture(ctx, nil, "-n", target.Namespace, "get", "providers.core.orka.ai", name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		result.State = bundleStateUnknown
		result.Detail = "cannot reach destination cluster"
		return result
	}
	agentRaw, err := worker.orkaCapture(ctx, nil, "-n", target.Namespace, "get", "agents.core.orka.ai", name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		result.State = bundleStateUnknown
		result.Detail = "cannot reach destination cluster"
		return result
	}
	result.Provider = describeOrkaResource(providerRaw)
	result.Agent = describeOrkaResource(agentRaw)

	if !result.Provider.Found && !result.Agent.Found {
		result.State = bundleStateNotDeployed
		return result
	}
	if (result.Provider.Found && !result.Provider.Marked) || (result.Agent.Found && !result.Agent.Marked) {
		result.State = bundleStateUnknown
		result.Detail = "missing ownership marker"
		return result
	}
	if (result.Provider.Found && result.Provider.MarkerOwner != name) || (result.Agent.Found && result.Agent.MarkerOwner != name) {
		result.State = bundleStateForeign
		return result
	}
	if !result.Provider.Found || !result.Agent.Found {
		result.State = bundleStateUnknown
		result.Detail = "partial deployment: only one of Provider or Agent exists"
		return result
	}
	for _, resource := range []bundleResourceStatus{result.Provider, result.Agent} {
		if !validOrkaMarkerDigest(resource.PortableDigest) || !validOrkaMarkerDigest(resource.RenderedDigest) {
			result.State = bundleStateUnknown
			result.Detail = "missing ownership marker"
			return result
		}
	}
	if result.Provider.PortableDigest != result.Agent.PortableDigest || result.Provider.RenderedDigest != result.Agent.RenderedDigest {
		result.State = bundleStateUnknown
		result.Detail = "Provider and Agent ownership markers disagree"
		return result
	}

	liveDigest := result.Agent.PortableDigest
	result.LiveDigest = liveDigest
	// The deployed commit and how far behind it is are reported regardless of
	// whether the digest still matches today's agent.yaml: an operator gets
	// to see exactly which revision is live even when it is in sync, and a
	// working copy that is itself uncommitted or rolled back to an earlier
	// commit must not silently skip the search just because the digests
	// happen to agree.
	if commit, found := bundleFindDeployedCommit(ctx, bundle, liveDigest); found {
		result.DeployedCommit = shortSHA(commit)
		result.deployedCommitFull = commit
		if behind, err := commitsBehindCommit(ctx, bundle, commit); err == nil {
			result.Behind = behind
		}
	} else {
		result.GitNote = "deployed revision not found in Git"
	}

	if liveDigest == portableDigest {
		drifted, changed, err := worker.bundleFieldDrift(ctx, bundle, target.Namespace, providerRaw)
		if err != nil {
			result.State = bundleStateUnknown
			result.Detail = err.Error()
			return result
		}
		if drifted {
			result.State = bundleStateDrifted
			result.ChangedFields = changed
			return result
		}
		result.State = bundleStateInSync
		return result
	}

	// The deployed digest never matches what agent.yaml renders today once
	// any newer revision exists, so a mismatch is always behind — the only
	// question is whether that revision could still be located.
	result.State = bundleStateBehind
	if result.DeployedCommit == "" {
		result.Detail = result.GitNote
	}
	return result
}

// describeOrkaResource reads UID, generation, readiness and the ownership
// marker directly off one live object's raw JSON, independent of whether it
// turns out to belong to this bundle. A missing or non-string marker value
// is reported unmarked, never guessed at.
func describeOrkaResource(raw []byte) bundleResourceStatus {
	var result bundleResourceStatus
	if len(raw) == 0 {
		return result
	}
	result.Found = true
	var doc struct {
		Metadata struct {
			UID         string         `json:"uid"`
			Generation  int64          `json:"generation"`
			Annotations map[string]any `json:"annotations"`
		} `json:"metadata"`
		Status struct {
			Ready      bool              `json:"ready"`
			Conditions []serverCondition `json:"conditions"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return result
	}
	result.UID = doc.Metadata.UID
	result.Generation = doc.Metadata.Generation
	if owner, ok := doc.Metadata.Annotations[orkaBundleMarker].(string); ok && owner != "" {
		result.Marked = true
		result.MarkerOwner = owner
	}
	if digest, ok := doc.Metadata.Annotations[orkaPortableMarker].(string); ok {
		result.PortableDigest = digest
	}
	if digest, ok := doc.Metadata.Annotations[orkaRenderedMarker].(string); ok {
		result.RenderedDigest = digest
	}
	if doc.Status.Ready && doc.Metadata.Generation > 0 {
		for _, condition := range doc.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" && condition.ObservedGeneration == doc.Metadata.Generation {
				result.Ready = true
			}
		}
	}
	return result
}

// bundleFieldDrift renders agent.yaml against the destination's own live
// Provider bindings and reuses planOrkaReconcile — the exact same read-only
// comparison `kmx agent lift --plan` uses — to decide whether the live
// objects still match. It is called only once ownership is already
// confirmed, so a refusal here means the object changed between reads, not a
// marker problem.
func (a *App) bundleFieldDrift(ctx context.Context, bundle, namespace string, providerRaw []byte) (bool, []string, error) {
	var provider orkaProviderSpec
	if json.Unmarshal(providerRaw, &provider) != nil {
		return false, nil, fmt.Errorf("destination returned invalid Provider data")
	}
	if provider.Spec.SecretRef.Key == "" {
		return false, nil, fmt.Errorf("destination Provider has no explicit Secret reference key")
	}
	bindings := agentruntime.OrkaBindings{Namespace: namespace, Provider: agentruntime.OrkaProviderBindings{
		Type: provider.Spec.Type, BaseURL: provider.Spec.BaseURL,
		Azure:     agentruntime.OrkaAzureBindings{DeploymentName: provider.Spec.Azure.DeploymentName, APIVersion: provider.Spec.Azure.APIVersion},
		SecretRef: agentruntime.OrkaSecretRefBindings{Name: provider.Spec.SecretRef.Name, Key: provider.Spec.SecretRef.Key},
	}}
	rendered, err := RenderOrkaBundleFile(bundle, bindings)
	if err == nil && provider.Spec.Type == "azure-openai" && provider.Spec.Azure.APIVersion != "" {
		// CRD defaulting fills an omitted apiVersion on the live Provider. The
		// ownership digest identifies whether the authored binding stated it.
		var live struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(providerRaw, &live); err != nil {
			return false, nil, fmt.Errorf("destination returned invalid Provider metadata")
		}
		if live.Metadata.Annotations[orkaRenderedMarker] != rendered.RenderedDigest() {
			bindings.Provider.Azure.APIVersion = ""
			withoutVersion, candidateErr := RenderOrkaBundleFile(bundle, bindings)
			if candidateErr == nil && live.Metadata.Annotations[orkaRenderedMarker] == withoutVersion.RenderedDigest() {
				rendered = withoutVersion
			}
		}
	}
	if err != nil {
		return false, nil, fmt.Errorf("cannot render portable agent against destination bindings")
	}
	renderedBundle, err := orkaBundleFromRendered(rendered)
	if err != nil {
		return false, nil, fmt.Errorf("cannot decode rendered bundle")
	}
	checks, err := a.planOrkaReconcile(ctx, rendered, renderedBundle, namespace)
	if err != nil {
		return false, nil, err
	}
	var changed []string
	drifted := false
	for _, check := range checks {
		if check.markerRefresh {
			return false, nil, fmt.Errorf("live ownership digests do not match the rendered revision")
		}
		if check.outcome == agentruntime.ResourceUpdated {
			drifted = true
			for _, field := range orkaChangedFields(check.existing, check.candidate) {
				changed = append(changed, check.id.Kind+"."+field)
			}
		}
	}
	return drifted, dedupSortedStrings(changed), nil
}

func dedupSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

// bundleGitHistoryLimit bounds how far back a target's live digest is
// searched for among agent.yaml's own revisions. A digest older than this
// many commits, or from history that was rewritten, is reported not found
// rather than searched for indefinitely.
const bundleGitHistoryLimit = 200

// bundleFindDeployedCommit searches the most recent bundleGitHistoryLimit
// commits touching agent.yaml for one whose content matches the live
// digest, returning its full SHA. found is false when no match exists
// inside that bounded window, including when the deployed revision was only
// ever staged or edited locally and never committed.
//
// The bundle path is resolved first, as bundleLiftSelectionPath does. Git
// reports the repository root with symlinks resolved, so a bundle reached
// through a symlink (macOS's /var is one) would otherwise look like a path
// outside the repository and never match.
func bundleFindDeployedCommit(ctx context.Context, bundle, liveDigest string) (sha string, found bool) {
	if liveDigest == "" {
		return "", false
	}
	bundle, err := resolveOrkaPath(bundle)
	if err != nil {
		return "", false
	}
	gitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rootRaw, err := exec.CommandContext(gitCtx, "git", "-C", bundle, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(rootRaw))
	rel, err := filepath.Rel(root, filepath.Join(bundle, "agent.yaml"))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	out, err := exec.CommandContext(gitCtx, "git", "-C", root, "log", "-n", strconv.Itoa(bundleGitHistoryLimit), "--format=%H", "--", rel).Output()
	if err != nil {
		return "", false
	}
	for _, commit := range strings.Fields(string(out)) {
		blob, err := exec.CommandContext(gitCtx, "git", "-C", root, "show", commit+":"+filepath.ToSlash(rel)).Output()
		if err != nil {
			continue
		}
		if agentruntime.PortableBundleDigest(blob) == liveDigest {
			return commit, true
		}
	}
	return "", false
}

// commitsBehindCommit counts every commit from the deployed commit to HEAD,
// regardless of whether it touched agent.yaml: a target is behind by however
// far HEAD has moved, not only by agent.yaml's own revisions.
func commitsBehindCommit(ctx context.Context, bundle, sha string) (int, error) {
	gitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rootRaw, err := exec.CommandContext(gitCtx, "git", "-C", bundle, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return 0, err
	}
	root := strings.TrimSpace(string(rootRaw))
	out, err := exec.CommandContext(gitCtx, "git", "-C", root, "rev-list", "--count", sha+"..HEAD").Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// shortSHA reports a commit the way an operator would recognize it at a
// glance, never the full 40 hex characters.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func (a *App) printBundleStatusJSON(report bundleStatusReport) error {
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Out, string(body))
	return nil
}

func (a *App) printBundleStatusTable(report bundleStatusReport) {
	ui := cliui.New(a.Out)
	fmt.Fprintf(a.Out, "\n%s\n%s\n", ui.Heading(report.Bundle),
		ui.Fields([]cliui.Field{
			{Label: "portable digest", Value: report.PortableDigest},
			{Label: "git", Value: report.GitCommit},
		}))
	if len(report.Targets) == 0 {
		fmt.Fprintln(a.Out, "\n  no known targets — lift this bundle, or pass --to-context to inspect one directly")
		return
	}
	rows := make([][]string, 0, len(report.Targets))
	for _, target := range report.Targets {
		detail := target.Detail
		if target.GitNote != "" && !strings.Contains(detail, target.GitNote) {
			if detail != "" {
				detail += "; "
			}
			detail += target.GitNote
		}
		rows = append(rows, []string{
			target.Context, target.Namespace, target.State, readyWord(target.Provider.Ready), readyWord(target.Agent.Ready),
			bundleBehindCell(target), bundleDeployedCell(target), bundleReceiptCell(target), target.Evaluation, target.Gate, detail,
		})
	}
	fmt.Fprintf(a.Out, "\n%s\n", ui.Report("Targets",
		[]string{"CONTEXT", "NAMESPACE", "STATE", "PROVIDER READY", "AGENT READY", "BEHIND", "DEPLOYED", "RECEIPT", "EVAL", "GATE", "DETAIL"}, rows,
		cliui.ColumnText, cliui.ColumnText, cliui.ColumnState, cliui.ColumnState, cliui.ColumnState, cliui.ColumnText, cliui.ColumnText, cliui.ColumnText, cliui.ColumnState, cliui.ColumnText, cliui.ColumnText))
	for _, target := range report.Targets {
		if len(target.ChangedFields) == 0 {
			continue
		}
		fmt.Fprintf(a.Out, "  %s/%s differing fields: %s\n", target.Context, target.Namespace, strings.Join(target.ChangedFields, ", "))
	}
}

func bundleBehindCell(target bundleTargetStatus) string {
	if target.State != bundleStateBehind || target.DeployedCommit == "" {
		return "-"
	}
	return fmt.Sprintf("%d", target.Behind)
}

func bundleDeployedCell(target bundleTargetStatus) string {
	if target.DeployedCommit == "" {
		return "-"
	}
	return target.DeployedCommit
}

func bundleReceiptCell(target bundleTargetStatus) string {
	if target.ReceiptID != "" {
		return "recorded:" + shortSHA(target.ReceiptID)
	}
	if target.NoReceipt {
		return "no receipt"
	}
	return "recorded"
}
