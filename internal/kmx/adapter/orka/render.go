// Package orka is the Orka adapter for the krm resource model. It renders a
// validated krm.Graph into Orka core.orka.ai/v1alpha1 Provider, Tool and Agent
// resources; each shared Provider or Tool renders once, however many Agents
// use it.
//
// Rendering performs no cluster or schema I/O. Callers validate Providers and
// Agents against their selected orkaschema.Validator before emission.
package orka

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/krm"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

const (
	APIVersion = "core.orka.ai/v1alpha1"

	descriptionAnnotation = "kaimahi.dev/description"
)

// Options selects where and how resources are rendered.
type Options struct {
	// Namespace is required: every resource renders into it.
	Namespace string
	// Labels are added to every rendered resource.
	Labels map[string]string
}

// Rendered holds Orka resources in apply order: Providers, then Tools, then
// Agents, each by name.
type Rendered struct {
	Documents []map[string]any
	// Secrets lists, by name and key, the value-free prerequisites the target
	// namespace must already hold. Rendering never creates a Secret.
	Secrets []krm.SecretKeyRef
}

// Render translates every resource in g.
func Render(g *krm.Graph, opts Options) (*Rendered, error) {
	if g == nil {
		return nil, fmt.Errorf("nothing to render")
	}
	namespace := opts.Namespace
	if err := scaffold.ValidateNamespace(namespace); err != nil {
		return nil, fmt.Errorf("an explicit Orka namespace is required: %w", err)
	}
	r := &Rendered{}
	secrets := map[krm.SecretKeyRef]bool{}
	meta := func(kind, name string) map[string]any {
		metadata := map[string]any{"name": name, "namespace": namespace}
		if len(opts.Labels) > 0 {
			labels := make(map[string]any, len(opts.Labels))
			for k, v := range opts.Labels {
				labels[k] = v
			}
			metadata["labels"] = labels
		}
		return map[string]any{"apiVersion": APIVersion, "kind": kind, "metadata": metadata}
	}

	for _, p := range g.Providers {
		s := p.Spec
		spec := map[string]any{
			"type":         s.Type,
			"defaultModel": s.Model,
			"secretRef":    map[string]any{"name": s.Credentials.Secret.Name, "key": s.Credentials.Secret.Key},
		}
		switch {
		case s.OpenAI != nil && s.OpenAI.BaseURL != "":
			spec["baseURL"] = s.OpenAI.BaseURL
		case s.Azure != nil:
			spec["baseURL"] = s.Azure.Endpoint
			azure := map[string]any{"deploymentName": s.Model}
			if s.Azure.APIVersion != "" {
				azure["apiVersion"] = s.Azure.APIVersion
			}
			spec["azure"] = azure
		}
		if limits := rateLimit(s.RateLimits); limits != nil {
			spec["rateLimit"] = limits
		}
		secrets[s.Credentials.Secret] = true
		doc := meta("Provider", p.Metadata.Name)
		doc["spec"] = spec
		r.Documents = append(r.Documents, doc)
	}

	for _, t := range g.Tools {
		h := t.Spec.HTTP
		httpSpec := map[string]any{"method": h.Method, "url": h.URL}
		if h.Timeout != "" {
			httpSpec["timeout"] = h.Timeout
		}
		if len(h.Headers) > 0 {
			headers := make(map[string]any, len(h.Headers))
			for k, v := range h.Headers {
				headers[k] = v
			}
			httpSpec["headers"] = headers
		}
		if h.Credentials != nil {
			httpSpec["authSecretRef"] = map[string]any{"name": h.Credentials.Secret.Name, "key": h.Credentials.Secret.Key}
			secrets[h.Credentials.Secret] = true
		}
		spec := map[string]any{"description": t.Spec.Description, "http": httpSpec}
		if t.Spec.Parameters != nil {
			spec["parameters"] = t.Spec.Parameters
		}
		doc := meta("Tool", t.Metadata.Name)
		doc["spec"] = spec
		r.Documents = append(r.Documents, doc)
	}

	for _, a := range g.Agents {
		s := a.Spec
		if g.Provider(s.Provider) == nil {
			return nil, fmt.Errorf("Agent %q uses Provider %q, which is not in the rendered graph", a.Metadata.Name, s.Provider)
		}
		spec := map[string]any{
			"providerRef":  map[string]any{"name": s.Provider, "namespace": namespace},
			"systemPrompt": map[string]any{"inline": s.Instructions},
		}
		if len(s.Tools) > 0 {
			tools := make([]any, 0, len(s.Tools))
			for _, name := range s.Tools {
				if g.Tool(name) == nil {
					return nil, fmt.Errorf("Agent %q uses Tool %q, which is not in the rendered graph", a.Metadata.Name, name)
				}
				tools = append(tools, map[string]any{"name": name})
			}
			spec["tools"] = tools
		}
		// Orka treats an empty allowedAgents list as "any Agent", so
		// coordination is rendered only with an explicit, non-empty list.
		if len(s.AllowedAgents) > 0 {
			allowed := make([]any, 0, len(s.AllowedAgents))
			for _, name := range s.AllowedAgents {
				if g.Agent(name) == nil {
					return nil, fmt.Errorf("Agent %q may delegate to Agent %q, which is not in the rendered graph", a.Metadata.Name, name)
				}
				allowed = append(allowed, map[string]any{"name": name})
			}
			spec["coordination"] = map[string]any{"enabled": true, "allowedAgents": allowed}
		}
		doc := meta("Agent", a.Metadata.Name)
		if s.Description != "" {
			doc["metadata"].(map[string]any)["annotations"] = map[string]any{descriptionAnnotation: s.Description}
		}
		doc["spec"] = spec
		r.Documents = append(r.Documents, doc)
	}

	for ref := range secrets {
		r.Secrets = append(r.Secrets, ref)
	}
	sort.Slice(r.Secrets, func(i, j int) bool {
		if r.Secrets[i].Name != r.Secrets[j].Name {
			return r.Secrets[i].Name < r.Secrets[j].Name
		}
		return r.Secrets[i].Key < r.Secrets[j].Key
	})
	if err := scanRendered(r.Documents); err != nil {
		return nil, err
	}
	return r, nil
}

// YAML returns the documents as one deterministic multi-document stream.
func (r *Rendered) YAML() (string, error) {
	var docs []string
	for _, doc := range r.Documents {
		out, err := yaml.Marshal(doc)
		if err != nil {
			return "", err
		}
		docs = append(docs, string(out))
	}
	return strings.Join(docs, "---\n"), nil
}

func rateLimit(l *krm.RateLimits) map[string]any {
	if l == nil {
		return nil
	}
	out := map[string]any{}
	if l.RequestsPerMinute != nil {
		out["requestsPerMinute"] = *l.RequestsPerMinute
	}
	if l.TokensPerMinute != nil {
		out["tokensPerMinute"] = *l.TokensPerMinute
	}
	return out
}

// scanRendered refuses output carrying a credential shape. The bundle's
// sources were scanned when loaded; this catches a shape created by joining
// values that were each harmless alone.
func scanRendered(docs []map[string]any) error {
	data, err := json.Marshal(docs)
	if err != nil {
		return fmt.Errorf("rendered Orka resources are not JSON-compatible: %w", err)
	}
	if shape := secretshapes.Match(string(data)); shape != nil {
		return fmt.Errorf("refusing rendered Orka resources containing something shaped like %s", shape.What)
	}
	return nil
}
