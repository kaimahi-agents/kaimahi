package app

import (
	"fmt"
	"strings"
)

// Down deletes the kind cluster `kmx up` created.
//
// Two things have to agree before anything is deleted, and neither is the
// kubeconfig on its own.
//
// The guard vouches for KUBE_CTX; this deletes KIND_CLUSTER. They agree by
// default, but both are overridable, so `KUBE_CTX=kind-safe
// KIND_CLUSTER=other kmx down` would show a banner naming one cluster and
// destroy another. Refuse when the thing confirmed is not the thing deleted.
//
// And `kind delete cluster` deletes by CONTAINER name: it never reads the
// kubeconfig, so a kubeconfig that has never heard of this context is not
// evidence there is nothing to delete. The guard's "context not created
// yet" allowance is bring-up's, and taking it here is how a real cluster
// gets deleted under a banner saying it does not exist — which happened.
// So the container engine is asked first, and a cluster the kubeconfig
// cannot vouch for needs a confirmation naming it.
func (a *App) Down() error {
	if err := a.preflight(depKind, depKubectl, a.engineDependency()); err != nil {
		return err
	}
	want := "kind-" + a.Cfg.KindCluster
	if a.Cfg.KubeContext != want {
		return fmt.Errorf("refusing: the guard would check context %s, but this would\n"+
			"delete kind cluster %q (context %s).\n"+
			"Set KIND_CLUSTER and KUBE_CTX consistently, or just KIND_CLUSTER.",
			a.Cfg.KubeContext, a.Cfg.KindCluster, want)
	}
	// A failed listing is not an empty one — a stopped daemon or an
	// unreachable socket would otherwise read as "already gone", and the
	// operator would be told the cluster was deleted by a run that could
	// not see it.
	listed, err := a.Run.Capture("kind", "get", "clusters")
	if err != nil {
		return fmt.Errorf("`kind get clusters` failed — refusing to guess whether %q exists "+
			"(is the %s daemon running?): %w", a.Cfg.KindCluster, a.Cfg.ContainerEngine, err)
	}
	if !containsLine(listed, a.Cfg.KindCluster) {
		a.notef("no kind cluster named %q — nothing to delete.", a.Cfg.KindCluster)
		return nil
	}
	if err := a.GuardKnown(fmt.Sprintf("DELETE the kind cluster %q", a.Cfg.KindCluster), "kmx down"); err != nil {
		return err
	}
	return a.Run.Run("kind", "delete", "cluster", "--name", a.Cfg.KindCluster)
}

// containsLine reports whether name is one of the lines in out.
func containsLine(out, name string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}
