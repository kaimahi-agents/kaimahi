package app

import (
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
)

// The model-plane views: what it has spent and what the model proxy decided.
//
// The views are UNGUARDED, exactly like `make ledger`:
// they reach the cluster only through kubectl carrying an explicit
// --context, so they land wherever the rest of the invocation was already
// going to land, and they change nothing when they get there.
//
// Budget and credential mutations below do change state and are guarded.

// Ledger prints the spend ledger and the month-to-date totals.
func (a *App) Ledger(credential string) error {
	return a.session(func(c *admin.Client) error { return c.Ledger(a.Out, credential) })
}

// Flow prints the model ledger chronologically: spend, calls and refusals.
func (a *App) Flow(credential string) error {
	return a.session(func(c *admin.Client) error { return c.Flow(a.Out, credential) })
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

// Budget replaces a credential's monthly caps. A nil cap is "no cap" — and
// `kmx budget` with no flags therefore CLEARS both, which is what
// `make budget` with no CAP_* does and what CI relies on.
func (a *App) Budget(credential string, capCents, capTokens *int64) error {
	if err := admin.ValidCredentialName(credential); err != nil {
		return err
	}
	if err := admin.CheckCap("cap_cents", capCents); err != nil {
		return err
	}
	if err := admin.CheckCap("cap_tokens", capTokens); err != nil {
		return err
	}
	args := []string{"budget", credential}
	if capCents != nil {
		args = append(args, "--cents", fmt.Sprint(*capCents))
	}
	if capTokens != nil {
		args = append(args, "--tokens", fmt.Sprint(*capTokens))
	}
	if err := a.Guard(fmt.Sprintf("replace monthly caps for credential %q: cents=%s tokens=%s (null clears the cap)", credential, capOrNone(capCents), capOrNone(capTokens)),
		a.operationCommand(args...)); err != nil {
		return err
	}
	return a.session(func(c *admin.Client) error {
		if err := c.SetBudget(credential, capCents, capTokens); err != nil {
			return err
		}
		a.notef("Budget for %q: cap_cents=%s cap_tokens=%s (monthly, UTC).",
			credential, capOrNone(capCents), capOrNone(capTokens))
		return nil
	})
}

// capOrNone renders a cap for the operator note:
// the number, or `null` for "no cap".
func capOrNone(v *int64) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprint(*v)
}

// RenewCredential extends a credential's deadline. Unlike the views above
// it CHANGES something — an expiry is what stops a credential outliving the
// work it was issued for, and moving one is a governance decision even
// though no Secret is rewritten and no credential material moves. So it
// goes through the guard like every other mutation: the operator sees which
// cluster's credential they are extending before it is extended.
func (a *App) RenewCredential(name string, ttl *int64) error {
	if err := admin.ValidCredentialName(name); err != nil {
		return err
	}
	if err := admin.CheckCredentialTTL(ttl); err != nil {
		return err
	}
	args := []string{"credential", "renew", name}
	lifetime := "the plane's default lifetime"
	if ttl != nil {
		lifetime = fmt.Sprintf("%d seconds", *ttl)
		args = append(args, "--ttl", fmt.Sprint(*ttl))
	}
	if err := a.Guard(fmt.Sprintf("EXTEND the expiry of credential %q by renewing for %s", name, lifetime), a.operationCommand(args...)); err != nil {
		return err
	}
	return a.session(func(c *admin.Client) error {
		expires, err := c.RenewCredential(name, ttl)
		if err != nil {
			return err
		}
		a.notef("Credential %q now expires %s.", name, expires)
		return nil
	})
}

// IssueIdentityCredential creates an identity without retaining its bearer.
// The one-time bearer is validated and discarded rather than printed or stored.
func (a *App) IssueIdentityCredential(name string, ttl *int64) error {
	if err := a.Guard(fmt.Sprintf("issue identity-only credential %q and DISCARD its bearer", name), "kmx credential issue "+name+" --discard"); err != nil {
		return err
	}
	return a.session(func(c *admin.Client) error {
		created, err := c.IssueIdentityCredential(name, ttl)
		if err != nil {
			return err
		}
		if created {
			a.notef("Credential %q issued as an identity only; its bearer token was discarded, not stored.", name)
		} else {
			a.notef("Credential %q already issued; keeping it (no token is stored for it).", name)
		}
		return nil
	})
}

// IssueCredentialToSecret issues a bearer directly into Kubernetes custody.
// issueCredential contains the shared pre-POST binding and 409 safety checks
// used by govern; this entry point deliberately adds no second implementation.
func (a *App) IssueCredentialToSecret(name, secret, namespace string, ttl *int64) error {
	if err := validCredentialName(name); err != nil {
		return err
	}
	if secret == "" || namespace == "" {
		return fmt.Errorf("kmx credential issue: secret and namespace must be named")
	}
	command := "kmx credential issue " + name + " --secret " + secret
	if err := a.Guard(fmt.Sprintf("issue credential %q into Secret %s/%s", name, namespace, secret), command); err != nil {
		return err
	}
	return a.session(func(c *admin.Client) error {
		return a.issueCredential(c, name, GovernOptions{
			Secret:          secret,
			SecretNamespace: namespace,
			TTLSeconds:      ttl,
			Command:         command,
		}, false)
	})
}
