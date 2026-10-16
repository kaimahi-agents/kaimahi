package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	godigest "github.com/opencontainers/go-digest"
	"go.yaml.in/yaml/v3"
)

// Source layout differences end here; resolution consumes only normalized inputs.
type normalizedMember struct {
	member       sourceMember
	instructions []byte
	cases        []runtime.EvaluationCase
}
type normalizedSource struct {
	workspace sourceWorkspace
	members   []normalizedMember
	profile   []byte
	selected  map[string]string
	source    map[string]SourceFile
}

func normalizeSource(ctx context.Context, root string) (*normalizedSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := safeAbsolute(root)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	s := &normalizedSource{selected: map[string]string{}, source: map[string]SourceFile{}}
	total := 0
	read := func(name string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b, err := readBounded(r, name)
		if err != nil {
			return nil, err
		}
		info, err := r.Lstat(name)
		if err != nil {
			return nil, err
		}
		total += len(b)
		if total > maxNativeBytes {
			return nil, errors.New("workspace source exceeds 8 MiB")
		}
		s.selected[name] = godigest.FromBytes(b).String()
		s.source[name] = SourceFile{Data: b, Mode: info.Mode()}
		return b, nil
	}
	wb, err := read("workspace.yaml")
	if err != nil {
		return nil, err
	}
	w, err := parseSourceWorkspace(wb)
	if err != nil {
		return nil, err
	}
	s.workspace = w
	paths := []string{w.Member}
	if w.SchemaVersion == 2 {
		paths = slices.Clone(w.Members)
	}
	slices.Sort(paths)
	ids := make([]string, len(paths))
	for i, p := range paths {
		ids[i] = strings.Split(p, "/")[1]
	}
	if err := memberDirectories(r, "agents", ids); err != nil {
		return nil, err
	}
	if w.SchemaVersion == 2 {
		if err := memberDirectories(r, "eval", ids); err != nil {
			return nil, err
		}
	}
	s.profile, err = read(w.BuildProfile)
	if err != nil {
		return nil, err
	}
	caseCount := 0
	for _, memberPath := range paths {
		mb, err := read(memberPath)
		if err != nil {
			return nil, err
		}
		var m sourceMember
		if err := strictYAML(mb, &m); err != nil {
			return nil, fmt.Errorf("member: %w", err)
		}
		if memberPath != "agents/"+m.ID+"/member.yaml" {
			return nil, errors.New("workspace member path and ID disagree")
		}
		// Check before joining so cleaning cannot conceal an authored escape.
		if !fs.ValidPath(m.Instructions) || strings.Contains(m.Instructions, "\\") {
			return nil, errors.New("instructions path must be member-relative")
		}
		instructions, err := read(path.Dir(memberPath) + "/" + m.Instructions)
		if err != nil {
			return nil, err
		}
		if err := validateMember(m, instructions); err != nil {
			return nil, err
		}
		if m.Policy.Kind != 0 {
			if m.Policy.Kind != yaml.ScalarNode || m.Policy.Tag != "!!str" || !fs.ValidPath(m.Policy.Value) || strings.Contains(m.Policy.Value, "\\") {
				return nil, errors.New("policy path must be a nonempty member-relative string")
			}
			if profile, _ := m.Runtime.profile(); profile.ID != "orka" {
				return nil, errors.New("policy authoring supports Orka members only")
			}
			raw, err := read(path.Dir(memberPath) + "/" + m.Policy.Value)
			if err != nil {
				return nil, err
			}
			m.policyBytes, err = canonicalMemberPolicy(raw)
			if err != nil {
				return nil, err
			}
		}
		caseDir := "eval"
		if w.SchemaVersion == 2 {
			caseDir += "/" + m.ID
		}
		caseEntries, err := entries(r, caseDir, maxCases)
		if err != nil {
			return nil, err
		}
		caseCount += len(caseEntries)
		if caseCount > maxCases {
			return nil, errors.New("workspace requires at most 128 total cases")
		}
		cases := map[string][]byte{}
		for _, entry := range caseEntries {
			if !caseName(entry.Name()) || !entry.Type().IsRegular() {
				return nil, errors.New("eval entries must be regular YAML files without subdirectories")
			}
			b, err := read(caseDir + "/" + entry.Name())
			if err != nil {
				return nil, err
			}
			cases[entry.Name()] = b
		}
		parsed, err := authoredCases(cases)
		if err != nil {
			return nil, err
		}
		s.members = append(s.members, normalizedMember{member: m, instructions: instructions, cases: parsed})
	}
	return s, nil
}

func memberDirectories(r *os.Root, dir string, ids []string) error {
	children, err := entries(r, dir, len(ids))
	if err != nil {
		return err
	}
	if len(children) != len(ids) {
		return errors.New("workspace member directory closure mismatch")
	}
	for _, child := range children {
		if !child.IsDir() || !slices.Contains(ids, child.Name()) {
			return errors.New("workspace member directory closure mismatch")
		}
	}
	return nil
}
