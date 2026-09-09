package app

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

// LiftDown removes what the lift created, and the two branches have opposite
// rules rather than different degrees of the same rule.
//
// On a cluster this path CREATED, everything went into one resource group and
// all of it comes out with the group — and the proof is that the group is
// afterwards gone.
//
// On a cluster somebody else created, the cluster and its resource group are
// never deleted and never adopted. Only the resources this run added come
// out, only by the id recorded when they went in, and anything whose id
// cannot be re-resolved is LEFT and named rather than removed on a guess.
// Deleting by name on a subscription we do not own is how a demo removes a
// stranger's production monitoring; the ordering of those two failures is not
// close, but leaving a resource quietly billing is bad enough that it is
// reported loudly rather than mentioned.
func (a *App) LiftDown(opt lift.Options) error {
	if err := opt.ValidateForTeardown(); err != nil {
		return err
	}
	if err := a.preflight(depAz, depKubectl); err != nil {
		return err
	}
	acct, err := a.azAccount()
	if err != nil {
		return err
	}
	record, err := a.readLiftRecord(opt.ResourceGroup, opt.Cluster)
	if err != nil {
		// No record is not the same as nothing to do, and it must not read
		// as success. On the created branch the resource group is still
		// removable by hand; on the other, resources may be billing with no
		// account of what they are.
		return fmt.Errorf(`%w

  Without the record there is no list of ids this run created, and this
  refuses to go looking by name — on a subscription that is not ours, a name
  can belong to somebody else. If you know a lift ran here, remove what it
  made by hand from the Azure portal, or delete the whole resource group if
  you created it for this.`, err)
	}
	if record.Subscription != acct.ID {
		return fmt.Errorf("kmx lift down: the record was written against a different subscription than the CLI is signed in to. Refusing to delete anything (az account set --subscription ...)")
	}
	if record.Branch != opt.Branch() {
		return fmt.Errorf("kmx lift down: this lift was recorded as %q and you asked for %q teardown. These have opposite rules, so the difference is refused rather than reconciled", record.Branch, opt.Branch())
	}

	if record.Branch == lift.BringYourOwn {
		return a.liftDownBringYourOwn(opt, record)
	}
	return a.liftDownCreated(opt, record)
}

// liftDownCreated hands the whole resource group to the script that already
// knows how to delete one safely: it refuses a group that does not carry the
// tag this path stamps, takes a confirmation naming the group, waits for the
// delete rather than reporting the request, and re-checks that the group is
// gone before saying so.
func (a *App) liftDownCreated(opt lift.Options, record *lift.Record) error {
	fmt.Fprintf(a.Err, "\nkmx lift down: this will irreversibly delete resource group %q and its contents,\n"+
		"  plus any outside resources listed in this run's record.\n", opt.ResourceGroup)
	if err := a.confirmLiftDown(opt); err != nil {
		return err
	}
	work, cleanup, err := a.liftWorkspace()
	if err != nil {
		return err
	}
	defer cleanup()

	if outside := record.Outside(); len(outside) > 0 {
		// A group deletion proves cleanup only for what was inside it. If a
		// resource landed elsewhere, say so before the group goes, while its
		// id is still on screen.
		fmt.Fprintln(a.Err, "\nkmx lift down: these were created OUTSIDE the resource group, so deleting")
		fmt.Fprintln(a.Err, "  the group will not remove them. Each is removed and checked separately:")
		for _, res := range outside {
			fmt.Fprintf(a.Err, "    %s %s\n", res.Kind, res.Name)
		}
		if err := a.removeRecorded(outside); err != nil {
			return err
		}
	}

	if err := a.runScript(work, "scripts/aks-down.sh", map[string]string{
		"AKS_RESOURCE_GROUP": opt.ResourceGroup,
		"AKS_CLUSTER":        opt.Cluster,
		// Go already verified consent for this group before ANY deletion.
		// Runner does not forward stdin; the script must not prompt again.
		"KAIMAHI_CONFIRM": opt.ResourceGroup,
	}); err != nil {
		return err
	}

	// The script already re-checks, and this checks again from here rather
	// than trusting an exit status: the claim being made is "it is gone",
	// and an unreadable answer is not that claim.
	state, err := a.groupExists(opt.ResourceGroup)
	switch {
	case err != nil, state == lift.Unusable:
		return fmt.Errorf("kmx lift down: the delete returned, but the resource group's state could not be re-checked — NOT claiming it is gone. If it is still there it is still billing:\n    az group exists --name %s", shellArg(opt.ResourceGroup))
	case state == lift.Present:
		return fmt.Errorf("kmx lift down: resource group %s still exists after the delete returned", opt.ResourceGroup)
	}
	a.forgetLiftRecord(opt)
	fmt.Fprintf(a.Err, "\nkmx lift down: resource group %s is gone (az group exists says false).\n"+
		"  Cleanup covers that group and the outside resources in this run's record; other resources and billing were not checked.\n", opt.ResourceGroup)
	return nil
}

// liftDownBringYourOwn removes the monitoring this run added to a cluster it
// does not own, and nothing else.
func (a *App) liftDownBringYourOwn(opt lift.Options, record *lift.Record) error {
	fmt.Fprintf(a.Err, `----------------------------------------------------------------
  kmx lift down — on a cluster YOU created

  cluster %q and resource group %q are NOT deleted. They were
  not created here and they are not deleted here.

  What comes out is only what this run put in, by the id it recorded:
`, opt.Cluster, opt.ResourceGroup)
	for _, res := range record.Created {
		fmt.Fprintf(a.Err, "    %s %s\n", res.Kind, res.Name)
	}
	fmt.Fprintln(a.Err, "----------------------------------------------------------------")

	if err := a.confirmLiftDown(opt); err != nil {
		return err
	}

	a.aimAtTheCluster(opt)

	// The in-cluster objects go FIRST, before the add-ons that read them.
	//
	// Order matters here in a way it did not when the scrape job lived in a
	// ConfigMap. The PodMonitor's KIND is defined by a custom resource
	// definition the metrics add-on installs, and disabling the add-on can
	// take that definition — and every object of that kind — with it. Removing
	// ours afterwards would then be a delete against a kind the cluster no
	// longer has: it would either fail, or succeed only because something else
	// had already done the work. Neither is this command doing what it says.
	// Removed first, the deletion is ours and is observable.
	if err := a.removeInClusterObservability(record); err != nil {
		return fmt.Errorf("kmx lift down: in-cluster cleanup is incomplete; the run record has been kept.\n  Retry: %s\n%w", a.liftCommand(opt, true), err)
	}

	// Then the add-ons. They hold references to the workspaces, and a
	// workspace deleted while something still routes to it leaves the cluster
	// reporting an error nobody asked for.
	// Only add-ons THIS RUN enabled are turned off. An operator who already
	// had Container Insights running would otherwise have it switched off by a
	// teardown that was only ever meant to remove what the lift added — the
	// same mistake as deleting their workspace, made quieter by the fact that
	// nothing disappears, it just stops collecting.
	if record.Before.WeEnabledMetrics() {
		a.warnAboutScrapeJobsTheAddonOwns()
		a.notef("turning off the Managed Prometheus this run enabled")
		if err := a.Run.Run("az", "aks", "update", "--name", opt.Cluster, "--resource-group", opt.ResourceGroup,
			"--disable-azure-monitor-metrics", "--output", "none"); err != nil {
			return fmt.Errorf("could not disable Managed Prometheus on your cluster — stopping before deleting anything it still points at: %w", err)
		}
	} else {
		a.notef("Managed Prometheus was already on before this run, or its prior state was not established; leaving it unchanged.")
	}
	if record.Before.WeEnabledLogs() {
		a.notef("turning off the Container Insights this run enabled")
		if err := a.Run.Run("az", "aks", "disable-addons", "--name", opt.Cluster, "--resource-group", opt.ResourceGroup,
			"--addons", "monitoring", "--output", "none"); err != nil {
			return fmt.Errorf("could not disable Container Insights on your cluster — stopping before deleting anything it still points at: %w", err)
		}
	} else {
		a.notef("Container Insights was already on before this run, or its prior state was not established; leaving it unchanged.")
	}

	if err := a.removeRecorded(record.Created); err != nil {
		return err
	}
	if !record.Before.Recorded {
		a.notef("in-cluster monitoring ownership was not established; cleanup was not checked. The run record has been kept.")
		return nil
	}
	a.forgetLiftRecord(opt)
	return nil
}

// warnAboutScrapeJobsTheAddonOwns says the one thing an operator cannot find
// out afterwards.
//
// The PodMonitor KIND belongs to the metrics add-on: it installs the custom
// resource definition, and disabling it takes that definition away — which
// takes every object of that kind on the cluster with it, whoever wrote them.
// Measured on a live cluster, not inferred: after `az aks update
// --disable-azure-monitor-metrics`, `kubectl get podmonitors…` answers "the
// server doesn't have a resource type".
//
// This is not something this command can avoid. Turning off an add-on this run
// turned on is exactly what teardown is for, and the collection is Kubernetes
// doing what it does. What it can do is refuse to be quiet about it, because
// the alternative is an operator whose own scrape jobs are gone with nothing
// having said so. Their manifests still exist wherever they keep them; what is
// lost is the objects, and re-applying them once the add-on is back is the fix.
func (a *App) warnAboutScrapeJobsTheAddonOwns() {
	out, err := a.kubectlCapture("get", scrapeMonitorResource, "--all-namespaces",
		"-o", "jsonpath={range .items[*]}{.metadata.namespace}/{.metadata.name} {end}")
	switch {
	case err == nil:
	case noSuchResourceType(err):
		// The kind is not on this cluster, so no PodMonitor of anyone's can be
		// here to lose. That is an answer, and the only one that justifies
		// saying nothing.
		return
	default:
		// Anything else — an unreachable API server, an RBAC denial — is not
		// "there are none". Read that way it would produce silence at exactly
		// the moment an operator most needs a sentence, and the add-on would
		// go anyway.
		//
		// It warns and continues rather than refusing: this read is advisory,
		// teardown is what the operator asked for, and stopping here would
		// leave two workspaces billing because a WARNING could not be
		// computed. What is at risk is recoverable — their manifests are
		// untouched — and teardown is re-runnable. The rule this project
		// applies elsewhere, that an unknown must not authorise a destructive
		// act, governs deletions; it is not a reason to abandon a teardown.
		a.notef(`could not read the PodMonitors on this cluster (%v), so this cannot say
  whether turning Managed Prometheus off will take any of yours with it — it
  removes the PodMonitor KIND, and Kubernetes collects every object of that
  kind. Check by hand:

    kubectl --context %s get %s --all-namespaces`,
			err, a.Cfg.KubeContext, scrapeMonitorResource)
		return
	}
	var theirs []string
	for _, name := range strings.Fields(out) {
		if name != "kaimahi/"+scrapeMonitor {
			theirs = append(theirs, name)
		}
	}
	if len(theirs) == 0 {
		return
	}
	a.notef(`turning Managed Prometheus off removes the PodMonitor KIND itself, and with it
  every PodMonitor on this cluster — including %s, which
  this run did not create and would otherwise leave alone. The add-on owns
  that custom resource definition; nothing here can disable one without the
  other. Re-apply your own manifests when the add-on is back.`,
		strings.Join(theirs, ", "))
}

// removeInClusterObservability takes back the two cluster-side objects — and
// only the ones this run created.
//
// Ownership comes from what the run RECORDED before it applied anything, never
// from what the object looks like now. A PodMonitor holding only our scrape
// job may still have been created by the operator, and "it looks like ours" is
// not "we made it"; the same goes for a NetworkPolicy of that name they had
// already written themselves.
//
// Both objects are in the kaimahi namespace, and neither is the cluster-wide
// ama-metrics-prometheus-config ConfigMap. Teardown does not read that
// ConfigMap, does not edit it and does not delete it, because the lift does not
// write it — an adopter's other scrape jobs are not something this command
// should ever have been in a position to remove.
func (a *App) removeInClusterObservability(record *lift.Record) error {
	var cleanupErr error
	if record.Before.WeCreatedScraperPolicy() {
		if err := a.kubectlRun("-n", "kaimahi", "delete", "networkpolicy", scraperPolicy, "--ignore-not-found"); err != nil {
			cleanupErr = fmt.Errorf("could not remove the scraper's NetworkPolicy allowance: %w\n"+
				"    kubectl --context %s -n kaimahi delete networkpolicy %s", err, shellArg(a.Cfg.KubeContext), scraperPolicy)
		}
	} else {
		a.notef("the NetworkPolicy %s was there before this run, or its origin was never established; leaving it.", scraperPolicy)
	}

	if !record.MayRemoveScrapeMonitor() {
		a.notef("the PodMonitor %s in kaimahi was there before this run, was never applied by it, "+
			"or its origin was never established; leaving it.", scrapeMonitor)
		return cleanupErr
	}
	if err := a.kubectlRun("-n", "kaimahi", "delete", scrapeMonitorResource, scrapeMonitor, "--ignore-not-found"); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("could not remove the scrape job: %w\n"+
			"    kubectl --context %s -n kaimahi delete %s %s", err, shellArg(a.Cfg.KubeContext), scrapeMonitorResource, scrapeMonitor))
	}
	return cleanupErr
}

// removeRecorded deletes recorded resources by their recorded id, confirming
// each one first, and reports everything it did not remove.
func (a *App) removeRecorded(resources []lift.Resource) error {
	if len(resources) == 0 {
		fmt.Fprintln(a.Err, "\nkmx lift down: no Azure resource ids were recorded; no Azure resources were checked or removed.")
		return nil
	}
	var removals []lift.Removal
	deleted, alreadyGone := 0, 0
	for _, res := range resources {
		state, resolved := a.confirmRecordedResource(res.ID)
		rm := lift.PlanRemoval(res, state, resolved)
		switch {
		case rm.Delete:
			if err := a.Run.Run("az", "resource", "delete", "--ids", res.ID, "--output", "none"); err != nil {
				rm.Delete = false
				rm.Reason = fmt.Sprintf("the delete failed and it may still be billing: %v", err)
			} else {
				deleted++
				a.notef("removed %s %s", res.Kind, res.Name)
			}
		case state == lift.Absent:
			// Turning the add-ons off takes their own rules and rule groups
			// with them, so several recorded resources are legitimately gone
			// before we reach them. Counted rather than passed over in
			// silence: "we deleted twelve things" and "six were already gone"
			// are different claims, and only one of them is true.
			alreadyGone++
			a.notef("already gone, nothing to delete: %s %s", res.Kind, res.Name)
		}
		removals = append(removals, rm)
	}

	left := lift.LeftBehind(removals)
	if len(left) == 0 {
		fmt.Fprintf(a.Err, "\nkmx lift down: recorded Azure resources: %d delete operations completed, %d already gone. Unrecorded resources were not checked.\n",
			deleted, alreadyGone)
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "kmx lift down: %d resource(s) were NOT removed, and they may still be billing.\n\n", len(left))
	for _, rm := range left {
		// PlanRemoval's diagnostic contains a raw command; quote its id here.
		rm.Reason = strings.ReplaceAll(rm.Reason, "az resource show --ids "+rm.Resource.ID, "az resource show --ids "+shellArg(rm.Resource.ID))
		fmt.Fprintf(&b, "  %s %q\n    %s\n", rm.Resource.Kind, rm.Resource.Name, rm.Reason)
		if rm.Resource.Billing != "" {
			fmt.Fprintf(&b, "    cost while it exists: %s\n", rm.Resource.Billing)
		}
		fmt.Fprintf(&b, "    id: %s\n\n", rm.Resource.ID)
	}
	b.WriteString("  Nothing was deleted on a guess. Check each id above and remove it by hand\n")
	b.WriteString("  if it is yours. The run record has been kept so this can be re-run.")
	return errors.New(b.String())
}

func (a *App) confirmLiftDown(opt lift.Options) error {
	name, kind := opt.Cluster, "cluster"
	if !opt.BringYourOwn {
		name, kind = opt.ResourceGroup, "resource group"
	}
	proceed := fmt.Sprintf("  to proceed:  KAIMAHI_CONFIRM=%s %s", shellArg(name), a.liftCommand(opt, true))
	if c := strings.TrimSpace(a.Cfg.Confirm); c != "" {
		if c == name {
			return nil
		}
		return fmt.Errorf("kmx lift down: KAIMAHI_CONFIRM does not name this %s — refusing.\n%s", kind, proceed)
	}
	if a.Stdin == nil || !isTerminalFile(a.Stdin) {
		return fmt.Errorf("kmx lift down: no TTY and no KAIMAHI_CONFIRM — refusing to act unattended on a cloud subscription.\n%s", proceed)
	}
	fmt.Fprintf(a.Err, "Type the %s name to confirm teardown (anything else aborts): ", kind)
	if readTrimmedLine(a.Stdin) != name {
		return errors.New("kmx lift down: not confirmed — nothing was deleted")
	}
	return nil
}

// forgetLiftRecord removes the record once everything in it is gone. It is
// only ever called after a complete removal: a record deleted while resources
// survive would take with it the only list of what they are.
func (a *App) forgetLiftRecord(opt lift.Options) {
	path, err := liftRecordPath(opt.ResourceGroup, opt.Cluster)
	if err != nil {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		a.notef("could not remove the run record %s: %v", path, err)
	}
}
