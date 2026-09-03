package app

import (
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
)

// The read-only views of the plane: what it has spent, what has been
// granted, and what the enforcement points decided.
//
// UNGUARDED, exactly like `make ledger` and the audit targets. The line the
// guard draws is not "mutates" but "can be aimed somewhere unintended", and
// these reach the cluster only through kubectl carrying an explicit
// --context: they land wherever the rest of the invocation was already going
// to land, and they change nothing when they get there.

// Ledger prints the spend ledger and the month-to-date totals.
func (a *App) Ledger(credential string) error {
	return a.session(func(c *admin.Client) error { return c.Ledger(a.Out, credential) })
}

// Grants lists grants with liveness — an expired grant is not a grant.
func (a *App) Grants(credential string) error {
	return a.session(func(c *admin.Client) error { return c.Grants(a.Out, credential) })
}

// Flow prints one credential's four audit trails merged into a single
// chronological reading — what was triggered, what it spent, what it called,
// what it was refused, and what a human let through.
func (a *App) Flow(credential string) error {
	return a.session(func(c *admin.Client) error { return c.Flow(a.Out, credential) })
}

// Audit prints one of the plane's audit trails.
func (a *App) Audit(kind, credential string) error {
	switch kind {
	case "tool":
		return a.session(func(c *admin.Client) error { return c.ToolAudit(a.Out, credential) })
	case "approval":
		return a.session(func(c *admin.Client) error { return c.ApprovalAudit(a.Out, credential) })
	default:
		return fmt.Errorf("usage: kmx audit tool|approval [<credential>]")
	}
}

// session opens an admin session for one command and closes it again. Each
// command is its own port-forward: kmx is a CLI, not a daemon, and a forward
// that outlived its command would be exactly the stale forward the plumbing
// refuses to talk through.
func (a *App) session(do func(*admin.Client) error) error {
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	client, err := admin.Open(a, a.Cfg.AdminPort, a.Err)
	if err != nil {
		return err
	}
	defer client.Close()
	return do(client)
}

// Credentials lists the governed credentials and when each one expires —
// the view that makes an expiry something an operator SEES COMING rather
// than diagnoses at 3am.
func (a *App) Credentials() error {
	return a.session(func(c *admin.Client) error { return c.Credentials(a.Out) })
}

// RenewCredential extends a credential's deadline. It is not a mutation
// the guard covers for the same reason the views are not: it lands
// wherever the invocation was already going to land, and it moves no
// credential material — kmx accepts none in any form, and renewal
// mints none either, so no Secret has to be rewritten.
func (a *App) RenewCredential(name string, ttl *int64) error {
	return a.session(func(c *admin.Client) error {
		expires, err := c.RenewCredential(name, ttl)
		if err != nil {
			return err
		}
		a.notef("Credential %q now expires %s.", name, expires)
		return nil
	})
}
