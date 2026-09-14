package lift

// The payload split: what a lift lands, and why there is no default.

import (
	"strings"
	"testing"
)

// `lift` bills money and installs a platform. A default would mean somebody's
// existing script quietly changed which one it deploys the day the project's
// direction moved — so the refusal is the feature.
func TestAPayloadIsRequiredAndNeverDefaulted(t *testing.T) {
	o := created()
	o.Payload = ""
	err := o.Validate()
	if err == nil {
		t.Fatal("a lift with no payload was accepted")
	}
	for _, want := range []string{"--payload is required", "orka", "kagent", "bills money"} {
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

// The two payloads differ only in what runs agents. Everything about the
// CLUSTER — provisioning it, proving its boundary, the plane, monitoring,
// verification — is shared, and a change that split those would be a change
// to what `lift` is for.
func TestBothPayloadsShareEveryClusterPhase(t *testing.T) {
	orka := strings.Join(stepsFor(PayloadOrka), " ")
	kagent := strings.Join(stepsFor(PayloadKagent), " ")
	for _, shared := range []string{"cluster", "boundary", "credential", "plane", "observability", "verify"} {
		if !strings.Contains(orka, shared) {
			t.Errorf("the orka payload dropped the shared phase %q: %s", shared, orka)
		}
		if !strings.Contains(kagent, shared) {
			t.Errorf("the kagent payload dropped the shared phase %q: %s", shared, kagent)
		}
	}
}

// And they differ exactly where they should: one lands Orka, the other lands
// the legacy runtime and its demo agents.
func TestEachPayloadLandsOnlyItsOwnPlatform(t *testing.T) {
	orka := strings.Join(stepsFor(PayloadOrka), " ")
	kagent := strings.Join(stepsFor(PayloadKagent), " ")

	if !strings.Contains(orka, "orka") {
		t.Errorf("the orka payload never installs Orka: %s", orka)
	}
	for _, legacy := range []string{"kagent", "agents"} {
		if strings.Contains(orka, legacy) {
			t.Errorf("the orka payload still lands the legacy phase %q: %s", legacy, orka)
		}
	}
	for _, legacy := range []string{"kagent", "agents"} {
		if !strings.Contains(kagent, legacy) {
			t.Errorf("the kagent payload lost %q: %s", legacy, kagent)
		}
	}
	if strings.Contains(kagent, " orka ") {
		t.Errorf("the kagent payload installs Orka: %s", kagent)
	}
}

// A phase belongs to a payload. Judging `--step agents` against an orka lift
// has to refuse it, and say which phases that lift actually has.
func TestAStepIsJudgedAgainstItsOwnPayload(t *testing.T) {
	o := created()
	o.Payload = PayloadOrka
	o.Step = "agents"
	err := o.Validate()
	if err == nil {
		t.Fatal("a kagent phase was accepted on an orka lift")
	}
	// "a orka" would be wrong and "an kagent" equally so, hence the article
	// is avoided rather than guessed per payload.
	if !strings.Contains(err.Error(), "not a phase of the orka payload") {
		t.Errorf("the refusal does not name the payload: %v", err)
	}
	if !strings.Contains(err.Error(), "orka, observability, verify") {
		t.Errorf("the refusal does not list the phases this lift has: %v", err)
	}

	ok := created()
	ok.Payload = PayloadKagent
	ok.Step = "agents"
	if err := ok.Validate(); err != nil {
		t.Errorf("a kagent phase was refused on a kagent lift: %v", err)
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
func TestVerifyDescribesWhatEachPayloadCanActuallyProve(t *testing.T) {
	orka := PurposeOf("verify", PayloadOrka)
	if strings.Contains(orka, "agent answers") {
		t.Errorf("the orka plan promises an answer it cannot produce: %q", orka)
	}
	if !strings.Contains(orka, "Provider is yours") {
		t.Errorf("the orka plan does not say why nothing answers: %q", orka)
	}
	if kagent := PurposeOf("verify", PayloadKagent); !strings.Contains(kagent, "agent answers") {
		t.Errorf("the kagent plan stopped promising its answer: %q", kagent)
	}
}

// A record must remember what it landed, or a resumed run installs the other
// platform on top of it.
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
}

// A record written before the split carries no payload, and only one thing
// could have written it. Reading that as unknown would refuse a resume that
// is actually fine; reading it as orka would be a lie about history.
func TestALegacyRecordReadsAsKagent(t *testing.T) {
	if got := (&Record{}).PayloadOrLegacy(); got != PayloadKagent {
		t.Fatalf("a record from before the split reads as %q, want %q", got, PayloadKagent)
	}
}

// Shell completion cannot know which payload is being typed, so offering only
// one payload's phases hides valid answers for the other.
func TestCompletionOffersEveryPhaseOfBothPayloads(t *testing.T) {
	all := strings.Join(AllSteps(), " ")
	for _, want := range []string{"cluster", "boundary", "kagent", "credential", "plane", "agents", "orka", "observability", "verify"} {
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

// The --byo refusal lists "every other phase", and which those are depends on
// the payload. Advertising kagent phases to an orka lift sends an operator to
// a phase their lift does not have.
func TestTheByoRefusalNamesThisPayloadsPhases(t *testing.T) {
	o := byo()
	o.Payload = PayloadOrka
	o.Step = "cluster"
	err := o.Validate()
	if err == nil {
		t.Fatal("--byo --step cluster was accepted")
	}
	if strings.Contains(err.Error(), "agents") {
		t.Errorf("an orka lift was offered a kagent phase: %v", err)
	}
	if !strings.Contains(err.Error(), "orka") {
		t.Errorf("the refusal does not list this payload's phases: %v", err)
	}
}
