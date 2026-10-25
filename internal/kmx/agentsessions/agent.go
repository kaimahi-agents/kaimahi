package agentsessions

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const AgentAPIVersion = "agents.aks-durable-agents.io/v1alpha1"
const AgentPlural = "agents.agents.aks-durable-agents.io"

// AgentSpec is the immutable v1.1 controller contract, not host configuration.
type AgentSpec struct {
	Package struct {
		Reference string `json:"reference"`
	} `json:"package"`
	Member          string `json:"member"`
	ModelBindingRef struct {
		Name string `json:"name"`
	} `json:"modelBindingRef"`
}

type Agent struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name              string          `json:"name"`
		Namespace         string          `json:"namespace"`
		UID               string          `json:"uid,omitempty"`
		Generation        int64           `json:"generation,omitempty"`
		ResourceVersion   string          `json:"resourceVersion,omitempty"`
		DeletionTimestamp json.RawMessage `json:"deletionTimestamp,omitempty"`
	} `json:"metadata"`
	Spec   AgentSpec `json:"spec"`
	Status struct {
		AgentUID           string `json:"agentUID"`
		ObservedGeneration int64  `json:"observedGeneration"`
		PackageDigest      string `json:"packageDigest"`
		Member             string `json:"member"`
		ServedDigest       string `json:"servedDigest"`
		Conditions         []struct {
			Type               string `json:"type"`
			Status             string `json:"status"`
			ObservedGeneration int64  `json:"observedGeneration"`
		} `json:"conditions"`
	} `json:"status,omitempty"`
}

// DecodeAgent rejects ambiguous JSON and unknown spec fields, while allowing
// ordinary API-server metadata and future controller status fields. Status
// messages and host implementation details are intentionally not retained.
func DecodeAgent(raw []byte) (Agent, error) {
	var a Agent
	var doc map[string]any
	bad := errors.New("invalid Agent resource")
	if len(raw) > 4<<20 || suitecore.Unmarshal(raw, &doc) != nil || doc == nil {
		return a, bad
	}
	if _, err := agentIdentity(doc); err != nil {
		return a, bad
	}
	// encoding/json accepts case-folded aliases. Project exact API keys before
	// typed decoding so an alias cannot acknowledge a missing contract field.
	projected := agentKeys(doc, "apiVersion", "kind", "metadata", "spec", "status").(map[string]any)
	projected["metadata"] = agentKeys(doc["metadata"], "name", "namespace", "uid", "generation", "resourceVersion", "deletionTimestamp")
	projected["status"] = agentKeys(doc["status"], "agentUID", "observedGeneration", "packageDigest", "member", "servedDigest", "conditions")
	if status, ok := projected["status"].(map[string]any); ok {
		if rows, ok := status["conditions"].([]any); ok {
			if len(rows) > 64 {
				return a, bad
			}
			for i, c := range rows {
				rows[i] = agentKeys(c, "type", "status", "observedGeneration")
			}
		}
	}
	encoded, _ := json.Marshal(projected)
	if json.Unmarshal(encoded, &a) != nil {
		return a, bad
	}
	if _, err := HarnessImageDigest(a.Spec.Package.Reference); err != nil || !agentSafeText(a.Spec.Member) || a.Spec.ModelBindingRef.Name != "default" {
		return a, bad
	}
	seen := map[string]bool{}
	for _, c := range a.Status.Conditions {
		if (c.Type != "PackageVerified" && c.Type != "Registered") || seen[c.Type] || (c.Status != "True" && c.Status != "False" && c.Status != "Unknown") || c.ObservedGeneration < 0 {
			return a, bad
		}
		seen[c.Type] = true
	}
	canonical, _ := json.Marshal(a.Spec)
	var specMap map[string]any
	_ = json.Unmarshal(canonical, &specMap)
	if !reflect.DeepEqual(doc["spec"], specMap) {
		return a, bad
	}
	return a, nil
}

// DecodeAgentIdentity retains an acknowledged create identity independently of
// admission's spec/status. It grants no spec, registration or reuse authority.
func DecodeAgentIdentity(raw []byte) (Agent, error) {
	var doc map[string]any
	if len(raw) > 4<<20 || suitecore.Unmarshal(raw, &doc) != nil || doc == nil {
		return Agent{}, errors.New("invalid Agent identity")
	}
	return agentIdentity(doc)
}
func agentIdentity(doc map[string]any) (Agent, error) {
	var a Agent
	projection := agentKeys(doc, "apiVersion", "kind", "metadata").(map[string]any)
	projection["metadata"] = agentKeys(doc["metadata"], "name", "namespace", "uid", "generation", "resourceVersion", "deletionTimestamp")
	encoded, _ := json.Marshal(projection)
	if json.Unmarshal(encoded, &a) != nil || a.APIVersion != AgentAPIVersion || a.Kind != "Agent" || scaffold.ValidateObjectName(a.Metadata.Name) != nil || scaffold.ValidateNamespace(a.Metadata.Namespace) != nil || a.Metadata.Generation < 1 || !agentSafeText(a.Metadata.UID) {
		return a, errors.New("invalid Agent identity")
	}
	return a, nil
}

func agentKeys(value any, keys ...string) any {
	m, ok := value.(map[string]any)
	if !ok {
		return value
	}
	out := map[string]any{}
	for _, key := range keys {
		if v, ok := m[key]; ok {
			out[key] = v
		}
	}
	return out
}
func agentSafeText(s string) bool {
	return s != "" && len(s) <= 1024 && utf8.ValidString(s) && strings.TrimSpace(s) == s && strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) < 0 && scaffold.RefuseKeyShapes(s) == nil
}

// AgentRegistered means last controller-observed admission only. It is neither
// a live-host availability check nor execution or target evaluation evidence.
func AgentRegistered(a Agent) bool {
	_, digest, ok := strings.Cut(a.Spec.Package.Reference, "@")
	if !ok || a.Metadata.UID == "" || a.Metadata.Generation < 1 || len(a.Metadata.DeletionTimestamp) > 0 && string(a.Metadata.DeletionTimestamp) != "null" || a.Status.AgentUID != a.Metadata.UID || a.Status.ObservedGeneration != a.Metadata.Generation || a.Status.Member != a.Spec.Member || a.Status.PackageDigest != digest || a.Status.ServedDigest != digest {
		return false
	}
	seen := map[string]bool{}
	for _, c := range a.Status.Conditions {
		if c.Type != "PackageVerified" && c.Type != "Registered" {
			return false
		}
		if seen[c.Type] || c.Status != "True" || c.ObservedGeneration != a.Metadata.Generation {
			return false
		}
		seen[c.Type] = true
	}
	return seen["PackageVerified"] && seen["Registered"]
}
