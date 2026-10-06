package krm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// Graph is a validated set of resources: names are unique per kind and every
// Agent's Provider, Tools and delegation targets resolve within it.
// Resources are ordered by name.
type Graph struct {
	Providers []*Provider
	Tools     []*Tool
	Agents    []*Agent
}

// NewGraph orders the resources by name and validates every reference.
// All problems are reported together.
func NewGraph(providers []*Provider, tools []*Tool, agents []*Agent) (*Graph, error) {
	g := &Graph{
		Providers: sortedBy(providers, func(p *Provider) string { return p.Metadata.Name }),
		Tools:     sortedBy(tools, func(t *Tool) string { return t.Metadata.Name }),
		Agents:    sortedBy(agents, func(a *Agent) string { return a.Metadata.Name }),
	}
	var errs []error
	errs = append(errs, duplicates(KindProvider, g.Providers, func(p *Provider) string { return p.Metadata.Name })...)
	errs = append(errs, duplicates(KindTool, g.Tools, func(t *Tool) string { return t.Metadata.Name })...)
	errs = append(errs, duplicates(KindAgent, g.Agents, func(a *Agent) string { return a.Metadata.Name })...)
	for _, a := range g.Agents {
		if g.Provider(a.Spec.Provider) == nil {
			errs = append(errs, fmt.Errorf("Agent %q uses unknown Provider %q", a.Metadata.Name, a.Spec.Provider))
		}
		for _, name := range a.Spec.Tools {
			if g.Tool(name) == nil {
				errs = append(errs, fmt.Errorf("Agent %q uses unknown Tool %q", a.Metadata.Name, name))
			}
		}
		for _, name := range a.Spec.AllowedAgents {
			if g.Agent(name) == nil {
				errs = append(errs, fmt.Errorf("Agent %q allows delegation to unknown Agent %q", a.Metadata.Name, name))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *Graph) Provider(name string) *Provider {
	return find(g.Providers, name, func(p *Provider) string { return p.Metadata.Name })
}

func (g *Graph) Tool(name string) *Tool {
	return find(g.Tools, name, func(t *Tool) string { return t.Metadata.Name })
}

func (g *Graph) Agent(name string) *Agent {
	return find(g.Agents, name, func(a *Agent) string { return a.Metadata.Name })
}

// dependencies returns the names of everything the named Agent runs with:
// the Agent, its Provider and Tools, and every Agent it may delegate to,
// transitively, with their own Providers and Tools.
func (g *Graph) dependencies(agent string) (providers, tools, agents map[string]bool) {
	providers, tools, agents = map[string]bool{}, map[string]bool{}, map[string]bool{}
	queue := []string{agent}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if agents[name] {
			continue
		}
		agents[name] = true
		a := g.Agent(name)
		providers[a.Spec.Provider] = true
		for _, t := range a.Spec.Tools {
			tools[t] = true
		}
		queue = append(queue, a.Spec.AllowedAgents...)
	}
	return providers, tools, agents
}

// AgentIdentity is the digest of everything the named Agent runs with: the
// exact authored bytes of the Agent, its Provider and Tools, and every Agent
// it may delegate to, transitively, with their dependencies. Changing any of
// them, including a shared Provider or Tool, changes this identity.
//
// The digest is SHA-256 over "Agent <name>\n", naming the Agent identified,
// then each resource in kind order (Provider, Tool, Agent) and name order,
// framed as "<kind>/<name> <byte length>\n<bytes>\n". Agents that delegate to
// each other run with the same resources but keep distinct identities.
func (g *Graph) AgentIdentity(agent string) (string, error) {
	if g.Agent(agent) == nil {
		return "", fmt.Errorf("no Agent %q", agent)
	}
	providers, tools, agents := g.dependencies(agent)
	h := sha256.New()
	h.Write([]byte(KindAgent + " " + agent + "\n"))
	frame := func(kind, name string, source []byte) {
		h.Write([]byte(kind + "/" + name + " " + strconv.Itoa(len(source)) + "\n"))
		h.Write(source)
		h.Write([]byte("\n"))
	}
	for _, p := range g.Providers {
		if providers[p.Metadata.Name] {
			frame(KindProvider, p.Metadata.Name, p.source)
		}
	}
	for _, t := range g.Tools {
		if tools[t.Metadata.Name] {
			frame(KindTool, t.Metadata.Name, t.source)
		}
	}
	for _, a := range g.Agents {
		if agents[a.Metadata.Name] {
			frame(KindAgent, a.Metadata.Name, a.source)
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// AffectedAgents returns, in name order, every Agent whose identity changes
// when the named resource changes.
func (g *Graph) AffectedAgents(kind, name string) ([]string, error) {
	var affected []string
	for _, a := range g.Agents {
		providers, tools, agents := g.dependencies(a.Metadata.Name)
		var deps map[string]bool
		switch kind {
		case KindProvider:
			deps = providers
		case KindTool:
			deps = tools
		case KindAgent:
			deps = agents
		default:
			return nil, fmt.Errorf("unknown resource kind %q", kind)
		}
		if deps[name] {
			affected = append(affected, a.Metadata.Name)
		}
	}
	return affected, nil
}

func sortedBy[T any](items []*T, name func(*T) string) []*T {
	out := append([]*T(nil), items...)
	sort.SliceStable(out, func(i, j int) bool { return name(out[i]) < name(out[j]) })
	return out
}

func duplicates[T any](kind string, sorted []*T, name func(*T) string) []error {
	var errs []error
	for i := 1; i < len(sorted); i++ {
		if n := name(sorted[i]); n == name(sorted[i-1]) && (i == 1 || n != name(sorted[i-2])) {
			errs = append(errs, fmt.Errorf("%s %q is defined more than once", kind, n))
		}
	}
	return errs
}

func find[T any](items []*T, want string, name func(*T) string) *T {
	for _, item := range items {
		if name(item) == want {
			return item
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
