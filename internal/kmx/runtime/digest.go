package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

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
