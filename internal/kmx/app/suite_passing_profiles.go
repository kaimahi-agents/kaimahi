package app

import (
	"errors"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

// suiteReceiptDeclarationView projects only kmx-owned legacy declaration labels
// for offline receipt coherence. These names are not upstream image/configuration
// ABIs or implementation qualification. Public implementation pins cannot supply
// observed runtime/host facts; every original receipt check still applies.
// Keep the profiled marker/graph: this copy grants no execution/export authority.
func suiteReceiptDeclarationView(s *workspace.NativeSuite) (*workspace.NativeSuite, error) {
	if s == nil {
		return nil, errSuitePassingReceipt
	}
	if s.Profile == nil {
		if err := s.RequireUnprofiled(); err != nil {
			return nil, errors.New("profiled receipt declaration is unavailable")
		}
		return s, nil
	}
	p := s.Profile
	if s.Platform.String() != "linux/amd64" || p.ImageRequired {
		return nil, errors.New("unsupported profiled private receipt declaration mapping")
	}
	result := *s
	switch {
	case p.ConfigurationABI == "kmx-native-orka-v2" && p.Runtime.ID == "orka":
		result.Runtime = workspace.RuntimeProfile{ID: "orka"}
		result.RendererVersion = "kmx.native-orka/v2"
	case p.ConfigurationABI == "kmx-native-kagent-v2" && p.Runtime.ID == "kagent" && p.Runtime.Version == "v0.10.3":
		result.Runtime = workspace.RuntimeProfile{ID: "kagent", Version: "v0.10.3", Engine: "go"}
		result.RendererVersion = "kmx.native-kagent/v2"
		result.ExecutionProfile = "kmx.kagent-v0.10.3-go-linux-amd64-tool-free-openai/v1"
	case p.ConfigurationABI == "kmx-native-agentsessions-v2" && p.Runtime.ID == "agentsessions" && p.Runtime.Version == workspace.AgentSessionsVersion:
		result.Runtime = workspace.RuntimeProfile{ID: "agentsessions", Version: workspace.AgentSessionsVersion, Engine: "chat"}
		result.RendererVersion = "kmx.native-agentsessions/v2"
		result.ExecutionProfile = "kmx.agentsessions-chat-system-prompt-tool-free-openai/v1"
	default:
		return nil, errors.New("unsupported profiled private receipt declaration mapping")
	}
	return &result, nil
}

// Different private receipt layouts carry composition identity in different
// subject envelopes, just as they do S. No default-mode interpretation is safe.
func (r *suitePassingReceipt) compositionDigest() string {
	if r == nil {
		return ""
	}
	switch r.ExecutionMode {
	case "native-orka":
		return r.Subject.CompositionDigest
	case "native-kagent":
		if r.Kagent != nil {
			return r.Kagent.Observation.Subject.CompositionDigest
		}
	case "native-agentsessions":
		if r.Sessions != nil {
			return r.Sessions.Subject.CompositionDigest
		}
	}
	return ""
}
