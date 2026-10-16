package app

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

// suiteSourceRevision returns the HEAD commit if it holds exactly the files
// push packs from dir, or why not. Like lift, it compares bytes with HEAD:
// git status can hide edits and ignores untracked files, which are packed.
func suiteSourceRevision(ctx context.Context, dir string) (*suiteoras.ProvenanceSource, string) {
	return sourceRevision(ctx, dir, nil)
}

// sourceRevision shares Git discovery and reproducibility checks. A workspace
// supplies fingerprint-bound raw inputs; extracted suites match the whole tree.
func sourceRevision(ctx context.Context, dir string, inputs map[string]workspace.SourceFile) (*suiteoras.ProvenanceSource, string) {
	gitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, "suite directory could not be resolved"
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, "suite directory could not be resolved"
	}
	git := func(stdin []byte, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(gitCtx, "git", append([]string{"--literal-pathspecs", "-C", canonical}, args...)...)
		cmd.Stdin = bytes.NewReader(stdin)
		// Hooks and callers can select another repository even with -C.
		// Keep unrelated settings, but discover the source repository afresh.
		cmd.Env = make([]string, 0, len(os.Environ()))
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			switch key {
			case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
				"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE",
				"GIT_IMPLICIT_WORK_TREE", "GIT_PREFIX", "GIT_GRAFT_FILE",
				"GIT_SHALLOW_FILE", "GIT_REPLACE_REF_BASE":
				continue
			}
			cmd.Env = append(cmd.Env, entry)
		}
		return cmd.Output()
	}
	rootRaw, err := git(nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, "not in a Git repository"
	}
	root, err := filepath.EvalSymlinks(strings.TrimSpace(string(rootRaw)))
	if err != nil {
		return nil, "Git repository root could not be resolved"
	}
	rel, err := filepath.Rel(root, canonical)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, "suite directory is outside the Git repository"
	}
	rel = filepath.ToSlash(rel)
	if !suiteoras.SafeProvenanceText(rel) {
		return nil, "suite path is not printable text"
	}
	commitRaw, err := git(nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, "repository has no HEAD commit"
	}
	commit := strings.TrimSpace(string(commitRaw))
	newHash := sha1.New
	if format, err := git(nil, "rev-parse", "--show-object-format"); err == nil && strings.TrimSpace(string(format)) == "sha256" {
		newHash = sha256.New
	}
	treeArgs := []string{"ls-tree", "-r", "-z", "--full-tree", commit}
	if rel != "." {
		treeArgs = append(treeArgs, "--", rel)
	}
	treeRaw, err := git(nil, treeArgs...)
	if err != nil {
		return nil, "HEAD tree could not be read"
	}
	committed, err := parseLsTree(treeRaw, rel, inputs)
	if err != nil {
		return nil, err.Error()
	}
	if len(committed) == 0 {
		return nil, "suite directory is not in HEAD"
	}
	var reason string
	if inputs == nil {
		reason = matchCommittedTree(gitCtx, canonical, committed, newHash)
	} else {
		reason = matchWorkspaceTree(gitCtx, inputs, committed, newHash)
	}
	if reason != "" {
		return nil, reason
	}
	// These attributes make a checkout differ from the blob.
	var paths []byte
	for name := range committed {
		if inputs != nil {
			if _, selected := inputs[name]; !selected {
				continue
			}
		}
		full := name
		if rel != "." {
			full = rel + "/" + name
		}
		paths = append(append(paths, full...), 0)
	}
	attrs, err := git(paths, "-C", root, "check-attr", "-z", "--stdin", "eol", "filter", "ident", "working-tree-encoding")
	if err != nil {
		return nil, "Git attributes could not be read"
	}
	if reason := checkoutRewrites(attrs); reason != "" {
		return nil, reason
	}
	if inputs != nil {
		// Unselected .gitattributes edits must not mask rewriting rules at HEAD.
		attrs, err := git(paths, "-C", root, "check-attr", "--source="+commit, "-z", "--stdin", "eol", "filter", "ident", "working-tree-encoding")
		if err != nil {
			return nil, "Git attributes at HEAD could not be read; workspace source claims require Git 2.40+ with check-attr --source support"
		}
		if reason := checkoutRewrites(attrs); reason != "" {
			return nil, reason
		}
	}
	source := &suiteoras.ProvenanceSource{Commit: commit, Path: rel}
	if remote, err := git(nil, "remote", "get-url", "origin"); err == nil {
		if uri := publicRepositoryURI(strings.TrimSpace(string(remote))); suiteoras.SafeProvenanceText(uri) {
			source.URI = uri
		}
	}
	return source, ""
}

// checkoutRewrites names the first path with a rewriting attribute.
func checkoutRewrites(raw []byte) string {
	fields := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	for i := 0; i+2 < len(fields); i += 3 {
		name, attribute, value := fields[i], fields[i+1], fields[i+2]
		if value == "unspecified" || value == "unset" {
			continue
		}
		return fmt.Sprintf("%s has Git attribute %s=%s, so a checkout may not match the packed bytes", name, attribute, value)
	}
	return ""
}

type committedEntry struct {
	mode   string
	object string
}

// parseLsTree maps suite-relative paths to HEAD mode and object ID.
func parseLsTree(raw []byte, rel string, inputs map[string]workspace.SourceFile) (map[string]committedEntry, error) {
	entries := map[string]committedEntry{}
	prefix := ""
	if rel != "." {
		prefix = rel + "/"
	}
	for _, record := range bytes.Split(raw, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, name, ok := strings.Cut(string(record), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || !strings.HasPrefix(name, prefix) {
			return nil, errors.New("HEAD tree could not be parsed")
		}
		name = strings.TrimPrefix(name, prefix)
		if inputs != nil {
			_, selected := inputs[name]
			if !selected && !strings.HasPrefix(name, "agents/") && !strings.HasPrefix(name, "eval/") {
				continue
			}
		}
		if fields[1] != "blob" {
			return nil, errors.New("suite directory contains a Git submodule")
		}
		entries[name] = committedEntry{mode: fields[0], object: fields[2]}
	}
	return entries, nil
}

// matchCommittedTree reports why the packed files differ from HEAD, or "".
func matchCommittedTree(ctx context.Context, dir string, committed map[string]committedEntry, newHash func() hash.Hash) string {
	seen := map[string]bool{}
	nonEmpty := map[string]bool{}
	var directories []string
	err := filepath.WalkDir(dir, func(full string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name, err := filepath.Rel(dir, full)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		if name == "." {
			return nil
		}
		if entry.IsDir() {
			directories = append(directories, name)
			return nil
		}
		want, tracked := committed[name]
		if !tracked {
			return fmt.Errorf("%s is not in HEAD", name)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var mode string
		var data []byte
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			mode, data = "120000", []byte(filepath.ToSlash(target))
		case info.Mode().IsRegular():
			mode = "100644"
			if info.Mode().Perm()&0o111 != 0 {
				mode = "100755"
			}
			if data, err = os.ReadFile(full); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s has a file type Git cannot record", name)
		}
		if mode != want.mode || gitObjectID(newHash, data) != want.object {
			return fmt.Errorf("%s differs from HEAD", name)
		}
		seen[name] = true
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			nonEmpty[parent] = true
		}
		return nil
	})
	if err != nil {
		return err.Error()
	}
	for name := range committed {
		if !seen[name] {
			return name + " is in HEAD but missing from the directory"
		}
	}
	// Packing includes empty directories; Git cannot record them.
	for _, directory := range directories {
		if !nonEmpty[directory] {
			return directory + " is an empty directory, which Git cannot record"
		}
	}
	return ""
}

// matchWorkspaceTree compares frozen authored bytes, not generated OCI files or
// a later live tree. HEAD must also reproduce the case and one-member inventory.
func matchWorkspaceTree(ctx context.Context, inputs map[string]workspace.SourceFile, committed map[string]committedEntry, newHash func() hash.Hash) string {
	member := ""
	for name := range inputs {
		if strings.HasPrefix(name, "agents/") && strings.Count(name, "/") == 2 && strings.HasSuffix(name, "/member.yaml") {
			member = path.Dir(name) + "/"
		}
	}
	for name := range committed {
		if strings.HasPrefix(name, "eval/") {
			if _, selected := inputs[name]; !selected {
				return name + " is in HEAD but missing from selected cases"
			}
		}
		if strings.HasPrefix(name, "agents/") && !strings.HasPrefix(name, member) {
			return "HEAD does not reproduce the selected one-member inventory"
		}
	}
	for name, file := range inputs {
		if err := ctx.Err(); err != nil {
			return err.Error()
		}
		want, tracked := committed[name]
		if !tracked {
			return name + " is not in HEAD"
		}
		mode := "100644"
		if file.Mode.Perm()&0o111 != 0 {
			mode = "100755"
		}
		if mode != want.mode || gitObjectID(newHash, file.Data) != want.object {
			return name + " differs from HEAD"
		}
	}
	return ""
}

func gitObjectID(newHash func() hash.Hash, data []byte) string {
	h := newHash()
	h.Write([]byte("blob " + strconv.Itoa(len(data)) + "\x00"))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// publicRepositoryURI returns a credential-free git+https URI, or "".
func publicRepositoryURI(remote string) string {
	if user, rest, ok := strings.Cut(remote, "@"); ok && user == "git" && !strings.Contains(remote, "://") {
		host, repo, ok := strings.Cut(rest, ":")
		if !ok || host == "" || repo == "" {
			return ""
		}
		remote = "https://" + host + "/" + repo
	}
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	return "git+" + parsed.String()
}
