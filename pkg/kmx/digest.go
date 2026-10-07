package kmx

import (
	"bytes"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	agentSourcePath       = "portable-agent.yaml"
	targetPlatformPath    = "target/platform"
	targetIDPath          = "target/id"
	bindingAPIVersionPath = "binding/api-version"
	bindingKindPath       = "binding/kind"
	bindingDataPath       = "binding/data"
)

// Digest is a canonical SHA-256 identity. Its zero value means unspecified.
type Digest struct {
	sum [sha256.Size]byte
}

var (
	_ encoding.TextMarshaler   = Digest{}
	_ encoding.TextUnmarshaler = (*Digest)(nil)
)

// ParseDigest accepts the canonical lowercase 64-hex representation used by
// the existing portable and rendered bundle identities.
func ParseDigest(value string) (Digest, error) {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return Digest{}, fmt.Errorf("digest must contain %d lowercase hexadecimal characters", sha256.Size*2)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return Digest{}, fmt.Errorf("invalid digest: %w", err)
	}
	var digest Digest
	copy(digest.sum[:], decoded)
	if digest.IsZero() {
		return Digest{}, fmt.Errorf("digest must not be the all-zero unspecified value")
	}
	return digest, nil
}

func (d Digest) IsZero() bool {
	return d == Digest{}
}

func (d Digest) String() string {
	if d.IsZero() {
		return ""
	}
	return hex.EncodeToString(d.sum[:])
}

func (d Digest) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

func (d *Digest) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*d = Digest{}
		return nil
	}
	parsed, err := ParseDigest(string(text))
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

type digestEntry struct {
	path string
	data []byte
}

func framedDigest(entries ...digestEntry) Digest {
	var frames bytes.Buffer
	for _, entry := range entries {
		fmt.Fprintf(&frames, "%s %d\n", entry.path, len(entry.data))
		frames.Write(entry.data)
		frames.WriteByte('\n')
	}
	sum := sha256.Sum256(frames.Bytes())
	var digest Digest
	copy(digest.sum[:], sum[:])
	return digest
}

func agentSourceDigest(data []byte) Digest {
	return framedDigest(digestEntry{path: agentSourcePath, data: data})
}
