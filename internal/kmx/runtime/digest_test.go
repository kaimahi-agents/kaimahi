package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"testing"
)

// Native document framing is stable and byte exact.
func TestRenderedBundleDigestFramesEachDocumentInOrder(t *testing.T) {
	documents := [][]byte{[]byte("first"), []byte("second")}
	want := hashHex(t, "rendered/000.yaml 5\nfirst\nrendered/001.yaml 6\nsecond\n")
	if got := RenderedBundleDigest(documents); got != want {
		t.Fatalf("RenderedBundleDigest = %q, want %q", got, want)
	}
}

func TestRenderedBundleDigestIsOrderSensitive(t *testing.T) {
	first := RenderedBundleDigest([][]byte{[]byte("a"), []byte("b")})
	if second := RenderedBundleDigest([][]byte{[]byte("b"), []byte("a")}); first == second {
		t.Fatalf("reordered documents produced the same digest %q", first)
	}
}

// Length framing is what stops two different document splits from hashing the
// same concatenated bytes.
func TestRenderedBundleDigestSeparatesDocumentBoundaries(t *testing.T) {
	first := RenderedBundleDigest([][]byte{[]byte("ab"), []byte("c")})
	if second := RenderedBundleDigest([][]byte{[]byte("a"), []byte("bc")}); first == second {
		t.Fatalf("different document boundaries produced the same digest %q", first)
	}
}

func TestDigestsAreLowercase64Hex(t *testing.T) {
	format := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for name, got := range map[string]string{
		"rendered":       RenderedBundleDigest([][]byte{[]byte("Mixed CASE 0xFF")}),
		"rendered empty": RenderedBundleDigest(nil),
	} {
		if !format.MatchString(got) {
			t.Fatalf("%s digest %q is not lowercase 64-hex", name, got)
		}
	}
}

func hashHex(t *testing.T, framed string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(framed))
	return hex.EncodeToString(sum[:])
}
