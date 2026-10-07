package kmx

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const digestPrefix = "sha256:"

// Digest is a canonical SHA-256 identity. Its zero value means unspecified.
type Digest struct {
	sum [sha256.Size]byte
}

var (
	_ encoding.TextMarshaler   = Digest{}
	_ encoding.TextUnmarshaler = (*Digest)(nil)
)

// NewDigest returns the SHA-256 identity of the exact bytes supplied.
func NewDigest(data []byte) Digest {
	return Digest{sum: sha256.Sum256(data)}
}

// ParseDigest accepts the canonical lowercase sha256:<hex> representation.
func ParseDigest(value string) (Digest, error) {
	if !strings.HasPrefix(value, digestPrefix) {
		return Digest{}, fmt.Errorf("digest must start with %q", digestPrefix)
	}
	hexValue := strings.TrimPrefix(value, digestPrefix)
	if len(hexValue) != sha256.Size*2 || strings.ToLower(hexValue) != hexValue {
		return Digest{}, fmt.Errorf("digest must contain %d lowercase hexadecimal characters", sha256.Size*2)
	}
	decoded, err := hex.DecodeString(hexValue)
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
	return digestPrefix + hex.EncodeToString(d.sum[:])
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

func digestParts(parts ...[]byte) Digest {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(strconv.Itoa(len(part))))
		h.Write([]byte{'\n'})
		h.Write(part)
		h.Write([]byte{'\n'})
	}
	var digest Digest
	copy(digest.sum[:], h.Sum(nil))
	return digest
}
