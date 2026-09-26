// Identity digests. Two digests are retained and are never described as
// equivalent: one identifies authored portable behavior, the other identifies
// the exact bytes an adapter rendered. Both use the same catalogue framing — a
// stable logical path, a space, the decimal byte length, a newline, the raw
// bytes, and a trailing newline — hashed with SHA-256 into lowercase 64-hex.
// Framing path and length, not just bytes, keeps an empty document, a renamed
// path, and a different document split from ever hashing alike.
package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// PortablePath is the stable logical path the portable digest frames its
// source bytes under.
const PortablePath = "portable-agent.yaml"

// renderedPathFormat derives each rendered document's logical path from its
// zero-based position in artifact order.
const renderedPathFormat = "rendered/%03d.yaml"

func frameEntry(path string, data []byte) []byte {
	header := fmt.Sprintf("%s %d\n", path, len(data))
	frame := make([]byte, 0, len(header)+len(data)+1)
	frame = append(frame, header...)
	frame = append(frame, data...)
	return append(frame, '\n')
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PortableBundleDigest frames the exact validated portable source bytes under
// PortablePath. Callers pass the authored bytes themselves, never a
// reserialized approximation, because the digest identifies what was authored.
func PortableBundleDigest(source []byte) string {
	return sha256Hex(frameEntry(PortablePath, source))
}

// RenderedBundleDigest frames every final rendered document in artifact order
// under its own numbered logical path and hashes the concatenated frames once.
// It covers review-only documents too, so nothing an adapter emits escapes its
// output identity.
func RenderedBundleDigest(documents [][]byte) string {
	var frames bytes.Buffer
	for i, document := range documents {
		frames.Write(frameEntry(fmt.Sprintf(renderedPathFormat, i), document))
	}
	return sha256Hex(frames.Bytes())
}
