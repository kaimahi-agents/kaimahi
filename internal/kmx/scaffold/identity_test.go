package scaffold

import (
	"strings"
	"testing"
)

func byo(t *testing.T, runAsUser string) string {
	t.Helper()
	id, err := ParseRunAsUser(runAsUser, "acme/agent:1")
	if err != nil {
		t.Fatalf("ParseRunAsUser(%q): %v", runAsUser, err)
	}
	doc, err := Generate(Spec{Name: "a1", Image: "acme/agent:1", Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// The declarative path pins UID 1001 because that is the user of ONE image,
// kagent's. Copying it onto an arbitrary image would fail the pod at
// CreateContainer over a UID the operator never chose.
func TestBYONeverInheritsTheDeclarativeUID(t *testing.T) {
	for _, runAsUser := range []string{"", "root", "65532"} {
		doc := byo(t, runAsUser)
		if strings.Contains(doc, "runAsUser: 1001") {
			t.Errorf("--run-as-user %q stamped kagent's UID on somebody else's image:\n%s", runAsUser, doc)
		}
	}
}

// The half of the posture that cannot be made wrong by an image's contents
// is not optional, and does not wait for a flag.
func TestBYOAlwaysDropsCapabilitiesAndPrivileges(t *testing.T) {
	for _, runAsUser := range []string{"", "root", "65532"} {
		doc := byo(t, runAsUser)
		for _, required := range []string{
			"allowPrivilegeEscalation: false",
			"drop: [ALL]",
			"type: RuntimeDefault",
		} {
			if !strings.Contains(doc, required) {
				t.Errorf("--run-as-user %q lost %q, which no image can make wrong:\n%s", runAsUser, required, doc)
			}
		}
	}
}

// A stated UID buys the same posture a declarative agent gets.
func TestAStatedUIDGetsTheFullPosture(t *testing.T) {
	doc := byo(t, "65532")
	for _, required := range []string{
		"runAsNonRoot: true",
		"runAsUser: 65532",
		"readOnlyRootFilesystem: true",
		"mountPath: /tmp",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("a stated UID did not get %q:\n%s", required, doc)
		}
	}
}

// The gap has to be legible in the ARTIFACT. The manifest is what gets
// committed and reviewed; a warning that scrolled past the terminal once is
// not a record of what was left off.
func TestAnUnstatedUIDLeavesTheGapWrittenDown(t *testing.T) {
	doc := byo(t, "")
	if strings.Contains(doc, "runAsNonRoot") && !strings.Contains(doc, "# runAsNonRoot is NOT set") {
		t.Errorf("runAsNonRoot was set without a UID to satisfy it:\n%s", doc)
	}
	if strings.Contains(doc, "readOnlyRootFilesystem: true") {
		t.Errorf("kmx cannot know this image tolerates a read-only root:\n%s", doc)
	}
	for _, said := range []string{"# runAsNonRoot is NOT set", "--run-as-user", "readOnlyRootFilesystem is NOT set"} {
		if !strings.Contains(doc, said) {
			t.Errorf("the manifest does not say %q, so a reviewer cannot see the gap:\n%s", said, doc)
		}
	}
}

// Root is allowed only when someone says it, and it is never silent.
func TestRootMustBeSaidOutLoud(t *testing.T) {
	if _, err := ParseRunAsUser("0", "acme/agent:1"); err == nil {
		t.Error("--run-as-user 0 is root by another spelling and must be refused")
	}
	id, err := ParseRunAsUser("root", "acme/agent:1")
	if err != nil {
		t.Fatal(err)
	}
	if !id.Root || id.UID != 0 || !id.Stated {
		t.Errorf("root is a stated answer, not an absent one: %+v", id)
	}
	if !strings.Contains(id.Note, "ROOT") {
		t.Errorf("the note must name what was chosen: %q", id.Note)
	}
	doc := byo(t, "root")
	if strings.Contains(doc, "runAsNonRoot: true") {
		t.Errorf("runAsNonRoot alongside root would fail the pod it was asked to run:\n%s", doc)
	}
}

// An unanswered question and an answered one must not read alike — the same
// line `kmx status` draws between "there are none" and "kmx cannot tell".
func TestAnUnstatedUIDIsNotTheSameAsRoot(t *testing.T) {
	absent, err := ParseRunAsUser("", "acme/agent:1")
	if err != nil {
		t.Fatal(err)
	}
	stated, err := ParseRunAsUser("root", "acme/agent:1")
	if err != nil {
		t.Fatal(err)
	}
	if absent.Stated || !stated.Stated {
		t.Errorf("absent and root are the same answer: %+v vs %+v", absent, stated)
	}
	if absent.Note == stated.Note {
		t.Error("not knowing and choosing root print the same thing")
	}
}

// A user NAME is exactly what Kubernetes cannot act on, so it is refused
// here rather than at CreateContainer.
func TestANamedUserIsRefusedWithHowToFindTheNumber(t *testing.T) {
	_, err := ParseRunAsUser("python", "acme/agent:1")
	if err == nil {
		t.Fatal("a user name must be refused: the kubelet cannot prove it is non-root")
	}
	if !strings.Contains(err.Error(), "docker image inspect") {
		t.Errorf("the refusal must say how to find the number: %v", err)
	}
}

// Same rule --isolation follows: a flag that reaches nothing is worse than
// no flag.
func TestRunAsUserNeedsAnImage(t *testing.T) {
	if _, err := ParseRunAsUser("1001", ""); err == nil {
		t.Error("--run-as-user without --image must be refused")
	}
}

// The declarative path keeps its pin, and keeps it unconditionally.
func TestDeclarativeHardeningIsUntouched(t *testing.T) {
	doc, err := Generate(Spec{Name: "a1", ModelConfig: "ollama"})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"runAsNonRoot: true", "runAsUser: 1001", "readOnlyRootFilesystem: true",
		"allowPrivilegeEscalation: false", "drop: [ALL]", "type: RuntimeDefault",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("the declarative agent lost %q to make BYO easier:\n%s", required, doc)
		}
	}
}

// The key-shape scan is a promise about the DOCUMENT, and the BYO branch
// returned before making it.
//
// Every operator-supplied field is also scanned individually on the way in, so
// the two checks overlap for everything --image accepts today. They stop
// overlapping the moment a field reaches the manifest without being added to
// that input list — which is what Placement does, and what the next field
// added to renderBYO will do by default. That is the gap this closes, and it
// is the reason the declarative path ends with the same call.
func TestBYODocumentIsScannedForKeyShapes(t *testing.T) {
	id, err := ParseRunAsUser("65532", "acme/agent:1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Generate(Spec{
		Name:  "a1",
		Image: "acme/agent:1",
		// Reaches the document; not in Generate's per-input scan.
		Placement: &Placement{NodeSelector: map[string]string{"pool": "kmh_" + strings.Repeat("a", 12)}},
		Identity:  id,
	})
	if err == nil {
		t.Fatal("a key shape reached a BYO manifest: the final document scan was skipped")
	}
	if !strings.Contains(err.Error(), "credential") {
		t.Errorf("the refusal does not name what it found: %v", err)
	}
}
