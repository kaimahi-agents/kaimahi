package lift

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Options are everything the lift needs to know before it touches a
// subscription. They are validated as a set rather than one at a time,
// because which of them are required depends on which branch is running, and
// a missing one on a cloud target should be a refusal at the start rather
// than a failure eight minutes in with a resource group already created.
type Options struct {
	// BringYourOwn selects the branch. It is an explicit flag and never
	// inferred from whether a cluster happens to exist: inferring it would
	// mean a typo'd cluster name silently switches which of two very
	// different teardown rules applies.
	BringYourOwn bool

	ResourceGroup string
	Registry      string
	Cluster       string
	Location      string
	NodeSize      string
	NodeCount     int
	NetworkPolicy string
	// NetworkPolicySet distinguishes an unset engine from one set to the
	// empty string. The distinction is load-bearing: unset takes the default
	// that enforces, while an explicit empty value is the AKS default that
	// does not, and it must reach its own refusal rather than being quietly
	// swapped for something that works. The shell script this path carries
	// draws the same distinction, with `-` rather than `:-`.
	NetworkPolicySet bool

	// Observability wires Azure-managed monitoring. On by default: an agent
	// that arrives on a managed cluster with nothing to look at is the gap
	// this path exists to close.
	Observability bool

	// Step runs one phase of the lift. The phases are re-runnable, so a
	// failure is resumed by naming the phase that failed rather than by
	// unpicking the ones before it.
	Step string

	// Payload selects WHAT lands on the cluster this command provisions.
	//
	// There is deliberately no default. `lift` bills money and installs a
	// platform, and the two payloads are different products: one lands Orka,
	// the other lands the legacy kagent runtime and its demo agents. A
	// default would mean somebody's existing script silently changed which
	// platform it deploys the day the project's direction moved, which is
	// the one outcome worth a one-word break instead.
	Payload string

	// Plan prints what would happen, where, and stops. It creates nothing
	// and reads nothing on the cluster or in the subscription — only an Azure
	// CLI preflight and `az account show`, which fill in the two lines that say
	// where this would land.
	Plan bool
}

// Payloads are what a lift can land. Named rather than inferred, and
// validated against this list, so a typo is a refusal instead of a surprise.
const (
	PayloadOrka   = "orka"
	PayloadKagent = "kagent"
)

// Payloads lists them for the flag's help and its completion.
var Payloads = []string{PayloadOrka, PayloadKagent}

// Steps are the phases of the lift, in order. Each is re-runnable on its own
// and each is idempotent, which is what makes a failed lift resumable instead
// of a cleanup problem.
//
// The two payloads share every phase that is about the CLUSTER — provisioning
// it, proving its boundary, the plane that meters a model seam, monitoring and
// verification. They differ only in what is installed to run agents, which is
// the whole point of the distinction.
var Steps = stepsFor(PayloadKagent)

func stepsFor(payload string) []string {
	if payload == PayloadOrka {
		return []string{"cluster", "boundary", "credential", "plane", "orka", "observability", "verify"}
	}
	return []string{"cluster", "boundary", "kagent", "credential", "plane", "agents", "observability", "verify"}
}

// StepsForPayload exposes the phase list for a payload, for the planner and
// for anything that needs to name the phases before Options exist.
func StepsForPayload(payload string) []string { return stepsFor(payload) }

// AllSteps is every phase either payload has, in order, each once. Shell
// completion cannot know which payload is being typed, and offering only one
// payload's phases would hide valid answers for the other.
func AllSteps() []string {
	var out []string
	seen := map[string]bool{}
	for _, payload := range Payloads {
		for _, step := range stepsFor(payload) {
			if !seen[step] {
				seen[step] = true
				out = append(out, step)
			}
		}
	}
	return out
}

// ValidPayload refuses anything that is not one of the two, and refuses the
// empty string with the reasoning rather than a bare usage line: an operator
// who typed `kmx lift` before this flag existed needs to know that the answer
// changed, not merely that a flag is missing.
func ValidPayload(payload string) error {
	switch payload {
	case PayloadOrka, PayloadKagent:
		return nil
	case "":
		return errors.New("kmx lift: --payload is required, and has no default.\n" +
			"  --payload orka     Orka, the platform this project now gets agents onto\n" +
			"  --payload kagent   the legacy kagent runtime and its two demo agents\n" +
			"  This command bills money and installs a platform. A default would mean an\n" +
			"  existing script quietly changed which one it deploys, so the choice is yours\n" +
			"  to state rather than ours to assume.")
	default:
		return fmt.Errorf("kmx lift: unknown --payload %q — one of: %s", payload, strings.Join(Payloads, ", "))
	}
}

// PurposeOf is what a phase is for, in the words the banner uses.
//
// One phase means different things to the two payloads, and saying the wrong
// one is worse than saying nothing: a plan that promises "the agent answers"
// on a payload that installs no agent has told the operator it will prove
// something it cannot.
func PurposeOf(step, payload string) string {
	if step == "verify" && payload == PayloadOrka {
		return "Orka is installed and ready; no model call is made, because the Provider is yours"
	}
	return StepPurpose[step]
}

// StepPurpose is what each phase is for, in the words the banner uses.
var StepPurpose = map[string]string{
	"cluster":       "resource group, private registry and an AKS cluster with a policy engine",
	"boundary":      "the network boundary and the ledger, then PROVE the boundary is enforced",
	"kagent":        "the agent runtime",
	"orka":          "Orka at the pinned version; the model Provider stays yours to create",
	"credential":    "check the model credential the managed path needs (captured by you, not by kmx)",
	"plane":         "build the model-traffic bridge in the registry and deploy it",
	"agents":        "the same agents you ran locally, governed from the start",
	"observability": "Azure Monitor workspace, Container Insights, the scrape job and a workbook",
	"verify":        "the agent answers, and the dashboard has its traffic",
}

var (
	// Azure resource groups: 1-90 chars, alphanumerics, underscore,
	// parentheses, hyphen, period (no trailing period), plus unicode letters.
	// Checked loosely here — Azure is the authority — but checked at all, so
	// an obviously wrong value fails before a create is attempted.
	resourceGroupShape = regexp.MustCompile(`^[A-Za-z0-9_().\-]{1,90}$`)
	registryShape      = regexp.MustCompile(`^[a-zA-Z0-9]{5,50}$`)
	clusterShape       = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,61}[a-zA-Z0-9]$`)
	locationShape      = regexp.MustCompile(`^[a-z0-9]{3,40}$`)
)

// Engines that actually enforce NetworkPolicy on AKS. A cluster created
// without one applies every policy and enforces none, which reads as
// protection and is worse than having no policy at all.
var enforcingEngines = map[string]bool{"cilium": true, "azure": true, "calico": true}

// Validate refuses an unusable set of options, and returns every problem at
// once rather than the first — a cloud target is slow enough that finding out
// about three missing parameters one run at a time is its own cost.
func (o Options) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	// The payload joins the other problems rather than short-circuiting: this
	// function's contract is that an operator learns everything wrong with
	// what they asked for in one reading, not one round-trip per mistake.
	payloadErr := ValidPayload(o.Payload)
	if payloadErr != nil {
		add("%s", payloadErr.Error())
	}

	// The phase list depends on the payload, so a step can only be judged
	// once the payload is known to be one. Judging it against the wrong list
	// would refuse a phase that the lift they asked for actually has.
	if payloadErr == nil && o.Step != "" && !validStep(o.Step, o.Payload) {
		add("--step %q is not a phase of the %s payload. Phases, in order: %s",
			o.Step, o.Payload, strings.Join(stepsFor(o.Payload), ", "))
	}

	if strings.TrimSpace(o.ResourceGroup) == "" {
		add("--resource-group is required: it names where this runs, and on a managed cluster nothing is guessed")
	} else if !resourceGroupShape.MatchString(o.ResourceGroup) {
		add("--resource-group %q is not a usable Azure resource group name", o.ResourceGroup)
	}

	if strings.TrimSpace(o.Cluster) == "" {
		add("--cluster is required: it is the cluster name and the kube-context name")
	} else if !clusterShape.MatchString(o.Cluster) {
		add("--cluster %q is not a usable AKS cluster name", o.Cluster)
	}

	// The registry is required on BOTH branches, for different reasons. On a
	// cluster this path creates, it is created too. On yours, it must already
	// exist and your cluster must already be able to pull from it — the lift
	// checks that and refuses, rather than granting itself the role, because
	// granting AcrPull on your subscription is a change to your cluster's
	// identity that a demo has no business making silently.
	if strings.TrimSpace(o.Registry) == "" {
		if o.BringYourOwn {
			add("--registry is required: the plane's image is built in a private registry your cluster can pull from. Nothing is published, so there is no public image to fall back on")
		} else {
			add("--registry is required: a globally-unique name for the private registry this creates")
		}
	} else if !registryShape.MatchString(o.Registry) {
		add("--registry %q must be 5-50 characters, alphanumeric only (an Azure container registry name is globally unique)", o.Registry)
	}

	if o.BringYourOwn {
		// `--step cluster` names the one phase this branch does not have.
		// steps() drops it from a full run, but an explicit --step bypasses
		// that and would run the provisioning script — creating a cluster on
		// the branch whose entire contract is that it creates none, and whose
		// teardown would then refuse to remove it.
		if o.Step == "cluster" {
			add("--step cluster cannot be used with --byo: that phase CREATES a cluster, and this branch never creates one. The phases here are: %s",
				strings.Join(stepsFor(o.Payload)[1:], ", "))
		}
		// Nothing about the cluster's shape is ours to choose on someone
		// else's cluster, so accepting these would be accepting parameters
		// that cannot be honoured.
		for flag, set := range map[string]bool{
			"--location":       strings.TrimSpace(o.Location) != "",
			"--node-size":      strings.TrimSpace(o.NodeSize) != "",
			"--node-count":     o.NodeCount != 0,
			"--network-policy": strings.TrimSpace(o.NetworkPolicy) != "",
		} {
			if set {
				add("%s cannot be used with --byo: your cluster already exists and this path never reshapes it", flag)
			}
		}
	} else {
		if l := strings.TrimSpace(o.Location); l != "" && !locationShape.MatchString(l) {
			add("--location %q is not a usable Azure region name", o.Location)
		}
		if o.NodeCount < 0 {
			add("--node-count cannot be negative")
		}
		// An explicitly empty value must reach this refusal with its message
		// rather than being swapped for the default: no engine is the AKS
		// default and is exactly the case worth refusing.
		if np := o.NetworkPolicy; (np != "" || o.NetworkPolicySet) && !enforcingEngines[np] {
			add("--network-policy %q is not a policy engine. Accepted: cilium (default), azure, calico. Without an engine AKS ignores NetworkPolicy and the plane's boundary is present but inert", np)
		}
	}

	if len(problems) == 0 {
		return nil
	}
	noun := "problem"
	if len(problems) > 1 {
		noun = "problems"
	}
	return fmt.Errorf("kmx lift: %d %s with what was asked for:\n\n- %s", len(problems), noun, strings.Join(problems, "\n- "))
}

// ValidateForTeardown checks the flags that say WHICH lift is being removed.
//
// It is separate from Validate because teardown needs less and must not
// demand more: a registry is required to build a plane image and irrelevant
// to deleting one, and refusing teardown over a missing --registry would be
// standing between an operator and a resource that is billing. What it does
// insist on is the pair that identifies the run, because without them there
// is nothing to look the record up by — and going looking by name on a
// subscription that may not be ours is the one thing teardown must never do.
func (o Options) ValidateForTeardown() error {
	var problems []string
	if strings.TrimSpace(o.ResourceGroup) == "" {
		problems = append(problems, "--resource-group is required: it is half of what identifies the run whose resources are being removed")
	}
	if strings.TrimSpace(o.Cluster) == "" {
		problems = append(problems, "--cluster is required: it is the other half")
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("kmx lift down: %s\n\n  Without both, there is no record to read, and this refuses to go looking\n  by name — on a subscription that is not ours, a name can belong to\n  somebody else.", strings.Join(problems, "\n  "))
}

func validStep(step, payload string) bool {
	for _, s := range stepsFor(payload) {
		if s == step {
			return true
		}
	}
	return false
}

// GroupName, ClusterName and RegistryName expose the three names that say
// which lift this is, for callers that need them without the rest.
func (o Options) GroupName() string    { return o.ResourceGroup }
func (o Options) ClusterName() string  { return o.Cluster }
func (o Options) RegistryName() string { return o.Registry }

// Branch reports which set of teardown rules this run is under.
func (o Options) Branch() Branch {
	if o.BringYourOwn {
		return BringYourOwn
	}
	return Created
}

// Banner is what the lift prints before it does anything, and it is not
// optional: the target is a cloud subscription, and an operator is entitled
// to read what is about to be created and where before it is.
//
// It deliberately does not print the subscription id. The id is an identifier
// this project keeps out of terminals and transcripts, and the subscription
// name plus the account the CLI is logged in as is what a human actually
// checks against.
func (o Options) Banner(account, subscriptionName string) string {
	var b strings.Builder
	b.WriteString("----------------------------------------------------------------\n")
	if o.BringYourOwn {
		b.WriteString("  kmx aks up — onto a cluster YOU created\n")
	} else {
		b.WriteString("  kmx aks up — creating a cluster and everything around it\n")
	}
	fmt.Fprintf(&b, "  subscription:    %s\n", subscriptionName)
	fmt.Fprintf(&b, "  signed in as:    %s\n", account)
	fmt.Fprintf(&b, "  resource group:  %s\n", o.ResourceGroup)
	fmt.Fprintf(&b, "  cluster:         %s\n", o.Cluster)
	fmt.Fprintf(&b, "  registry:        %s\n", o.Registry)
	if !o.BringYourOwn {
		fmt.Fprintf(&b, "  region:          %s\n", o.Location)
		fmt.Fprintf(&b, "  nodes:           %d x %s\n", o.NodeCount, o.NodeSize)
		fmt.Fprintf(&b, "  policy engine:   %s\n", o.NetworkPolicy)
	}
	b.WriteString("\n  will do, in order:\n")
	for _, step := range o.steps() {
		fmt.Fprintf(&b, "    %-14s %s\n", step, PurposeOf(step, o.Payload))
	}
	b.WriteString("\n")
	if o.BringYourOwn {
		b.WriteString("  YOUR CLUSTER AND RESOURCE GROUP ARE NEVER DELETED and never\n")
		b.WriteString("  adopted. `kmx aks down` removes only the monitoring resources\n")
		b.WriteString("  this run creates, and only by the id it recorded when it made\n")
		b.WriteString("  them. Anything it cannot prove is its own is left alone and\n")
		b.WriteString("  named, so you can remove it yourself.\n")
	} else {
		b.WriteString("  Everything above is created inside that ONE resource group and\n")
		b.WriteString("  comes back down with it: `kmx aks down`. It bills until it does.\n")
	}
	b.WriteString("----------------------------------------------------------------\n")
	return b.String()
}

// steps is the phase list this invocation will actually run.
func (o Options) steps() []string {
	if o.Step != "" {
		return []string{o.Step}
	}
	var out []string
	for _, s := range stepsFor(o.Payload) {
		if s == "cluster" && o.BringYourOwn {
			continue // the cluster is theirs; nothing to create
		}
		if s == "observability" && !o.Observability {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Steps exposes the phase list for the caller that runs them.
func (o Options) StepsToRun() []string { return o.steps() }

// PolicyEngineVerdict turns what the control plane said about a cluster's
// NetworkPolicy engine into a decision, for a cluster this path did not
// create and therefore cannot vouch for.
//
// This is the cheap gate, and it runs before anything is installed. It cannot
// prove enforcement — enforcement is a property of the CNI that the API
// server does not vouch for — but it catches the case that actually happens:
// a cluster created with no engine at all, where every policy applies and
// none is enforced. The expensive gate is the existing negative proof, which
// runs against a live boundary once there is one.
func PolicyEngineVerdict(engine string, readable bool) error {
	if !readable {
		return errors.New("kmx lift: could not read this cluster's NetworkPolicy engine — refusing to install a model-traffic bridge whose boundary might be decorative. Not claiming it enforces, and not claiming it does not")
	}
	switch e := strings.TrimSpace(strings.ToLower(engine)); {
	case e == "" || e == "none":
		return errors.New(`kmx lift: this cluster has NO NetworkPolicy engine.

  AKS clusters created without one ACCEPT every NetworkPolicy and enforce
  none of them. The plane's boundary would be present and inert, which reads
  as protection and is worse than having none — you would believe the model
  seam and its database were isolated when they are not.

  Refusing to install. To fix it you must re-create the cluster with an
  engine (az aks create --network-policy cilium ...); an existing cluster
  cannot be migrated without reimaging every node pool`)
	case enforcingEngines[e]:
		return nil
	default:
		return fmt.Errorf("kmx lift: this cluster reports NetworkPolicy engine %q, which this path does not recognise as one that enforces. Refusing rather than assuming", engine)
	}
}
