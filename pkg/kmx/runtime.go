package kmx

import (
	"fmt"
)

type RuntimeOptions struct {
	Profile string `json:"profile,omitempty"`
}

// TargetBinding is versioned, opaque deployment input for one exact target.
// Generic KMX workflows include it in identity and pass it unchanged; only the
// selected runtime interprets its bytes.
type TargetBinding struct {
	target     TargetRef
	apiVersion string
	kind       string
	digest     Digest
	data       []byte
}

func NewTargetBinding(target TargetRef, apiVersion, kind string, data []byte) (TargetBinding, error) {
	if err := target.Validate(); err != nil {
		return TargetBinding{}, err
	}
	if err := validateIdentity("target binding API version", apiVersion); err != nil {
		return TargetBinding{}, err
	}
	if err := validateIdentity("target binding kind", kind); err != nil {
		return TargetBinding{}, err
	}
	if len(data) == 0 {
		return TargetBinding{}, fmt.Errorf("target binding data is required")
	}
	copied := append([]byte(nil), data...)
	return TargetBinding{
		target:     target,
		apiVersion: apiVersion,
		kind:       kind,
		digest: framedDigest(
			digestEntry{path: targetPlatformPath, data: []byte(target.Platform)},
			digestEntry{path: targetIDPath, data: []byte(target.ID)},
			digestEntry{path: bindingAPIVersionPath, data: []byte(apiVersion)},
			digestEntry{path: bindingKindPath, data: []byte(kind)},
			digestEntry{path: bindingDataPath, data: copied},
		),
		data: copied,
	}, nil
}

func (b TargetBinding) Target() TargetRef  { return b.target }
func (b TargetBinding) APIVersion() string { return b.apiVersion }
func (b TargetBinding) Kind() string       { return b.kind }
func (b TargetBinding) Digest() Digest     { return b.digest }
func (b TargetBinding) Bytes() []byte      { return append([]byte(nil), b.data...) }

func (b TargetBinding) Validate() error {
	if err := b.target.Validate(); err != nil {
		return err
	}
	if err := validateIdentity("target binding API version", b.apiVersion); err != nil {
		return err
	}
	if err := validateIdentity("target binding kind", b.kind); err != nil {
		return err
	}
	if len(b.data) == 0 || b.digest.IsZero() {
		return fmt.Errorf("target binding data and digest are required")
	}
	return nil
}

func (TargetBinding) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("TargetBinding is an in-process value; persist its source document")
}

func (*TargetBinding) UnmarshalJSON([]byte) error {
	return fmt.Errorf("TargetBinding is an in-process value; read its source document")
}
