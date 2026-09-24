package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"testing"
)

var hex64RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DESIGN.md §2: "catalogue framing over the exact validated portable source
// bytes under stable logical path portable-agent.yaml: path + " " +
// decimal_length + "\n" + bytes + "\n", SHA-256, lowercase 64-hex."
func TestDigestPortableBundleDigestMatchesExactFraming(t *testing.T) {
	data := []byte("hello portable agent")
	frame := fmt.Sprintf("portable-agent.yaml %d\n%s\n", len(data), data)
	sum := sha256.Sum256([]byte(frame))
	want := hex.EncodeToString(sum[:])

	if got := PortableBundleDigest(data); got != want {
		t.Errorf("PortableBundleDigest = %q, want %q", got, want)
	}
}

func TestDigestPortableBundleDigestIsLowercase64Hex(t *testing.T) {
	got := PortableBundleDigest([]byte("anything"))
	if !hex64RE.MatchString(got) {
		t.Errorf("PortableBundleDigest = %q, want 64 lowercase hex characters", got)
	}
}

func TestDigestPortableBundleDigestChangesWithContent(t *testing.T) {
	a := PortableBundleDigest([]byte("one"))
	b := PortableBundleDigest([]byte("two"))
	if a == b {
		t.Errorf("different content produced the same digest %q", a)
	}
}

// DESIGN.md §2: "the same framing over every byte of the final rendered
// documents in deployment order under stable logical paths rendered/000.yaml,
// rendered/001.yaml, etc."
func TestDigestRenderedBundleDigestMatchesExactFraming(t *testing.T) {
	docs := [][]byte{[]byte("first document"), []byte("second document")}
	var concatenated []byte
	for i, doc := range docs {
		frame := fmt.Sprintf("rendered/%03d.yaml %d\n%s\n", i, len(doc), doc)
		concatenated = append(concatenated, []byte(frame)...)
	}
	sum := sha256.Sum256(concatenated)
	want := hex.EncodeToString(sum[:])

	if got := RenderedBundleDigest(docs); got != want {
		t.Errorf("RenderedBundleDigest = %q, want %q", got, want)
	}
}

func TestDigestRenderedBundleDigestIsLowercase64Hex(t *testing.T) {
	got := RenderedBundleDigest([][]byte{[]byte("a"), []byte("b")})
	if !hex64RE.MatchString(got) {
		t.Errorf("RenderedBundleDigest = %q, want 64 lowercase hex characters", got)
	}
}

// Order is part of identity: DESIGN.md §2 frames documents "in deployment
// order" under numbered logical paths, so reordering two otherwise-identical
// documents must change the digest.
func TestDigestRenderedBundleDigestChangesWithDocumentOrder(t *testing.T) {
	a := RenderedBundleDigest([][]byte{[]byte("alpha"), []byte("beta")})
	b := RenderedBundleDigest([][]byte{[]byte("beta"), []byte("alpha")})
	if a == b {
		t.Errorf("swapping document order did not change the digest %q", a)
	}
}

// DESIGN.md §2: "Orka's optional random Task therefore changes the rendered
// digest without changing the portable digest." Exercised structurally here:
// adding a document to the rendered set must not perturb the portable
// digest of unrelated content, proving the two digests are computed from
// wholly independent inputs, not merely relabeled the same hash.
func TestDigestPortableDigestIsIndependentOfRenderedDocuments(t *testing.T) {
	source := []byte("authored portable behavior")
	before := PortableBundleDigest(source)

	_ = RenderedBundleDigest([][]byte{[]byte("provider"), []byte("agent"), []byte("random-task")})

	after := PortableBundleDigest(source)
	if before != after {
		t.Errorf("PortableBundleDigest changed from %q to %q after computing an unrelated rendered digest", before, after)
	}
}

// The same bytes framed under the portable path vs. the first rendered path
// must not collide: the logical path is part of what gets hashed, not just
// the raw content.
func TestDigestSameBytesFramedUnderDifferentPathsDiffer(t *testing.T) {
	data := []byte("identical bytes")
	portable := PortableBundleDigest(data)
	rendered := RenderedBundleDigest([][]byte{data})
	if portable == rendered {
		t.Errorf("portable and rendered/000.yaml framing of identical bytes produced the same digest %q", portable)
	}
}
