package runtime

import (
	"fmt"
	"strings"
)

// PreparedPortableRender carries source bytes validated for one target. Its
// private fields prevent callers from supplying unchecked behavior to Render.
type PreparedPortableRender struct {
	source []byte
	target ID
}

// PreparePortableRender validates authored bytes and refuses behavior in any
// extension the target does not consume, before an adapter can render them.
func PreparePortableRender(source []byte, adapter LifecycleAdapter) (*PreparedPortableRender, error) {
	if adapter == nil || adapter.ID() == "" {
		return nil, fmt.Errorf("render target is required")
	}
	agent, err := ParsePortableAgent(source)
	if err != nil {
		return nil, err
	}
	consumes := make(map[ID]bool)
	for _, extension := range adapter.ConsumedExtensions() {
		consumes[extension] = true
	}
	var paths []string
	if extension := agent.Extensions.Orka; extension != nil && !consumes[Orka] {
		if hasOrkaLimit(extension.Provider.RateLimit) {
			paths = append(paths, "extensions.orka.provider.rateLimit")
		}
		if extension.Agent != nil {
			if len(extension.Agent.Tools) != 0 {
				paths = append(paths, "extensions.orka.agent.tools")
			}
			if len(extension.Agent.Skills) != 0 {
				paths = append(paths, "extensions.orka.agent.skills")
			}
			if hasOrkaLimit(extension.Agent.RateLimit) {
				paths = append(paths, "extensions.orka.agent.rateLimit")
			}
			if extension.Agent.Coordination != nil {
				paths = append(paths, "extensions.orka.agent.coordination")
			}
		}
	}
	if extension := agent.Extensions.Kagent; extension != nil && !consumes[Kagent] {
		paths = append(paths, "extensions.kagent.runtime")
		if len(extension.Tools) != 0 {
			paths = append(paths, "extensions.kagent.tools")
		}
	}
	if len(paths) != 0 {
		return nil, fmt.Errorf("runtime %s cannot honor behavior in %s", adapter.ID(), strings.Join(paths, ", "))
	}
	return &PreparedPortableRender{source: agent.Source(), target: adapter.ID()}, nil
}

func hasOrkaLimit(limit *OrkaRateLimit) bool {
	return limit != nil && (limit.RequestsPerMinute != nil || limit.TokensPerMinute != nil)
}

// ForAdapter returns independent copies of the checked document and exact
// source. A nil, zero-value or wrong-target preparation cannot be rendered.
func (p *PreparedPortableRender) ForAdapter(target ID) (*PortableAgent, []byte, error) {
	if p == nil || p.target == "" || len(p.source) == 0 {
		return nil, nil, fmt.Errorf("render requires a prepared portable document")
	}
	if p.target != target {
		return nil, nil, fmt.Errorf("portable document prepared for %s, not %s", p.target, target)
	}
	agent, err := ParsePortableAgent(p.source)
	if err != nil {
		return nil, nil, err
	}
	return agent, agent.Source(), nil
}
