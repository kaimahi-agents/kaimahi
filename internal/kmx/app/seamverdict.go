package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// What kagent's `Accepted` condition actually is, and why reading it as a
// live answer told operators a broken credential worked.
//
// `Accepted` is a CACHED reconcile result. kagent records it when it last
// tried to reach an upstream and does not retry on its own, and a Secret
// mounted into a pod is not updated the instant it is written — the kubelet
// refreshes projected Secrets on its own sync period. So for minutes after a
// credential is written, the condition still carries the verdict reached
// against the credential BEFORE it. Written correctly, that reads as a
// harmless delay. Written wrongly, it reads as `Accepted: yes` for a
// credential that cannot be used, and five minutes of "it worked" is worse
// than an error, because the operator moves on.
//
// The rule, in the vocabulary the audit trail and `kmx status` already use —
// `none` is a known nothing, `unknown` is "we cannot say":
//
//	A verdict reached BEFORE the credential it is being asked about is not a
//	verdict. It is `unknown`.
//
// That is the whole of it. kmx does not guess whether the new credential
// works; it declines to reuse an answer that was about something else.
const (
	verdictAccepted = "accepted"
	verdictRejected = "rejected"
	verdictUnknown  = "unknown"
)

// seamVerdict is what kagent last decided about a seam, and when.
type seamVerdict struct {
	// State is accepted, rejected or unknown.
	State string
	// At is when kagent reached it. Zero when the condition carried no time.
	At time.Time
	// Message is kagent's own words on a rejection, kept verbatim: an
	// expired token and an unreachable host are different problems and the
	// upstream is the only thing that can tell them apart.
	Message string
	// Reason belongs to `unknown` and to nothing else, so a reader never has
	// to work out which meaning it carries.
	Reason string
}

// Fresh reports whether this verdict can be used to answer a question about
// something that happened at `since`.
func (v seamVerdict) Fresh(since time.Time) bool {
	return !since.IsZero() && !v.At.IsZero() && !v.At.Before(since)
}

// Line renders the verdict for a human.
func (v seamVerdict) Line(now time.Time) string {
	switch v.State {
	case verdictUnknown:
		return "unknown — " + v.Reason
	case verdictRejected:
		return fmt.Sprintf("rejected %s ago — %s", age(now, v.At), v.Message)
	default:
		return fmt.Sprintf("accepted %s ago", age(now, v.At))
	}
}

func age(now, then time.Time) string {
	if then.IsZero() {
		return "an unknown time"
	}
	d := now.Sub(then).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return d.String()
}

// classifySeamVerdict reads one `Accepted` condition.
//
// since is the moment the caller changed something the verdict would have to
// be about — the credential it just wrote. Pass the zero time to ask only
// "what does kagent currently think", which is what `kmx status` asks: it
// changed nothing, so it has no write to compare against and must not invent
// one.
//
// **This comparison assumes kmx's clock and the API server's agree**, and the
// assumption is worth stating because the two failure directions are not
// symmetric. `since` is kmx's wall clock; the condition's time is the API
// server's. If kmx's clock runs AHEAD, fresh verdicts look stale and are
// reported `unknown` — safe. If it runs BEHIND by more than the gap between
// writing the credential and reading the verdict, a stale verdict looks fresh
// and is reported as an answer — which is the failure this whole file exists
// to prevent.
//
// It is not defended against here, deliberately. Padding `since` by a skew
// allowance would trade a narrow fail-open for a much broader loss: a verdict
// arriving inside the allowance can never be newer than the padded `since`,
// so a REJECTION — measured at 14 seconds on a live cluster, and the case
// most worth catching — would be reported `unknown` instead. The fix that
// costs nothing is a server-side causal marker: take the seam's current
// lastTransitionTime BEFORE the write as the baseline and require the verdict
// to have moved past it, comparing the API server's clock only with itself.
// That is the change to make if this ever bites; on kind the API server
// shares the host's clock, and on a managed cluster both ends are
// NTP-synchronised, so it has not.
func classifySeamVerdict(c *serverCondition, generation, observed int64, since time.Time) seamVerdict {
	if generation != 0 && observed != generation {
		return seamVerdict{State: verdictUnknown,
			Reason: fmt.Sprintf("kagent is still reconciling this seam (generation %d, observed %d)", generation, observed)}
	}
	if c == nil {
		return seamVerdict{State: verdictUnknown, Reason: "kagent has recorded no verdict for this seam yet"}
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(c.LastTransitionTime))
	if err != nil && strings.TrimSpace(c.LastTransitionTime) != "" {
		at = time.Time{}
	}
	// A verdict with no time cannot be told apart from one reached before
	// the caller's change, so it cannot answer a question about that change.
	if !since.IsZero() && at.IsZero() {
		return seamVerdict{State: verdictUnknown, Message: c.Message,
			Reason: "kagent's verdict carries no timestamp, so it cannot be told apart from one reached " +
				"before the credential was written"}
	}
	if !since.IsZero() && at.Before(since) {
		return seamVerdict{State: verdictUnknown, At: at, Message: c.Message,
			Reason: fmt.Sprintf("kagent last checked this seam %s before the credential was written, and has not "+
				"looked since — a mounted Secret is not updated the instant it is written, so this says nothing "+
				"about the credential that is there now", age(since, at))}
	}
	if c.Status == "True" {
		return seamVerdict{State: verdictAccepted, At: at}
	}
	message := strings.TrimSpace(c.Message)
	if message == "" {
		message = "kagent did not say why"
	}
	return seamVerdict{State: verdictRejected, At: at, Message: message}
}

// pickCondition returns the Accepted condition, or nil.
func pickCondition(conditions []serverCondition, kind string) *serverCondition {
	for i := range conditions {
		if conditions[i].Type == kind {
			return &conditions[i]
		}
	}
	return nil
}

// readSeamVerdict asks the cluster what kagent last decided about one
// RemoteMCPServer, and when.
func (a *App) readSeamVerdict(namespace, server string, since time.Time) (seamVerdict, error) {
	raw, err := a.kubectlCapture("-n", namespace, "get", "remotemcpserver", server, "-o", "json")
	if err != nil {
		return seamVerdict{}, fmt.Errorf("cannot read RemoteMCPServer %q in namespace %s: %w", server, namespace, err)
	}
	var doc struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Status struct {
			ObservedGeneration int64             `json:"observedGeneration"`
			Conditions         []serverCondition `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return seamVerdict{}, fmt.Errorf("RemoteMCPServer %q returned invalid JSON: %w", server, err)
	}
	return classifySeamVerdict(pickCondition(doc.Status.Conditions, "Accepted"),
		doc.Metadata.Generation, doc.Status.ObservedGeneration, since), nil
}

// How long kmx waits for kagent to look again after a credential was written,
// and why the answer is asymmetric.
//
// A Kubernetes condition's lastTransitionTime records when the verdict
// CHANGED, not when it was last checked. Measured against a live cluster, on
// a governed tool seam whose credential was replaced with a well-formed but
// wrong token: the pass stood for 49 seconds and then flipped to
// Unauthorized on kagent's own resync, and flipped within 14 seconds when
// kagent was asked to look. Leave a GOOD credential in place and the
// condition never moves at all, because nothing about it changed.
//
// 90 seconds is chosen to sit above that measured window with room, so a
// credential kagent will reject is caught by the wait rather than after it.
//
// So the wait can confirm a REJECTION and can never confirm a pass. That
// asymmetry is the right way round for a fail-closed posture — the case worth
// blocking on is the broken credential — and it is why the timeout is short
// and reports `unknown` rather than long and reporting success. Waiting
// longer would buy nothing: an unchanged pass is indistinguishable from a
// stale one however long you stare at it.
//
// Variables so a test can exercise the timeout without spending it.
var (
	seamRecheckWait     = 90 * time.Second
	seamRecheckInterval = 5 * time.Second
)

// waitForSeamVerdict waits for a verdict kagent reached AFTER `since`, and
// says plainly when it does not get one.
//
// The timeout is not a failure. It is `unknown`: the credential may be
// perfectly good and kagent may simply not have looked yet. Reporting it as
// accepted would be the lie this exists to stop, and reporting it as rejected
// would send an operator to fix something that may be fine.
func (a *App) waitForSeamVerdict(namespace, server string, since time.Time) (seamVerdict, error) {
	return waitForVerdict(
		func() (seamVerdict, error) { return a.readSeamVerdict(namespace, server, since) },
		a.timeNow,
		func() {
			// Say what is being waited for, once. Silence here is what made
			// the old behaviour read as an instant pass.
			a.notef("Waiting for kagent to check the %s seam against the credential just written "+
				"(a mounted Secret is not refreshed the instant it is written)...", server)
			// kagent does not retry a cached verdict on its own; ask it to
			// look. Nothing else about the seam changes.
			_ = a.kubectlRun("-n", namespace, "annotate", "remotemcpserver", server,
				"kaimahi.dev/rechecked-at="+a.timeNow().UTC().Format(time.RFC3339), "--overwrite")
		},
		time.Sleep)
}

// waitForVerdict is the loop, separated from the cluster so it can be tested
// without one.
func waitForVerdict(read func() (seamVerdict, error), now func() time.Time,
	recheck func(), sleep func(time.Duration)) (seamVerdict, error) {
	deadline := now().Add(seamRecheckWait)
	asked := false
	var last seamVerdict
	var lastErr error
	for {
		v, err := read()
		if err != nil {
			// A read that fails is not a reason to fail the whole command
			// here: the credential and the allowlist are already written, and
			// a single API blip during the wait would throw that away. Keep
			// trying until the deadline. A persistent fault — RBAC, a seam
			// that is not there — still surfaces, because it will still be
			// failing when the deadline arrives.
			lastErr = err
		} else {
			lastErr = nil
			last = v
			if v.State != verdictUnknown {
				return v, nil
			}
		}
		if !asked {
			recheck()
			asked = true
		}
		if !now().Before(deadline) {
			if lastErr != nil {
				return seamVerdict{}, lastErr
			}
			last.Reason = fmt.Sprintf("kagent did not change its verdict in %s. A condition records when a "+
				"verdict CHANGED, not when it was last checked, so an unchanged pass cannot be told apart "+
				"from a stale one — whether the credential that is there now works is not known. It is not "+
				"known to be broken either: a credential kagent rejects flips this within about half a "+
				"minute. Look again with: kmx status", seamRecheckWait)
			return last, nil
		}
		sleep(seamRecheckInterval)
	}
}
