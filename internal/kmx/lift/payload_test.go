package lift

// The payload split, after the legacy runtime went: what a lift may still
// land, and what a record of one that landed the retired payload is allowed
// to do.

import (
	"strings"
	"testing"
)

// `lift` bills money and installs a platform. A default would mean somebody's
// existing script quietly changed which one it deploys the day the project's
// direction moved — so the refusal is the feature. It survives the retirement
// of the second payload: the reason was never "there are two".
func TestAPayloadIsRequiredAndNeverDefaulted(t *testing.T) {
	o := created()
	o.Payload = ""
	err := o.Validate()
	if err == nil {
		t.Fatal("a lift with no payload was accepted")
	}
	for _, want := range []string{"--payload is required", "orka", "bills money"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestAnUnknownPayloadIsRefusedByName(t *testing.T) {
	o := created()
	o.Payload = "orca"
	err := o.Validate()
	if err == nil || !strings.Contains(err.Error(), `unknown --payload "orca"`) {
		t.Fatalf("a misspelled payload was not named back: %v", err)
	}
}

// The retired payload is refused BY NAME, and not as a typo. An operator
// whose script still says `--payload kagent` asked for a platform that this
// command no longer installs; "unknown payload" would read as a misspelling
// and send them looking for the right spelling of something that is gone.
func TestTheKagentPayloadIsRefusedAsRetiredRatherThanUnknown(t *testing.T) {
	o := created()
	o.Payload = PayloadKagent
	err := o.Validate()
	if err == nil {
		t.Fatal("a new lift was accepted with the retired kagent payload")
	}
	if strings.Contains(err.Error(), "unknown --payload") {
		t.Errorf("the retired payload is reported as a typo: %v", err)
	}
	for _, want := range []string{"--payload kagent", "retired", "--payload orka"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if err := ValidPayload(PayloadKagent); err == nil {
		t.Fatal("ValidPayload still accepts the retired payload")
	}
}

// Only one payload remains, and it keeps every phase that is about the
// CLUSTER. Losing one of those to the retirement would be a change to what
// `lift` is for, not a removal of the legacy runtime.
func TestTheOrkaPayloadKeepsEveryClusterPhase(t *testing.T) {
	steps := strings.Join(stepsFor(PayloadOrka), " ")
	for _, shared := range []string{"cluster", "boundary", "credential", "plane", "observability", "verify"} {
		if !strings.Contains(steps, shared) {
			t.Errorf("the orka payload dropped the shared phase %q: %s", shared, steps)
		}
	}
	if !strings.Contains(steps, "orka") {
		t.Errorf("the orka payload never installs Orka: %s", steps)
	}
}

// The phases that existed only to land the legacy runtime and its demo agents
// are gone from every list — the phase list, completion, and the banner's
// vocabulary. A `--step kagent` that still validated would run a phase whose
// implementation went with the payload.
func TestTheLegacyOnlyPhasesAreGoneEverywhere(t *testing.T) {
	for _, legacy := range []string{"kagent", "agents"} {
		if validStep(legacy, PayloadOrka) {
			t.Errorf("--step %q is still a phase of the only payload", legacy)
		}
		if strings.Contains(strings.Join(AllSteps(), " "), legacy) {
			t.Errorf("completion still offers the retired phase %q", legacy)
		}
		if purpose := StepPurpose[legacy]; purpose != "" {
			t.Errorf("the banner still describes the retired phase %q as %q", legacy, purpose)
		}
	}
	o := created()
	o.Step = "agents"
	err := o.Validate()
	if err == nil {
		t.Fatal("a retired phase was accepted")
	}
	if !strings.Contains(err.Error(), "not a phase of the orka payload") {
		t.Errorf("the refusal does not name the payload: %v", err)
	}
	if !strings.Contains(err.Error(), "orka, observability, verify") {
		t.Errorf("the refusal does not list the phases this lift has: %v", err)
	}
}

// The contract of Validate is that an operator learns everything wrong with
// what they asked for in one reading. A missing payload must not short-circuit
// the rest.
func TestAMissingPayloadStillReportsTheOtherProblems(t *testing.T) {
	o := created()
	o.Payload = ""
	o.ResourceGroup = ""
	err := o.Validate()
	if err == nil {
		t.Fatal("an empty lift was accepted")
	}
	for _, want := range []string{"--payload", "--resource-group"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("only some problems were reported; %q is missing from: %v", want, err)
		}
	}
}

// A plan that promises "the agent answers" on a payload that installs no
// agent has told the operator it will prove something it cannot.
func TestVerifyDescribesWhatTheRemainingPayloadCanActuallyProve(t *testing.T) {
	orka := PurposeOf("verify", PayloadOrka)
	if strings.Contains(orka, "agent answers") {
		t.Errorf("the orka plan promises an answer it cannot produce: %q", orka)
	}
	if !strings.Contains(orka, "Provider is yours") {
		t.Errorf("the orka plan does not say why nothing answers: %q", orka)
	}
}

// A record must remember what it landed, or a resumed run installs the other
// platform on top of it. New records can only be the one remaining payload.
func TestARecordRemembersItsPayload(t *testing.T) {
	r, err := NewRecord("a1b2c3d4", Created, PayloadOrka, "sub", "rg", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	if r.Payload != PayloadOrka || r.PayloadOrLegacy() != PayloadOrka {
		t.Fatalf("payload = %q / %q", r.Payload, r.PayloadOrLegacy())
	}
	if _, err := NewRecord("a1b2c3d4", Created, "orca", "sub", "rg", "cluster"); err == nil {
		t.Error("a record was started with a payload that is not one")
	}
	if _, err := NewRecord("a1b2c3d4", Created, PayloadKagent, "sub", "rg", "cluster"); err == nil {
		t.Error("a new record was started with the retired payload")
	}
}

// PayloadKagent survives as a HISTORICAL sentinel and nothing else: it is
// what a record written before the split, or by a run that really did land
// the legacy runtime, reads as. Reading an absent payload as unknown would
// refuse a teardown that is fine; reading it as orka would be a lie about
// what is on somebody's cluster, and teardown decides deletions from it.
func TestARecordFromBeforeTheSplitStillReadsAsKagent(t *testing.T) {
	if got := (&Record{}).PayloadOrLegacy(); got != PayloadKagent {
		t.Fatalf("a record from before the split reads as %q, want %q", got, PayloadKagent)
	}
	if got := (&Record{Payload: PayloadKagent}).PayloadOrLegacy(); got != PayloadKagent {
		t.Fatalf("a record that landed kagent reads as %q", got)
	}
}

// A record of a kagent lift must still DECODE. Teardown is the whole reason
// the sentinel is kept: those clusters exist, they are billing, and refusing
// to read their record would strand the only list of what to delete.
func TestAKagentRecordStillDecodesSoItsResourcesCanBeRemoved(t *testing.T) {
	body := `{"run_id":"a1b2c3d4","branch":"created","subscription":"sub",` +
		`"payload":"kagent","resource_group":"rg","cluster":"c",` +
		`"created":[{"kind":"Azure Monitor workspace","name":"w","id":"/subscriptions/s/resourceGroups/rg/providers/p/w","in_resource_group":true}]}`
	r, err := ReadRecord(strings.NewReader(body))
	if err != nil {
		t.Fatalf("a kagent record no longer decodes, so its resources cannot be torn down: %v", err)
	}
	if r.PayloadOrLegacy() != PayloadKagent {
		t.Errorf("the recorded payload was lost: %q", r.PayloadOrLegacy())
	}
	if len(r.Created) != 1 {
		t.Fatalf("the list teardown deletes by was lost: %+v", r.Created)
	}
	// Teardown asks only for the two names that identify the run, and must
	// not have grown a payload requirement along the way.
	o := Options{ResourceGroup: "rg", Cluster: "c"}
	if err := o.ValidateForTeardown(); err != nil {
		t.Fatalf("teardown of a recorded lift now demands more than it did: %v", err)
	}
}

// Shell completion offers the phases that exist. Offering a retired one sends
// an operator to a phase whose implementation is gone.
func TestCompletionOffersEveryPhaseOnceAndNoRetiredOne(t *testing.T) {
	all := strings.Join(AllSteps(), " ")
	for _, want := range []string{"cluster", "boundary", "credential", "plane", "orka", "observability", "verify"} {
		if !strings.Contains(all, want) {
			t.Errorf("completion never offers %q: %s", want, all)
		}
	}
	seen := map[string]bool{}
	for _, step := range AllSteps() {
		if seen[step] {
			t.Errorf("completion offers %q twice: %s", step, all)
		}
		seen[step] = true
	}
}

// The --byo refusal lists "every other phase", and those must be the phases
// this lift actually has.
func TestTheByoRefusalNamesThisPayloadsPhases(t *testing.T) {
	o := byo()
	o.Payload = PayloadOrka
	o.Step = "cluster"
	err := o.Validate()
	if err == nil {
		t.Fatal("--byo --step cluster was accepted")
	}
	if strings.Contains(err.Error(), "agents") {
		t.Errorf("the refusal offers a retired phase: %v", err)
	}
	if !strings.Contains(err.Error(), "orka") {
		t.Errorf("the refusal does not list this payload's phases: %v", err)
	}
}
