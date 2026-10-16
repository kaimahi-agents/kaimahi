package workspace

import (
	"bytes"
	"errors"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/agent-primitives/suite/policy"
	"github.com/gowebpki/jcs"
	godigest "github.com/opencontainers/go-digest"
)

// NativePolicy retains the authored document, not an effective policy or proof of enforcement.
// Each loaded inventory owns its bytes and descriptor.
type NativePolicy struct {
	Path       string
	Descriptor agentsuite.Descriptor
	Bytes      []byte
}

var ErrPolicyExecutionUnsupported = errors.New("declared policy execution is unsupported; policy requires qualified enforcement before eval or Lift")

// RequirePolicyFree protects consumers that cannot enforce a declared policy.
// Offline loading and unevaluated transfer deliberately do not call this gate.
func (s *NativeSuite) RequirePolicyFree() error {
	if s == nil {
		return errors.New("native suite is required")
	}
	if s.Policy != nil {
		return ErrPolicyExecutionUnsupported
	}
	return nil
}

func canonicalMemberPolicy(raw []byte) ([]byte, error) {
	authored, err := policy.DecodeSuitePolicy(raw)
	if err != nil || authored.Version != policy.SchemaVersionV2 {
		// Decoder errors may quote arbitrary authored keys. Expose only a fixed diagnostic.
		return nil, errors.New("member policy requires a valid agentsuite.policy/v2 IPv4 TCP egress document")
	}
	// Do not Resolve/Payload here: authored arrays commit to order and duplicates.
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, errors.New("invalid member policy JSON")
	}
	return canonical, nil
}

func bindMemberPolicy(files map[string][]byte, id string, raw []byte) (*agentsuite.FileRef, error) {
	if raw == nil {
		return nil, nil
	}
	canonical, err := canonicalMemberPolicy(raw)
	if err != nil {
		return nil, err
	}
	name := "policies/" + id + ".json"
	files[name] = canonical
	return &agentsuite.FileRef{Path: name, Descriptor: agentsuite.Descriptor{MediaType: agentsuite.MediaTypePolicyV2, Digest: godigest.FromBytes(canonical).String(), Size: int64(len(canonical))}}, nil
}

func readMemberPolicy(files map[string][]byte, agent agentsuite.Agent, profile RuntimeProfile) ([]byte, error) {
	if agent.Policy == nil {
		return nil, nil
	}
	ref := *agent.Policy
	raw, exists := files[ref.Path]
	if profile.ID != "orka" || ref.Path != "policies/"+agent.ID+".json" || ref.Descriptor.MediaType != agentsuite.MediaTypePolicyV2 || !exists || !agentsuite.MatchesCanonicalDescriptor(raw, ref.Descriptor) {
		return nil, errors.New("unsupported native member policy binding")
	}
	canonical, err := canonicalMemberPolicy(raw)
	if err != nil || !bytes.Equal(canonical, raw) {
		return nil, errors.New("unsupported native member policy document")
	}
	return raw, nil
}
