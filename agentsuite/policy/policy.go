package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/agentsuite"
)

var digestDomain = []byte("agentsuite-effective-policy\x00v1\x00")

// DecodeSuitePolicy strictly decodes exactly one JSON object.
func DecodeSuitePolicy(data []byte) (SuitePolicy, error) {
	var p SuitePolicy
	if err := decodeStrict(data, &p); err != nil {
		return p, fmt.Errorf("decode policy: %w", err)
	}
	if err := Validate(p); err != nil {
		return p, err
	}
	return p, nil
}

// DecodeAdministrativePolicy applies the same closed JSON rules as suite policy decoding.
func DecodeAdministrativePolicy(data []byte) (AdministrativePolicy, error) {
	var p AdministrativePolicy
	if err := decodeStrict(data, &p); err != nil {
		return p, fmt.Errorf("decode administrative policy: %w", err)
	}
	if len(p.DenyWritePaths) > 4096 {
		return p, errors.New("too many administrative denyWritePaths")
	}
	for _, item := range p.DenyWritePaths {
		if err := validatePath(item); err != nil {
			return p, fmt.Errorf("administrative: %w", err)
		}
	}
	return p, nil
}

func decodeStrict(data []byte, dst any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("policy JSON must be an object")
	}
	return agentsuite.Unmarshal(data, dst)
}

func Validate(p SuitePolicy) error {
	if p.Version != SchemaVersion {
		return fmt.Errorf("unsupported policy version %q", p.Version)
	}
	if len(p.DenyWritePaths) == 0 {
		return errors.New("denyWritePaths must not be empty")
	}
	if len(p.DenyWritePaths) > 4096 {
		return errors.New("too many denyWritePaths")
	}
	for _, item := range p.DenyWritePaths {
		if err := validatePath(item); err != nil {
			return err
		}
	}
	return validateCapabilities(p.RequiredCapabilities)
}

func validateCapabilities(capabilities []string) error {
	if len(capabilities) == 0 {
		return errors.New("requiredCapabilities must not be empty")
	}
	if len(capabilities) != 1 || capabilities[0] != FilesystemDenyWrite {
		return fmt.Errorf("requiredCapabilities must be exactly [%q]", FilesystemDenyWrite)
	}
	return nil
}

func validatePath(value string) error {
	if value == "" || !strings.HasPrefix(value, "/") || strings.ContainsRune(value, 0) || path.Clean(value) != value {
		return fmt.Errorf("path %q must be clean and absolute", value)
	}
	if value == "/" {
		return errors.New("root deny path is not supported")
	}
	return nil
}

func uniqueSorted(groups ...[]string) []string {
	values := make(map[string]struct{})
	for _, group := range groups {
		for _, value := range group {
			values[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// Resolve validates and monotonically composes requested and administrative restrictions.
func Resolve(s SuitePolicy, a AdministrativePolicy) (EffectivePolicy, error) {
	if err := Validate(s); err != nil {
		return EffectivePolicy{}, err
	}
	for _, item := range a.DenyWritePaths {
		if err := validatePath(item); err != nil {
			return EffectivePolicy{}, fmt.Errorf("administrative: %w", err)
		}
	}
	if len(a.DenyWritePaths) > 4096 {
		return EffectivePolicy{}, errors.New("too many administrative denyWritePaths")
	}
	paths := uniqueSorted(s.DenyWritePaths, a.DenyWritePaths)
	if len(paths) > 4096 {
		return EffectivePolicy{}, errors.New("too many effective denyWritePaths")
	}
	return EffectivePolicy{
		Version:              SchemaVersion,
		DenyWritePaths:       paths,
		RequiredCapabilities: []string{FilesystemDenyWrite},
	}, nil
}

// CanonicalBytes emits deterministic UTF-8 JSON for a fully valid effective policy.
func CanonicalBytes(p EffectivePolicy) ([]byte, error) {
	if p.Version != SchemaVersion {
		return nil, errors.New("unsupported effective policy version")
	}
	if len(p.DenyWritePaths) == 0 {
		return nil, errors.New("denyWritePaths must not be empty")
	}
	if len(p.DenyWritePaths) > 4096 {
		return nil, errors.New("too many denyWritePaths")
	}
	for _, item := range p.DenyWritePaths {
		if err := validatePath(item); err != nil {
			return nil, err
		}
	}
	if err := validateCapabilities(p.RequiredCapabilities); err != nil {
		return nil, err
	}
	p.DenyWritePaths = uniqueSorted(p.DenyWritePaths)
	return agentsuite.Marshal(p)
}

// Digest returns a domain-separated, length-framed SHA-256 digest.
func Digest(data []byte) string {
	hash := sha256.New()
	hash.Write(digestDomain)
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(data)))
	hash.Write(size[:])
	hash.Write(data)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func Payload(p EffectivePolicy) (ProviderPayload, error) {
	data, err := CanonicalBytes(p)
	if err != nil {
		return ProviderPayload{}, err
	}
	return ProviderPayload{
		Version:              SchemaVersion,
		PolicyBytes:          data,
		PolicyDigest:         Digest(data),
		RequiredCapabilities: append([]string(nil), p.RequiredCapabilities...),
	}, nil
}

func VerifyPayload(p ProviderPayload) error {
	if p.Version != SchemaVersion {
		return errors.New("unsupported payload version")
	}
	if err := validateCapabilities(p.RequiredCapabilities); err != nil {
		return fmt.Errorf("payload: %w", err)
	}
	if Digest(p.PolicyBytes) != p.PolicyDigest {
		return errors.New("policy digest mismatch")
	}
	var effective EffectivePolicy
	if err := decodeStrict(p.PolicyBytes, &effective); err != nil {
		return fmt.Errorf("decode effective policy: %w", err)
	}
	canonical, err := CanonicalBytes(effective)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, p.PolicyBytes) {
		return errors.New("policy bytes are not canonical")
	}
	if !equalStrings(p.RequiredCapabilities, effective.RequiredCapabilities) {
		return errors.New("payload capabilities do not match policy capabilities")
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
