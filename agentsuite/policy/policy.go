package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
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
	for _, item := range p.DenyWritePaths {
		if err := validatePath(item); err != nil {
			return p, fmt.Errorf("administrative: %w", err)
		}
	}
	return p, nil
}

// decodeStrict rejects invalid UTF-8, duplicate keys at any nesting depth,
// unknown fields, trailing bytes, and secondary top-level values.
func decodeStrict(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return errors.New("JSON is not UTF-8")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("secondary top-level JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	first, err := dec.Token()
	if err != nil {
		return err
	}
	if err := consumeJSONValue(dec, first); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("secondary top-level JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func consumeJSONValue(dec *json.Decoder, first json.Token) error {
	delim, composite := first.(json.Delim)
	if !composite {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[key] = struct{}{}
			value, err := dec.Token()
			if err != nil {
				return err
			}
			if err := consumeJSONValue(dec, value); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("unterminated object")
		}
	case '[':
		for dec.More() {
			value, err := dec.Token()
			if err != nil {
				return err
			}
			if err := consumeJSONValue(dec, value); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("unterminated array")
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delim)
	}
	return nil
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
	return EffectivePolicy{
		Version:              SchemaVersion,
		DenyWritePaths:       uniqueSorted(s.DenyWritePaths, a.DenyWritePaths),
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
	for _, item := range p.DenyWritePaths {
		if err := validatePath(item); err != nil {
			return nil, err
		}
	}
	if err := validateCapabilities(p.RequiredCapabilities); err != nil {
		return nil, err
	}
	p.DenyWritePaths = uniqueSorted(p.DenyWritePaths)
	return json.Marshal(p)
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
