package app

import (
	"fmt"
)

// The kagent namespace. Named here rather than reached for through config,
// because it is not a setting: it is where the chart puts everything.
//
// Nothing in kmx drives the legacy runtime any more. What survives is the
// READ side: `kmx status` reports what is on a cluster that still has it, and
// certificate publication puts the plane's authority where an owner-managed
// workload in that namespace can find it. Both are about a namespace that
// exists on somebody's cluster, not about a runtime kmx operates.
const config_kagentNamespace = "kagent"

// requireNamespace refuses before minting a one-time credential that cannot
// be stored in its destination namespace.
//
// It lives beside the constant above for want of a better home rather than
// because it is related: `kmx migrate` is its only caller, and the check is
// about the operator's own namespace.
func (a *App) requireNamespace(namespace, flag string) error {
	if _, err := a.kubectlCapture("get", "namespace", namespace, "-o", "name"); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("namespace %q does not exist, and it is where the credential's Secret would go.\n"+
				"  Nothing has been issued — the token is shown once, so this is refused before it is minted.\n"+
				"  Name the namespace your runtime reads its Secret from:\n"+
				"    %s <your namespace>", namespace, flag)
		}
		return fmt.Errorf("cannot tell whether namespace %q exists (refusing to guess): %w", namespace, err)
	}
	return nil
}
