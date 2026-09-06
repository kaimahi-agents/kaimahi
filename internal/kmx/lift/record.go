// Package lift holds the parts of the managed-cluster path that must be
// right whether or not a cloud is reachable: what the lift is about to do,
// what it actually created, and — the one that can cost somebody else money
// — what it is allowed to delete afterwards.
//
// The split matters. Everything here is pure: it names resources, records
// them, and decides removal from an answer someone else obtained. Nothing in
// this package talks to Azure, so all of it is tested without a subscription,
// which is the only way the teardown rules get exercised at all — the real
// ones run perhaps twice a lane, by hand, against live infrastructure.
package lift

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Branch is who owns the cluster, and it decides what teardown may touch.
type Branch string

const (
	// Created: the cluster, its registry and its resource group were all made
	// by this path, and all of it comes back down together.
	Created Branch = "created"
	// BringYourOwn: the cluster existed before we arrived. The cluster and its
	// resource group are never deleted and never adopted; only the resources
	// this path added come out, and only by the id recorded when they went in.
	BringYourOwn Branch = "byo"
)

// Resource is one thing the lift created in somebody's subscription.
//
// Both Name and ID are recorded, and they do different jobs. The name is what
// a human reads. The ID is what teardown deletes by — and the two are kept
// separately so that a mismatch between them is detectable rather than
// invisible. Deleting by name on a subscription we do not own is how a demo
// removes a stranger's production monitoring: names collide, ids do not.
type Resource struct {
	// Kind is the human label ("Azure Monitor workspace"), not an ARM type.
	Kind string `json:"kind"`
	Name string `json:"name"`
	// ID is the full ARM resource id, captured from the create response.
	ID string `json:"id"`
	// Billing says what this keeps charging the owner for after the demo
	// ends, in the owner's words. Empty means it stops costing when it stops
	// being written to. This is reported at teardown for anything left
	// behind, because an unproven resource is an unproven bill.
	Billing string `json:"billing,omitempty"`
	// InResourceGroup is true when the resource lives inside the resource
	// group the lift itself created. `az group exists` returning false proves
	// those are gone; it says nothing at all about the others, which is why
	// the distinction is recorded rather than assumed.
	InResourceGroup bool `json:"in_resource_group"`
}

// Record is the complete account of one lift: where it ran and what it made.
//
// It is written before the first create and updated after each one, not at
// the end. A run that dies halfway has still put resources in someone's
// subscription, and a record written only on success would be a record of
// exactly the runs that did not need one.
type Record struct {
	RunID  string `json:"run_id"`
	Branch Branch `json:"branch"`
	// Subscription is recorded so teardown can refuse to act when the CLI is
	// pointed somewhere else entirely. Never printed, never committed.
	Subscription  string     `json:"subscription"`
	ResourceGroup string     `json:"resource_group"`
	Cluster       string     `json:"cluster"`
	Created       []Resource `json:"created"`
	// Before is what the cluster looked like when this run arrived. Teardown
	// consults it so it undoes only what this run did.
	Before Pre `json:"before"`
}

// Pre is the state a run found and must not mistake for its own work.
//
// Without it, teardown on a cluster we do not own is destructive in a way that
// is easy to miss: an operator who already had Container Insights running gets
// it switched off by a `kmx lift down` that was only ever meant to remove what
// the lift added. Turning somebody's monitoring off is not as bad as deleting
// their workspace, but it is the same mistake — acting on a resource because
// we touched it rather than because we made it.
//
// The two cluster-side objects are here for the same reason, and specifically
// so that ownership is never inferred from CONTENT: a ConfigMap that happens
// to hold only our scrape job today might have been created by the operator
// yesterday, and "it looks like ours" is not "we made it".
type Pre struct {
	// Recorded reports whether this struct was filled in at all. A record
	// written before this existed has every field false, which is
	// indistinguishable from "nothing was there" — and that reading would
	// have teardown disable an add-on it did not enable. False means "not
	// established", and teardown then leaves things alone.
	Recorded bool `json:"recorded"`

	MetricsAddonEnabled bool `json:"metrics_addon_enabled"`
	LogsAddonEnabled    bool `json:"logs_addon_enabled"`

	ScraperPolicyExisted bool `json:"scraper_policy_existed"`
	ScrapeConfigExisted  bool `json:"scrape_config_existed"`
}

// WeEnabledMetrics reports whether THIS run turned the metrics add-on on, so
// teardown knows whether turning it off is undoing its own change.
//
// Unestablished state answers "no" on purpose: leaving an add-on enabled costs
// the owner ingestion charges they can see and stop, while disabling one they
// were relying on breaks monitoring they may not notice is gone.
func (p Pre) WeEnabledMetrics() bool { return p.Recorded && !p.MetricsAddonEnabled }
func (p Pre) WeEnabledLogs() bool    { return p.Recorded && !p.LogsAddonEnabled }

// WeCreatedScraperPolicy and WeCreatedScrapeConfig answer the same question
// for the two cluster-side objects, and are the ONLY thing that authorises
// deleting them. Their contents are not evidence of who made them.
func (p Pre) WeCreatedScraperPolicy() bool { return p.Recorded && !p.ScraperPolicyExisted }
func (p Pre) WeCreatedScrapeConfig() bool  { return p.Recorded && !p.ScrapeConfigExisted }

var runIDShape = regexp.MustCompile(`^[a-z0-9]{8}$`)

// Names are Azure-constrained: an Azure Monitor workspace and a Log Analytics
// workspace both take 4-63 characters of alphanumerics and hyphens, starting
// and ending alphanumeric. The prefix keeps them recognisable to a human
// scanning a resource list; the run id keeps two runs in one subscription
// from colliding, which on a bring-your-own subscription is the difference
// between "delete the thing I made" and "delete the thing that was here".
const namePrefix = "kaimahi"

// MetricsWorkspaceName and LogsWorkspaceName are run-scoped by construction.
// There is no way to ask for a fixed name: a fixed name is what makes two
// runs, or a run and a stranger's resource, indistinguishable.
func MetricsWorkspaceName(runID string) string { return namePrefix + "-metrics-" + runID }
func LogsWorkspaceName(runID string) string    { return namePrefix + "-logs-" + runID }
func WorkbookName(runID string) string         { return namePrefix + "-workbook-" + runID }

// NewRecord starts an account of a run. It validates the run id rather than
// trusting it, because the id ends up in every resource name.
func NewRecord(runID string, branch Branch, subscription, resourceGroup, cluster string) (*Record, error) {
	if !runIDShape.MatchString(runID) {
		return nil, fmt.Errorf("lift: run id %q is not 8 lowercase alphanumerics — it names every resource this run creates", runID)
	}
	switch branch {
	case Created, BringYourOwn:
	default:
		return nil, fmt.Errorf("lift: unknown branch %q", branch)
	}
	for name, v := range map[string]string{"subscription": subscription, "resource group": resourceGroup, "cluster": cluster} {
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("lift: %s is required to record a run", name)
		}
	}
	return &Record{RunID: runID, Branch: branch, Subscription: subscription, ResourceGroup: resourceGroup, Cluster: cluster}, nil
}

// Add records a resource that now exists. An empty id is refused: a resource
// recorded without one can never be removed by this package, and recording it
// anyway would produce an account that looks complete and is not.
func (r *Record) Add(res Resource) error {
	if strings.TrimSpace(res.ID) == "" {
		return fmt.Errorf("lift: refusing to record %q (%s) with no resource id — teardown deletes by id, so an unrecorded id is a resource nobody can remove", res.Name, res.Kind)
	}
	if strings.TrimSpace(res.Name) == "" || strings.TrimSpace(res.Kind) == "" {
		return errors.New("lift: a recorded resource needs a kind and a name")
	}
	for _, existing := range r.Created {
		if existing.ID == res.ID {
			return nil // idempotent: a re-run that reused a resource records it once
		}
	}
	r.Created = append(r.Created, res)
	return nil
}

// Outside returns the resources a resource-group check cannot vouch for,
// which are exactly the ones the write-up has to name individually.
// InGroup reports whether an ARM resource id names something inside the given
// resource group, compared the way ARM compares: case-insensitively.
//
// Teardown on the created branch leans on this. A resource-group deletion
// accounts for what was INSIDE it and nothing else, so a resource assumed to
// be in the group but actually somewhere else — the monitoring add-ons put
// some of theirs in the cluster's managed node group — would be reported as
// covered by a check that never looked at it.
func InGroup(resourceID, group string) bool {
	needle := "/resourcegroups/" + strings.ToLower(strings.TrimSpace(group)) + "/"
	return strings.Contains(strings.ToLower(resourceID)+"/", needle)
}

func (r *Record) Outside() []Resource {
	var out []Resource
	for _, res := range r.Created {
		if !res.InResourceGroup {
			out = append(out, res)
		}
	}
	return out
}

func (r *Record) Write(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func ReadRecord(rd io.Reader) (*Record, error) {
	var r Record
	if err := json.NewDecoder(rd).Decode(&r); err != nil {
		return nil, fmt.Errorf("lift: cannot read the run record: %w", err)
	}
	if !runIDShape.MatchString(r.RunID) {
		return nil, fmt.Errorf("lift: run record carries an unusable run id %q", r.RunID)
	}
	switch r.Branch {
	case Created, BringYourOwn:
	default:
		return nil, fmt.Errorf("lift: run record carries an unknown branch %q", r.Branch)
	}
	return &r, nil
}

// Existence is the answer to "is this resource still there?", and it has
// three values rather than two on purpose.
//
// The two-valued version is the worst bug available here. `az` prints nothing
// on stdout and errors on stderr when the call itself fails — an expired
// token, a throttled subscription — and code that reads "no answer" as "not
// there" will report a teardown that never happened, leaving a stranger's
// subscription quietly billing. So a call that could not be made is its own
// answer, and it is a refusal.
type Existence int

const (
	// Unusable: the question could not be asked. Never treated as absence.
	Unusable Existence = iota
	// Absent: Azure answered, and the resource is not there.
	Absent
	// Present: Azure answered, and the resource is there.
	Present
)

// Removal is what teardown decided to do about one recorded resource.
type Removal struct {
	Resource Resource
	// Delete is true only when the recorded id was re-resolved and still
	// names the same resource.
	Delete bool
	// Reason explains a Delete of false, in terms an operator can act on.
	Reason string
}

// PlanRemoval decides, for one recorded resource, whether teardown may delete
// it — given what the subscription just said about it.
//
// resolvedID is the id Azure returned when asked about the resource, and it
// is compared against the id recorded at creation. The comparison is the
// whole point of the function: a name can be re-used, a resource can be
// deleted and a different one created in its place, and on a subscription we
// do not own either may have happened between the lift and the teardown. Only
// an exact id match authorises a delete. Everything else is left behind and
// said out loud, because leaving a resource costs money and deleting the
// wrong one costs somebody their monitoring.
func PlanRemoval(res Resource, state Existence, resolvedID string) Removal {
	switch state {
	case Absent:
		return Removal{Resource: res, Reason: "already gone — nothing to delete"}
	case Present:
		if !sameResourceID(res.ID, resolvedID) {
			return Removal{Resource: res, Reason: fmt.Sprintf(
				"the name %q now resolves to a DIFFERENT resource than the one this run created — refusing to delete something that is not ours. Remove it by hand only if you are certain: %s", res.Name, res.ID)}
		}
		return Removal{Resource: res, Delete: true}
	default:
		return Removal{Resource: res, Reason: fmt.Sprintf(
			"could not re-resolve the recorded id — NOT deleting on a guess, and NOT claiming it is gone. It may still be billing. Check by hand: az resource show --ids %s", res.ID)}
	}
}

// SameResourceID is sameResourceID for callers outside this package, which
// need the same comparison when they ask "is the workspace this cluster
// already sends to the one we just made?".
func SameResourceID(a, b string) bool { return sameResourceID(a, b) }

// sameResourceID compares two ARM resource ids the way ARM treats them:
// case-insensitively, ignoring a trailing slash. Anything else — a differing
// subscription, group, provider or name — is a different resource.
func sameResourceID(recorded, resolved string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/")) }
	if norm(recorded) == "" || norm(resolved) == "" {
		return false
	}
	return norm(recorded) == norm(resolved)
}

// LeftBehind summarises the removals that did not happen, newest problem
// first is not useful here — they are sorted by name so two runs of the same
// teardown print the same thing.
func LeftBehind(removals []Removal) []Removal {
	var out []Removal
	for _, rm := range removals {
		if !rm.Delete && rm.Reason != "already gone — nothing to delete" {
			out = append(out, rm)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource.Name < out[j].Resource.Name })
	return out
}
