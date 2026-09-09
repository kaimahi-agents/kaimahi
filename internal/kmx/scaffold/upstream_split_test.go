package scaffold

import (
	"strings"
	"testing"
)

// A cluster with no kagent has nothing to reconcile the RemoteMCPServer,
// so the apply has to be able to leave it out. The FILE never is: the
// artifact an operator reviews is the whole onboarding, whether or not
// this cluster can accept all of it today.
func TestUpstreamDocumentsSplitTheSeamFromThePlane(t *testing.T) {
	spec := warehouse()
	plane, seam, err := UpstreamDocuments(spec)
	if err != nil {
		t.Fatal(err)
	}
	whole, err := GenerateUpstream(spec)
	if err != nil {
		t.Fatal(err)
	}
	if plane+seam != whole {
		t.Fatal("the split documents are not the generated file")
	}

	// The plane's three: the overlay entry and the policy pair.
	for _, want := range []string{
		"kind: ConfigMap", "kaimahi-upstreams-extra",
		"kaimahi-upstream-warehouse-egress", "kaimahi-upstream-warehouse-ingress",
	} {
		if !strings.Contains(plane, want) {
			t.Errorf("the plane's documents do not carry %q:\n%s", want, plane)
		}
	}
	if got := strings.Count(plane, "\n---\n") + 1; got != 3 {
		t.Errorf("the plane's half is %d documents, want 3:\n%s", got, plane)
	}

	// The seam's one, and nothing of it anywhere else.
	for _, kagentOnly := range []string{"RemoteMCPServer", "kagent.dev"} {
		if !strings.Contains(seam, kagentOnly) {
			t.Errorf("the seam document does not carry %q:\n%s", kagentOnly, seam)
		}
		if strings.Contains(plane, kagentOnly) {
			t.Errorf("%q is in the half applied without kagent:\n%s", kagentOnly, plane)
		}
	}
	if got := strings.Count(seam, "\n---\n") + 1; got != 1 {
		t.Errorf("the seam half is %d documents, want 1:\n%s", got, seam)
	}
}

// Both halves are refused for key shapes, the same as the whole file: a
// generator that only checked the concatenation would let a subset
// through unchecked.
func TestUpstreamDocumentsRefuseKeyShapes(t *testing.T) {
	spec := warehouse()
	spec.Secret = "ghp" + "_" + strings.Repeat("0", 36)
	if _, _, err := UpstreamDocuments(spec); err == nil {
		t.Fatal("a key-shaped value passed the split generator")
	}
	if _, err := GenerateUpstream(spec); err == nil {
		t.Fatal("a key-shaped value passed the whole generator")
	}
}
