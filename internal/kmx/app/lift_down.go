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
		"KAIMAHI_CONFIRM":    a.Cfg.Confirm,
	}); err != nil {
		return err
	}

	// The script already re-checks, and this checks again from here rather
	// than trusting an exit status: the claim being made is "it is gone",
	// and an unreadable answer is not that claim.
	state, err := a.groupExists(opt.ResourceGroup)
	switch {
	case err != nil, state == lift.Unusable:
		return fmt.Errorf("kmx lift down: the delete returned, but the resource group's state could not be re-checked — NOT claiming it is gone. If it is still there it is still billing:\n    az group exists --name %s", opt.ResourceGroup)
	case state == lift.Present:
		return fmt.Errorf("kmx lift down: resource group %s still exists after the delete returned", opt.ResourceGroup)
	}
	a.forgetLiftRecord(opt)
	fmt.Fprintf(a.Err, "\nkmx lift down: resource group %s is gone (az group exists says false). Nothing is billing.\n", opt.ResourceGroup)
	return nil
}

// liftDownBringYourOwn removes the monitoring this run added to a cluster it
// does not own, and nothing else.
func (a *App) liftDownBringYourOwn(opt lift.Options, record *lift.Record) error {
	fmt.Fprintf(a.Err, `----------------------------------------------------------------
  kmx lift down — on a cluster YOU created

  cluster %q and resource group %q are NOT touched. They were
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

	// The add-ons are turned off first. They hold references to the
	// workspaces, and a workspace deleted while something still routes to it
	// leaves the cluster reporting an error nobody asked for.
	a.aimAtTheCluster(opt)

	// Only add-ons THIS RUN enabled are turned off. An operator who already
	// had Container Insights running would otherwise have it switched off by a
	// teardown that was only ever meant to remove what the lift added — the
	// same mistake as deleting their workspace, made quieter by the fact that
	// nothing disappears, it just stops collecting.
	if record.Before.WeEnabledMetrics() {
		a.notef("turning off the Managed Prometheus this run enabled")
		if err := a.Run.Run("az", "aks", "update", "--name", opt.Cluster, "--resource-group", opt.ResourceGroup,
			"--disable-azure-monitor-metrics", "--output", "none"); err != nil {
			return fmt.Errorf("could not disable Managed Prometheus on your cluster — stopping before deleting anything it still points at: %w", err)
		}
	} else {
		a.notef("Managed Prometheus was already on before this run; leaving it on.")
	}
	if record.Before.WeEnabledLogs() {
		a.notef("turning off the Container Insights this run enabled")
		if err := a.Run.Run("az", "aks", "disable-addons", "--name", opt.Cluster, "--resource-group", opt.ResourceGroup,
			"--addons", "monitoring", "--output", "none"); err != nil {
			return fmt.Errorf("could not disable Container Insights on your cluster — stopping before deleting anything it still points at: %w", err)
		}
	} else {
		a.notef("Container Insights was already on before this run; leaving it on.")
	}

	// The in-cluster objects, removed on the same rule: only what this run
	// created, decided by what it recorded at the time and never by what the
	// object contains now.
	a.removeInClusterObservability(record)

	if err := a.removeRecorded(record.Created); err != nil {
		return err
	}
	a.forgetLiftRecord(opt)
	return nil
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
func (a *App) removeInClusterObservability(record *lift.Record) {
	if record.Before.WeCreatedScraperPolicy() {
		if !a.kubectlQuiet("-n", "kaimahi", "delete", "networkpolicy", scraperPolicy, "--ignore-not-found") {
			a.notef("could not remove the scraper's NetworkPolicy allowance — remove it by hand:\n"+
				"    kubectl --context %s -n kaimahi delete networkpolicy %s", a.Cfg.KubeContext, scraperPolicy)
		}
	} else {
		a.notef("the NetworkPolicy %s was there before this run, or its origin was never established; leaving it.", scraperPolicy)
	}

	// A cluster whose metrics add-on has no PodMonitor CRD never carried one
	// of ours, and asking kubectl to delete a kind it does not know is an
	// error rather than a no-op — it would print "remove it by hand" for an
	// object that cannot exist.
	if present, err := a.clusterObjectExists("crd", scrapeMonitorResource); err == nil && !present {
		a.notef("this cluster has no PodMonitor CRD, so this run created no scrape job on it.")
		return
	}
	if !record.MayRemoveScrapeMonitor() {
		a.notef("the PodMonitor %s in kaimahi was there before this run, was never applied by it, "+
			"or its origin was never established; leaving it.", scrapeMonitor)
		return
	}
	if !a.kubectlQuiet("-n", "kaimahi", "delete", scrapeMonitorResource, scrapeMonitor, "--ignore-not-found") {
		a.notef("could not remove the scrape job — remove it by hand:\n"+
			"    kubectl --context %s -n kaimahi delete %s %s", a.Cfg.KubeContext, scrapeMonitorResource, scrapeMonitor)
	}
}

// removeRecorded deletes recorded resources by their recorded id, confirming
// each one first, and reports everything it did not remove.
func (a *App) removeRecorded(resources []lift.Resource) error {
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
		fmt.Fprintf(a.Err, "\nkmx lift down: nothing this run created is left. %d removed by recorded id, %d already gone.\n",
			deleted, alreadyGone)
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "kmx lift down: %d resource(s) were NOT removed, and they may still be billing.\n\n", len(left))
	for _, rm := range left {
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
	proceed := fmt.Sprintf("  to proceed:  KAIMAHI_CONFIRM=%s kmx lift down --byo %s", opt.Cluster, liftIdentityFlags(opt))
	if c := strings.TrimSpace(a.Cfg.Confirm); c != "" {
		if c == opt.Cluster {
			return nil
		}
		return fmt.Errorf("kmx lift down: KAIMAHI_CONFIRM does not name this cluster — refusing.\n%s", proceed)
	}
	if a.Stdin == nil || !isTerminalFile(a.Stdin) {
		return fmt.Errorf("kmx lift down: no TTY and no KAIMAHI_CONFIRM — refusing to act unattended on a cloud subscription.\n%s", proceed)
	}
	fmt.Fprint(a.Err, "Type the cluster name to remove this run's monitoring (anything else aborts): ")
	if readTrimmedLine(a.Stdin) != opt.Cluster {
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
