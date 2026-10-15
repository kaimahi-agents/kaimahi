package workspace

import (
	"bytes"
	"errors"
	"slices"
	"strings"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	godigest "github.com/opencontainers/go-digest"
)

const (
	maxMembers     = 16
	maxCases       = 128
	maxNativeBytes = 8 << 20
	// Root, shared profile/catalog, metadata, three files per member and cases.
	maxNativeFiles       = 4 + 3*maxMembers + maxCases
	multiNativePath      = "metadata/kmx/native-multi.json"
	multiRendererVersion = "kmx.native-multi/v1"
)

// ErrMultiMemberUnsupported refuses operational use of an offline-only inventory.
var ErrMultiMemberUnsupported = errors.New("multi-member workspaces support offline prepare/load only")

type multiRecord struct {
	SchemaVersion   int            `json:"schemaVersion"`
	RendererVersion string         `json:"rendererVersion"`
	Members         []nativeRecord `json:"members"`
}

func nativeBounds(files map[string][]byte) error {
	if len(files) > maxNativeFiles {
		return errors.New("native artifact exceeds file count limit")
	}
	total := 0
	for _, b := range files {
		total += len(b)
		if len(b) > maxFile || total > maxNativeBytes {
			return errors.New("native artifact exceeds size limit")
		}
	}
	return nil
}

// Each member uses the unchanged singleton resolver. Only the aggregate root and
// private inventory differ; no singleton execution declaration survives the merge.
func resolveMulti(name string, members []normalizedMember, profile []byte) (map[string][]byte, error) {
	if len(members) < 2 || len(members) > maxMembers {
		return nil, errors.New("two to 16 members are required")
	}
	members = slices.Clone(members)
	slices.SortFunc(members, func(a, b normalizedMember) int { return strings.Compare(a.member.ID, b.member.ID) })
	files := map[string][]byte{}
	metadata := multiRecord{SchemaVersion: 1, RendererVersion: multiRendererVersion, Members: []nativeRecord{}}
	var suite spec.Suite
	caseCount := 0
	for i, m := range members {
		if i > 0 && members[i-1].member.ID == m.member.ID {
			return nil, errors.New("duplicate native member")
		}
		caseCount += len(m.cases)
		if caseCount > maxCases {
			return nil, errors.New("workspace requires at most 128 total cases")
		}
		single, err := resolve(name, m.member, m.instructions, profile, m.cases)
		if err != nil {
			return nil, err
		}
		record, _, err := nativeProfile(single)
		if err != nil {
			return nil, err
		}
		var root spec.Suite
		if err := spec.Unmarshal(single["agentsuite.json"], &root); err != nil {
			return nil, err
		}
		if i == 0 {
			suite = root
		} else {
			suite.Agents = append(suite.Agents, root.Agents...)
			suite.Compositions = append(suite.Compositions, root.Compositions...)
		}
		delete(single, "agentsuite.json")
		delete(single, nativePath)
		delete(single, kagentNativePath)
		delete(single, agentsessionsNativePath)
		for j, c := range record.Cases {
			newPath := "metadata/kmx/eval/" + m.member.ID + "/" + strings.TrimPrefix(c.Path, "metadata/kmx/eval/")
			single[newPath] = single[c.Path]
			delete(single, c.Path)
			record.Cases[j].Path = newPath
		}
		for path, b := range single {
			if existing, ok := files[path]; ok && !bytes.Equal(existing, b) {
				return nil, errors.New("conflicting native graph entries")
			}
			files[path] = b
		}
		metadata.Members = append(metadata.Members, record)
	}
	root, err := spec.Marshal(suite)
	if err != nil {
		return nil, err
	}
	files["agentsuite.json"] = root
	b, err := spec.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	files[multiNativePath] = b
	if err := nativeBounds(files); err != nil {
		return nil, err
	}
	return files, nil
}

// Regeneration compares the entire file map, including every reference, digest,
// metadata field and raw case. Extra public extensions cannot be silently dropped.
func validateMulti(files map[string][]byte) (string, error) {
	var suite spec.Suite
	if err := spec.Unmarshal(files["agentsuite.json"], &suite); err != nil {
		return "", err
	}
	var metadata multiRecord
	if err := spec.Unmarshal(files[multiNativePath], &metadata); err != nil {
		return "", err
	}
	if metadata.SchemaVersion != 1 || metadata.RendererVersion != multiRendererVersion || len(metadata.Members) < 2 || len(metadata.Members) > maxMembers {
		return "", errors.New("unsupported native multi inventory")
	}
	members := make([]normalizedMember, 0, len(metadata.Members))
	caseCount := 0
	for _, record := range metadata.Members {
		var agent spec.Agent
		if err := spec.Unmarshal(files["agents/"+record.Member+".json"], &agent); err != nil {
			return "", err
		}
		m := sourceMember{ID: agent.ID, Description: agent.Description}
		m.Model.Protocol, m.Model.Model = agent.Model.Protocol, agent.Model.Model
		if record.Runtime != nil {
			p := record.Runtime
			m.Runtime = &sourceRuntime{ID: p.ID, Version: &p.Version, Engine: &p.Engine}
		}
		prefix := "metadata/kmx/eval/" + m.ID + "/"
		cases := map[string][]byte{}
		caseCount += len(record.Cases)
		if caseCount > maxCases {
			return "", errors.New("native inventory exceeds 128 total cases")
		}
		for _, c := range record.Cases {
			name, scoped := strings.CutPrefix(c.Path, prefix)
			b, exists := files[c.Path]
			if !scoped || !exists || !caseName(name) || cases[name] != nil || godigest.FromBytes(b).String() != c.Digest {
				return "", errors.New("native case path/digest/coverage mismatch")
			}
			cases[name] = b
		}
		members = append(members, normalizedMember{member: m, instructions: files["instructions/"+m.ID+".md"], cases: cases})
	}
	expected, err := resolveMulti(suite.Name, members, files["build-profiles/default.json"])
	if err != nil {
		return "", err
	}
	if !equalFiles(expected, files) {
		return "", errors.New("unsupported native graph or incomplete case coverage")
	}
	return suite.Name, nil
}
