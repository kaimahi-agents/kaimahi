package agentsuite

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
)

// DeploymentMember is one verified agent and its exact platform selection.
// Instructions are verified bytes, not a path for an adapter to reopen.
type DeploymentMember struct {
	Agent             Agent        `json:"agent"`
	Instructions      string       `json:"instructions"`
	Composition       Composition  `json:"composition"`
	CompositionDigest string       `json:"compositionDigest"`
	BuildProfile      BuildProfile `json:"buildProfile"`
}

// DeploymentSuite is a validated snapshot. Accessors return independent copies.
// LogicalDigest and an OCI artifact digest are separate identity domains.
type DeploymentSuite struct {
	suite     Suite
	digest    string
	members   []DeploymentMember
	providers []ToolProvider
}

func (s *DeploymentSuite) Name() string          { return s.suite.Name }
func (s *DeploymentSuite) LogicalDigest() string { return s.digest }
func (s *DeploymentSuite) Members() []DeploymentMember {
	data, _ := json.Marshal(s.members)
	var out []DeploymentMember
	_ = json.Unmarshal(data, &out)
	return out
}
func (s *DeploymentSuite) ToolProviders() []ToolProvider {
	data, _ := json.Marshal(s.providers)
	var out []ToolProvider
	_ = json.Unmarshal(data, &out)
	return out
}

// ResolveDeploymentSuite selects every agent on one platform. A missing member
// composition fails the entire request; there is no partial selection mode.
func ResolveDeploymentSuite(root string, platform Platform) (*DeploymentSuite, error) {
	if err := validatePlatform(platform); err != nil {
		return nil, err
	}
	content, err := loadDirectory(root)
	if err != nil {
		return nil, err
	}
	if _, err := validateContent(content); err != nil {
		return nil, err
	}
	decode := func(name string, target any) error {
		data, err := content.data(name)
		if err != nil {
			return err
		}
		return decodeStrict(data, target)
	}
	var suite Suite
	if err := decode("agentsuite.json", &suite); err != nil {
		return nil, err
	}
	raw, err := content.data("agentsuite.json")
	if err != nil {
		return nil, err
	}
	digest, err := canonicalDigest(raw)
	if err != nil {
		return nil, err
	}
	result := &DeploymentSuite{suite: suite, digest: digest}
	profiles := map[string]BuildProfile{}
	for _, ref := range suite.BuildProfiles {
		var profile BuildProfile
		if err := decode(ref.Path, &profile); err != nil {
			return nil, err
		}
		profiles[ref.ID] = profile
	}
	var catalog ToolProviderCatalog
	if err := decode(suite.ToolProviderCatalog.Path, &catalog); err != nil {
		return nil, err
	}
	for _, ref := range catalog.ToolProviders {
		var provider ToolProvider
		if err := decode(ref.Path, &provider); err != nil {
			return nil, err
		}
		result.providers = append(result.providers, provider)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer rootHandle.Close()
	for _, ref := range suite.Agents {
		var agent Agent
		if err := decode(ref.Path, &agent); err != nil {
			return nil, err
		}
		var selected *CompositionRef
		for i := range suite.Compositions {
			candidate := &suite.Compositions[i]
			if candidate.Agent == agent.ID && candidate.Platform == platform {
				if selected != nil {
					return nil, errors.New("ambiguous member composition")
				}
				selected = candidate
			}
		}
		if selected == nil {
			return nil, fmt.Errorf("agent %s has no composition for %s; the whole suite must be supported", agent.ID, platform)
		}
		var composition Composition
		if err := decode(selected.Path, &composition); err != nil {
			return nil, err
		}
		entry, ok := content.entries[agent.Instructions.Path]
		if !ok || entry.Size > maxJSONBytes {
			return nil, fmt.Errorf("agent %s instructions exceed deployment input limit", agent.ID)
		}
		file, err := rootHandle.Open(agent.Instructions.Path)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxJSONBytes+1))
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		if int64(len(data)) != entry.Size || digestBytes(data) != agent.Instructions.Digest {
			return nil, errors.New("instructions changed after suite validation")
		}
		result.members = append(result.members, DeploymentMember{Agent: agent, Instructions: string(data), Composition: composition, CompositionDigest: selected.Digest, BuildProfile: profiles[composition.BuildProfile]})
	}
	slices.SortFunc(result.members, func(a, b DeploymentMember) int {
		if a.Agent.ID < b.Agent.ID {
			return -1
		}
		if a.Agent.ID > b.Agent.ID {
			return 1
		}
		return 0
	})
	return result, nil
}
