package app

import (
	"encoding/json"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
)

// Budgets, and the approval verbs.
//
// internal/kmx/admin owns the transport and validation. What is decided HERE
// is which operations run behind the context guard, and that follows the
// Makefile exactly: `budget`, `approve`, `deny` and `request` are `guard`
// prerequisites there because each one changes what an agent may do or
// spend, and `approvals` is not, because it is a read.

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

// Approvals lists the requests waiting for a decision. A read: unguarded,
// like `make approvals`.
func (a *App) Approvals() error {
	return a.session(func(c *admin.Client) error { return c.Approvals(a.Out) })
}

// Approve mints the bounded grant a pending request asked for.
//
// The bounds are refused before the guard runs, not after: `kmx approve
// <id>` with no bounds is a mistake the plane would reject anyway, and
// asking an operator to confirm a context for an operation that cannot
// succeed teaches them to confirm without reading.
func (a *App) Approve(id string, ttlSeconds, maxUses, amount *int64) error {
	if err := admin.ValidRequestID(id); err != nil {
		return err
	}
	if err := admin.CheckBounds(ttlSeconds, maxUses); err != nil {
		return err
	}
	if err := admin.CheckCap("amount", amount); err != nil {
		return err
	}
	args := []string{"approve", id}
	if ttlSeconds != nil {
		args = append(args, "--ttl", fmt.Sprint(*ttlSeconds))
	}
	if maxUses != nil {
		args = append(args, "--uses", fmt.Sprint(*maxUses))
	}
	if amount != nil {
		args = append(args, "--amount", fmt.Sprint(*amount))
	}
	if err := a.Guard(fmt.Sprintf("approve pending request %s: ttl_seconds=%s uses=%s amount=%s (null means unset)", id, capOrNone(ttlSeconds), capOrNone(maxUses), capOrNone(amount)), a.operationCommand(args...)); err != nil {
		return err
	}
	return a.session(func(c *admin.Client) error {
		grant, err := c.Approve(id, ttlSeconds, maxUses, amount)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, admin.GrantSummary(grant))
		return nil
	})
}

// Deny refuses a pending request.
func (a *App) Deny(id string) error {
	if err := admin.ValidRequestID(id); err != nil {
		return err
	}
	if err := a.Guard("deny pending request "+id, a.operationCommand("deny", id)); err != nil {
		return err
	}
	return a.session(func(c *admin.Client) error {
		if err := c.Deny(id); err != nil {
			return err
		}
		a.notef("Request %s denied.", id)
		return nil
	})
}

// Request files an approval request explicitly.
//
// args (tool requests only) names the CALL to pre-approve. Omitting it
// means the ARGUMENT-LESS call, never "any call" — the distinction that
// welding a grant to its arguments, and not just to the verb, exists to make.
func (a *App) Request(credential, kind, subject string, args map[string]any) error {
	if err := admin.ValidRequest(credential, kind, subject, args); err != nil {
		return err
	}
	commandArgs := []string{"request", kind, subject, "--credential", credential}
	action := fmt.Sprintf("file a %s approval request for %q (%s)", kind, credential, subject)
	if kind == "tool" {
		if args == nil {
			action += ": argument-less call (not any call)"
		} else {
			body, err := json.Marshal(args)
			if err != nil {
				return fmt.Errorf("cannot encode tool call arguments: %w", err)
			}
			action += ": arguments=" + string(body)
			commandArgs = append(commandArgs, "--args", string(body))
		}
	}
	if err := a.Guard(action, a.operationCommand(commandArgs...)); err != nil {
		return err
	}
	return a.session(func(c *admin.Client) error {
		deduped, err := c.Request(credential, kind, subject, args)
		if err != nil {
			return err
		}
		if deduped {
			a.notef("Already pending — deduped (run `kmx approvals`).")
		} else {
			a.notef("Approval request filed (run `kmx approvals`).")
		}
		return nil
	})
}
