// Identity digests: DESIGN.md §2 defines two digests that are "retained and
// never described as equivalent". Both use the exact same catalogue framing
// — a stable logical path, a space, the decimal byte length, a newline, the
// raw bytes, and a trailing newline — hashed with SHA-256 into lowercase
// 64-hex. They differ only in what gets framed and under which path(s):
//
//  1. PortableBundleDigest frames the exact validated portable source bytes
//     once, under the fixed logical path "portable-agent.yaml". It
//     identifies authored portable behavior.
//  2. RenderedBundleDigest frames every final rendered document, in
//     deployment order, each under its own numbered logical path
//     ("rendered/000.yaml", "rendered/001.yaml", ...), concatenates the
//     frames, and hashes once. It identifies exact adapter output — Orka's
//     optional random Task changes this digest without changing the
//     portable digest, because the portable document never contained it.
package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// PortablePath is the stable logical path the portable bundle digest frames
// its source bytes under.
const PortablePath = "portable-agent.yaml"

// renderedPathFormat produces each rendered document's stable logical path
// from its zero-based position in deployment order.
const renderedPathFormat = "rendered/%03d.yaml"

// frameEntry reproduces DESIGN.md §2's exact catalogue framing for one
// logical path and its bytes: "path + \" \" + decimal_length + \"\\n\" +
// bytes + \"\\n\"". Framing the length and path — not just the raw bytes —
// is what is hashed, so an empty document, a renamed path, and a
// concatenation ambiguity are never confused with each other.
func frameEntry(path string, data []byte) []byte {
	header := fmt.Sprintf("%s %d\n", path, len(data))
	frame := make([]byte, 0, len(header)+len(data)+1)
	frame = append(frame, header...)
	frame = append(frame, data...)
	frame = append(frame, '\n')
	return frame
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PortableBundleDigest is DESIGN.md §2's portable bundle digest: catalogue
// framing over the exact validated portable source bytes under the stable
// logical path "portable-agent.yaml", SHA-256, lowercase 64-hex. Callers
// pass PortableAgent.Source() (or the deterministically encoded Orka
// shorthand's YAML bytes) — never a reserialized approximation.
func PortableBundleDigest(source []byte) string {
	return sha256Hex(frameEntry(PortablePath, source))
}

// RenderedBundleDigest is DESIGN.md §2's rendered bundle digest: the same
// framing applied to every final rendered document, in artifact order, under
// stable logical paths rendered/000.yaml, rendered/001.yaml, etc., with all
// frames concatenated and hashed once. It covers every rendered byte,
// including any document rendered for review only, so nothing an adapter
// emits escapes its output identity.
func RenderedBundleDigest(documents [][]byte) string {
	var frames bytes.Buffer
	for i, doc := range documents {
		frames.Write(frameEntry(fmt.Sprintf(renderedPathFormat, i), doc))
	}
	return sha256Hex(frames.Bytes())
}
