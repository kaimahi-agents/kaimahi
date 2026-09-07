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

// seamBaseline is what stood BEFORE the caller changed anything, and it is
// what makes the freshness question answerable without trusting two clocks to
// agree.
type seamBaseline struct {
	// Since is the caller's own clock at the moment it wrote the credential.
	// The zero time means the caller changed nothing and is asking only what
	// kagent currently thinks — which is what `kmx status` asks.
	Since time.Time
	// At is the verdict that stood before the write, in the API SERVER's
	// clock. Zero means there was no verdict at all, which is a complete
	// answer: any verdict that appears is about the credential we wrote.
	At time.Time
	// Known is false when the baseline could not be read. A change cannot be
	// proven against a baseline nobody has, so that is `unknown` rather than
	// an assumption in either direction.
	Known bool
}

// classifySeamVerdict reads one `Accepted` condition.
//
// Two things have to hold before a verdict counts as an answer about the
// credential the caller just wrote, and they fail in different ways on
// purpose:
//
//  1. **It moved.** The condition's time must be strictly later than the one
//     that stood before the write. This is the load-bearing check, because it
//     compares the API server's clock only with ITSELF: whatever kmx's clock
//     says, a verdict kagent has not revisited carries the same timestamp it
//     carried before, and no skew can make an unchanged verdict look like a
//     new one.
//  2. **It is not older than the write.** Kept as a second condition rather
//     than replaced by the first. It costs nothing when the clocks agree, and
//     where they do not it can only push the answer towards `unknown`, which
//     is the safe direction.
//
// The first check is what closes a genuine fail-open hole. Comparing the
// server's timestamp against kmx's wall clock alone is safe when kmx runs
// AHEAD — fresh verdicts look stale and are reported `unknown` — but when kmx
// runs BEHIND, a stale verdict looks fresh and is reported as an answer,
// which is the failure this whole file exists to prevent.
//
// Padding the wall-clock comparison by a skew allowance was the obvious
// alternative and is worse: a verdict arriving inside the allowance can never
// be newer than a padded `Since`, so a REJECTION — measured at 14 seconds on
// a live cluster, and the case most worth catching — would be reported
// `unknown` instead. The causal check loses nothing, because a rejection
// always moves the condition.
//
// What neither check can do is confirm a PASS: a good credential leaves the
// verdict where it was, so it is indistinguishable from a stale one. That
// asymmetry is the right way round for a fail-closed posture and is why the
// wait around this reports `unknown` rather than success.
func classifySeamVerdict(c *serverCondition, generation, observed int64, base seamBaseline) seamVerdict {
	asking := !base.Since.IsZero()
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
	// A change cannot be proven against a baseline nobody has.
	if asking && !base.Known {
		return seamVerdict{State: verdictUnknown, At: at, Message: c.Message,
			Reason: "what this seam said before the credential was written could not be read, so there is no " +
				"way to tell whether kagent has looked at it since"}
	}
	// A verdict with no time cannot be told apart from one reached before
	// the caller's change, so it cannot answer a question about that change.
	if asking && at.IsZero() {
		return seamVerdict{State: verdictUnknown, Message: c.Message,
			Reason: "kagent's verdict carries no timestamp, so it cannot be told apart from one reached " +
				"before the credential was written"}
	}
	// The causal check: the verdict has to have MOVED. Server clock against
	// server clock, so no disagreement between kmx's clock and the cluster's
	// can turn a verdict kagent never revisited into a fresh one.
	if asking && !at.After(base.At) {
		return seamVerdict{State: verdictUnknown, At: at, Message: c.Message,
			Reason: "kagent has not changed its verdict on this seam since before the credential was " +
				"written — a mounted Secret is not updated the instant it is written, so this is the same " +
				"answer it gave about the credential that was there before"}
	}
	if asking && at.Before(base.Since) {
		return seamVerdict{State: verdictUnknown, At: at, Message: c.Message,
			Reason: fmt.Sprintf("kagent last checked this seam %s before the credential was written, and has not "+
				"looked since — a mounted Secret is not updated the instant it is written, so this says nothing "+
				"about the credential that is there now", age(base.Since, at))}
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

// seamVerdictBaseline records what a seam said BEFORE the caller changes
// anything, so a later verdict can be shown to have moved.
//
// Call it before writing the credential. A seam that is not there yet is a
// complete answer and not a failure — there is no prior verdict, so any
// verdict that appears is about the credential about to be written. Only a
// read that genuinely failed leaves the baseline unknown, and that is carried
// rather than guessed at.
func (a *App) seamVerdictBaseline(namespace, server string, since time.Time) seamBaseline {
	raw, err := a.kubectlCapture("-n", namespace, "get", "remotemcpserver", server, "-o", "json")
	if err != nil {
		if isNotFound(err) {
			return seamBaseline{Since: since, Known: true}
		}
		a.notef("could not read what the %s seam said before this change (%v); its verdict afterwards "+
			"will be reported as unknown rather than assumed", server, err)
		return seamBaseline{Since: since}
	}
	at, err := parseSeamCondition(raw)
	if err != nil {
		a.notef("could not read what the %s seam said before this change (%v); its verdict afterwards "+
			"will be reported as unknown rather than assumed", server, err)
		return seamBaseline{Since: since}
	}
	return seamBaseline{Since: since, At: at, Known: true}
}

// parseSeamCondition returns the Accepted condition's transition time.
//
// The zero time is returned for exactly one case: there is no Accepted
// condition at all. That is a complete answer — nothing has been decided about
// this seam, so whatever is decided next is about the credential we are about
// to write.
//
// A condition that EXISTS but carries a time nobody can read is the opposite,
// and is an error. Returning the zero time there would report "I could not
// understand what the seam said" as "the seam said nothing", and the caller
// reads a zero baseline as "no prior verdict" — so the next verdict, including
// the stale one already sitting there, would count as a change. That is the
// same false absence the rest of this file exists to refuse, and it fails
// open.
func parseSeamCondition(raw string) (time.Time, error) {
	var doc struct {
		Status struct {
			Conditions []serverCondition `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return time.Time{}, err
	}
	c := pickCondition(doc.Status.Conditions, "Accepted")
	if c == nil {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(c.LastTransitionTime))
	if err != nil {
		return time.Time{}, fmt.Errorf("the Accepted condition carries a time that cannot be read (%q): %w",
			c.LastTransitionTime, err)
	}
	return at, nil
}

// readSeamVerdict asks the cluster what kagent last decided about one
// RemoteMCPServer, and when.
func (a *App) readSeamVerdict(namespace, server string, base seamBaseline) (seamVerdict, error) {
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
		doc.Metadata.Generation, doc.Status.ObservedGeneration, base), nil
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
func (a *App) waitForSeamVerdict(namespace, server string, base seamBaseline) (seamVerdict, error) {
	return waitForVerdict(
		func() (seamVerdict, error) { return a.readSeamVerdict(namespace, server, base) },
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
