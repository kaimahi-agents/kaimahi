package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

func makeCopilotdSnapshot(ctx context.Context, pair *agentfile.Pair, check func(...string) error) (agentpreview.SourceSnapshot, error) {
	return copilotdSnapshots(pair)(ctx, check)
}

// Each invocation freezes a baseline; private copies are never reused by cases.
func copilotdSnapshots(pair *agentfile.Pair) func(context.Context, func(...string) error) (agentpreview.SourceSnapshot, error) {
	baseline := ""
	return func(ctx context.Context, check func(...string) error) (result agentpreview.SourceSnapshot, err error) {
		fail := func(cause error) (agentpreview.SourceSnapshot, error) {
			if ctx.Err() != nil {
				cause = ctx.Err()
			}
			return agentpreview.SourceSnapshot{}, cause
		}
		current := func() error {
			if pair == nil {
				return agentpreview.ErrSnapshotUnavailable
			}
			override := ""
			if pair.Tests.Selection.Explicit {
				override = pair.Tests.Selection.Path
			}
			fresh, e := agentfile.LoadCopilotdPair(pair.AgentPath, override)
			if e != nil || !pair.SameSourceVersion(fresh) {
				return agentpreview.ErrSourceChanged
			}
			return nil
		}
		if e := current(); e != nil {
			return fail(e)
		}
		raw, e := copilotdGit(ctx, filepath.Dir(pair.AgentPath), "rev-parse", "--show-toplevel")
		if e != nil {
			return fail(agentpreview.ErrSnapshotUnavailable)
		}
		root := strings.TrimSuffix(string(raw), "\n")
		relative, e := filepath.Rel(root, pair.AgentPath)
		if e != nil || !filepath.IsAbs(root) || !copilotdRelative(relative) {
			return fail(agentpreview.ErrSnapshotUnavailable)
		}
		head := func() (string, error) {
			raw, e := copilotdGit(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD")
			var exit *exec.ExitError
			if e != nil && (!errors.As(e, &exit) || exit.ExitCode() != 1) {
				return "", agentpreview.ErrSnapshotUnavailable
			}
			return strings.TrimSpace(string(raw)), nil
		}
		revision, e := head()
		if e != nil {
			return fail(e)
		}
		inventory := func() ([]string, error) {
			stage, e := copilotdGit(ctx, root, "ls-files", "--stage", "-z")
			if e != nil {
				return nil, agentpreview.ErrSnapshotUnavailable
			}
			for _, entry := range bytes.Split(stage, []byte{0}) {
				if bytes.HasPrefix(entry, []byte("160000 ")) {
					return nil, agentpreview.ErrSubmodulesUnsupported
				}
			}
			raw, e := copilotdGit(ctx, root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
			if e != nil {
				return nil, agentpreview.ErrSnapshotUnavailable
			}
			names := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
			if len(raw) == 0 {
				names = nil
			}
			slices.Sort(names)
			names = slices.Compact(names)
			if slices.Contains(names, ".gitmodules") {
				declarations, e := copilotdGit(ctx, root, "config", "--file", ".gitmodules", "--get-regexp", `^submodule\..*\.path$`)
				var exit *exec.ExitError
				if e != nil && (!errors.As(e, &exit) || exit.ExitCode() != 1) {
					return nil, agentpreview.ErrSnapshotUnavailable
				}
				if len(declarations) > 0 {
					return nil, agentpreview.ErrSubmodulesUnsupported
				}
			}
			for _, name := range names {
				if !copilotdRelative(name) {
					return nil, agentpreview.ErrSnapshotUnavailable
				}
			}
			if !slices.Contains(names, filepath.ToSlash(relative)) {
				return nil, agentpreview.ErrSnapshotUnavailable
			}
			return names, nil
		}
		scan := func(destination string) (string, error) {
			names, e := inventory()
			if e != nil {
				return "", e
			}
			hash := sha256.New()
			for _, name := range names {
				if e := copyCopilotdEntry(ctx, root, destination, name, hash, check); e != nil {
					return "", e
				}
			}
			return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
		}
		if _, e := inventory(); e != nil {
			return fail(e)
		}
		directory, e := os.MkdirTemp("", "kmx-copilotd-")
		if e != nil {
			return fail(agentpreview.ErrSnapshotUnavailable)
		}
		created, e := os.Lstat(directory)
		if e != nil {
			return fail(agentpreview.ErrSnapshotUnavailable)
		}
		closed := false
		closeSnapshot := func(ctx context.Context) error {
			if closed {
				return nil
			}
			handle, e := fileaccess.OpenDirectory(ctx, directory)
			if e != nil {
				return agentpreview.ErrSnapshotUnavailable
			}
			info, e := handle.Stat()
			handle.Close()
			if e != nil || !os.SameFile(created, info) {
				return agentpreview.ErrSnapshotUnavailable
			}
			if e = os.RemoveAll(directory); e == nil {
				closed = true
			}
			return e
		}
		defer func() {
			if err != nil {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				err = errors.Join(err, closeSnapshot(cleanup))
			}
		}()
		digest, e := scan(directory)
		if e != nil {
			return fail(e)
		}
		identity := root + "\x00" + revision + "\x00" + digest
		if baseline != "" && baseline != identity {
			return fail(agentpreview.ErrSourceChanged)
		}
		after, e := scan("")
		if e != nil {
			return fail(e)
		}
		newHead, e := head()
		if e != nil {
			return fail(e)
		}
		if after != digest || newHead != revision {
			return fail(agentpreview.ErrSourceChanged)
		}
		if e = current(); e != nil {
			return fail(e)
		}
		baseline = identity
		return agentpreview.SourceSnapshot{Directory: directory, GitRevision: revision, ContentRevision: digest, Close: closeSnapshot}, nil
	}
}

// Git receives no credentials, optional write locks or hooks. User ignore
// settings remain effective; these read-only commands do not invoke filters.
func copilotdGit(ctx context.Context, root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-C", root}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "HOME=" + os.Getenv("HOME"), "XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		return nil, e
	}
	if e = cmd.Start(); e != nil {
		return nil, e
	}
	data, e := io.ReadAll(io.LimitReader(pipe, 32<<20+1))
	if e != nil || len(data) > 32<<20 {
		cancel()
		cmd.Wait()
		return nil, agentpreview.ErrSnapshotUnavailable
	}
	if e = cmd.Wait(); e != nil {
		return nil, e
	}
	return data, nil
}
func copilotdRelative(name string) bool {
	if !filepath.IsLocal(name) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".." || part == ".git" {
			return false
		}
	}
	return filepath.ToSlash(filepath.Clean(name)) == name
}

// Checked source handles and private, unexposed exclusive destinations avoid a
// durable-publication layer. Files stream without a per-file allocation/size cap.
func copyCopilotdEntry(ctx context.Context, root, destination, name string, hash io.Writer, check func(...string) error) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	path := filepath.Join(root, filepath.FromSlash(name))
	parent, e := fileaccess.OpenDirectory(ctx, filepath.Dir(path))
	if errors.Is(e, os.ErrNotExist) {
		fmt.Fprintf(hash, "%d:%s:0:0:", len(name), name)
		return nil
	}
	if e != nil {
		return agentpreview.ErrSnapshotUnavailable
	}
	defer parent.Close()
	before, e := os.Lstat(path)
	if errors.Is(e, os.ErrNotExist) {
		fmt.Fprintf(hash, "%d:%s:0:0:", len(name), name)
		return nil
	}
	if e != nil {
		return agentpreview.ErrSnapshotUnavailable
	}
	if check != nil {
		if e = check(name); e != nil {
			return e
		}
	}
	target := ""
	if destination != "" {
		target = filepath.Join(destination, filepath.FromSlash(name))
		if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
			return e
		}
	}
	fmt.Fprintf(hash, "%d:%s:%d:%d:", len(name), name, before.Mode(), before.Size())
	if before.Mode()&os.ModeSymlink != 0 {
		link, e := os.Readlink(path)
		if e != nil {
			return e
		}
		relative := filepath.Join(filepath.Dir(name), link)
		if filepath.IsAbs(link) || !copilotdRelative(filepath.ToSlash(relative)) {
			return agentpreview.ErrSnapshotUnavailable
		}
		resolved, e := filepath.EvalSymlinks(path)
		if e == nil {
			rel, e := filepath.Rel(root, resolved)
			if e != nil || !copilotdRelative(filepath.ToSlash(rel)) {
				return agentpreview.ErrSnapshotUnavailable
			}
		} else if !errors.Is(e, os.ErrNotExist) || slices.Contains(strings.Split(filepath.ToSlash(link), "/"), "..") {
			// A dangling link with parent traversal cannot establish containment.
			return agentpreview.ErrSnapshotUnavailable
		}
		if check != nil {
			if e = check(link); e != nil {
				return e
			}
		}
		io.WriteString(hash, link)
		if target != "" {
			if e = os.Symlink(link, target); e != nil {
				return e
			}
		}
	} else {
		file, e := fileaccess.OpenRegularAt(ctx, parent, filepath.Base(path))
		if e != nil {
			return e
		}
		defer file.Close()
		info, e := file.Stat()
		if e != nil || !os.SameFile(before, info) {
			return agentpreview.ErrSourceChanged
		}
		var output *os.File
		if target != "" {
			output, e = os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600|before.Mode().Perm()&0111)
			if e != nil {
				return e
			}
			defer output.Close()
		}
		buffer := make([]byte, 1<<20)
		tail := []byte{}
		var total int64
		for {
			if e = ctx.Err(); e != nil {
				return e
			}
			n, readErr := file.Read(buffer)
			total += int64(n)
			hash.Write(buffer[:n])
			joined := append(tail, buffer[:n]...)
			if check != nil {
				if e = check(string(joined)); e != nil {
					return e
				}
			}
			// Delay a suffix so a credential spanning chunks is rejected before any
			// complete credential is written. Credential capture is <=16KiB;
			// the longest checked representation uses six bytes per byte.
			keep := min(96<<10, len(joined))
			if readErr == io.EOF {
				keep = 0
			}
			cut := len(joined) - keep
			if output != nil {
				if _, e = output.Write(joined[:cut]); e != nil {
					return e
				}
			}
			tail = append([]byte(nil), joined[cut:]...)
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return readErr
			}
		}
		if total != before.Size() {
			return agentpreview.ErrSourceChanged
		}
		if output != nil {
			if e = output.Close(); e != nil {
				return e
			}
		}
	}
	after, e := os.Lstat(path)
	if e != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return agentpreview.ErrSourceChanged
	}
	return ctx.Err()
}
