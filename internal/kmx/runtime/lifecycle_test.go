package runtime

import (
	"testing"
)

func TestDocumentCopiesBytesAtConstructionAndOnAccess(t *testing.T) {
	raw := []byte("value")
	document := ApplyDocument(raw)
	raw[0] = 'X'
	if !document.Deployable() {
		t.Fatal("ApplyDocument must be deployable")
	}
	if got := string(document.Bytes()); got != "value" {
		t.Fatalf("Bytes() = %q after mutating the source slice, want %q", got, "value")
	}
	document.Bytes()[0] = 'X'
	if got := string(document.Bytes()); got != "value" {
		t.Fatalf("Bytes() = %q after mutating a returned copy, want %q", got, "value")
	}
	if ReviewDocument(raw).Deployable() {
		t.Fatal("ReviewDocument must never be deployable")
	}
}
