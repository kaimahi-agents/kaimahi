package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const (
	maxSessionReferenceManifestBytes  = 1 << 20
	sessionReferenceExtractionTimeout = 30 * time.Second
	sessionReferenceAppendTimeout     = 10 * time.Second
)

var (
	errSessionReferenceFile       = errors.New("session reference file must be a private regular file within 1 MiB, without links")
	errSessionReferenceDirectory  = errors.New("session reference file directory is unavailable or unsafe")
	errSessionReferenceManifest   = errors.New("invalid session reference manifest")
	errSessionReferenceDuplicate  = errors.New("duplicate session reference case or source")
	errSessionReferenceStore      = errors.New("session reference manifest must use one source store")
	errSessionReferenceAppend     = errors.New("session reference manifest append unavailable")
	errSessionReferenceChanged    = errors.New("session reference manifest changed before replacement")
	errSessionReferenceExtraction = errors.New("session reference extraction unavailable or unsupported")
)

// SessionReferenceExtractionOptions selects a read-only source and an authored
// assertion-only YAML file. The output is a private reference manifest, not a
// portable bundle or an evaluation receipt.
type SessionReferenceExtractionOptions struct {
	Sessions, SessionsCA, Assertions, Out string
	SessionUIDs                           []string
}

// ExtractAgentSessionReferences extracts every selected UID before writing any
// additions. It performs only source reads, never inference or candidate
// execution, and no file-supplied endpoint is ever dialed.
func (a *App) ExtractAgentSessionReferences(opt SessionReferenceExtractionOptions) error {
	if a.Out == nil {
		return errors.New("session reference extraction requires an output stream")
	}
	if len(opt.SessionUIDs) == 0 || len(opt.SessionUIDs) > 1000 {
		return errors.New("session reference extraction requires 1-1000 session UIDs")
	}
	seen := make(map[string]bool, len(opt.SessionUIDs))
	for _, uid := range opt.SessionUIDs {
		if !safeSessionReferenceUID(uid) {
			return errors.New("invalid session reference UID")
		}
		if seen[uid] {
			return errSessionReferenceDuplicate
		}
		seen[uid] = true
	}
	assertionRoot, assertionName, _, err := openSessionReferenceDirectory(opt.Assertions)
	if err != nil {
		return err
	}
	authoredRaw, _, err := readSessionReferenceFileMode(assertionRoot, assertionName, false)
	assertionRoot.Close()
	if err != nil {
		return errors.New("session reference assertions file unavailable or unsafe")
	}
	authored, err := agentruntime.ParseSessionReferenceAssertions(authoredRaw)
	if err != nil {
		return errors.New("invalid session reference assertions")
	}
	address, err := agentsessions.NormalizeAddress(opt.Sessions)
	if err != nil {
		return errors.New("invalid session reference source address")
	}
	root, name, _, err := openSessionReferenceDirectory(opt.Out)
	if err != nil {
		return err
	}
	existing, _, _, err := readOptionalSessionReferenceManifest(root, name)
	root.Close()
	if err != nil {
		return err
	}
	if len(existing.Cases)+len(opt.SessionUIDs) > 1000 {
		return errSessionReferenceManifest
	}
	existingIDs := make(map[string]bool, len(existing.Cases))
	for _, c := range existing.Cases {
		if c.Source.Store != address {
			return errSessionReferenceStore
		}
		existingIDs[c.ID] = true
		if c.Source.Store == address && seen[c.Source.SessionUID] {
			return errSessionReferenceDuplicate
		}
	}
	if len(existing.Cases) > 0 {
		// ID derivation depends only on authored ID and logical store/UID.
		// Reuse a validated source as a prototype solely for ID preflight;
		// no prototype evidence is written or passed to the source adapter.
		prototype := existing.Cases[0].Source
		prototype.Store = address
		for _, uid := range opt.SessionUIDs {
			prototype.SessionUID = uid
			c, err := agentruntime.NewSessionReferenceCase(authored, prototype)
			if err != nil {
				return errSessionReferenceManifest
			}
			if existingIDs[c.ID] {
				return errSessionReferenceDuplicate
			}
		}
	}
	ctx, stop := signal.NotifyContext(a.operationContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, sessionReferenceExtractionTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return errSessionReferenceExtraction
	}
	// Normalization identifies the logical store only. Dial the original spelling
	// so DNS-spelled loopback cannot become eligible for plaintext transport.
	client, err := agentsessions.Dial(agentsessions.Options{Address: opt.Sessions, CAFile: opt.SessionsCA})
	if err != nil {
		return errSessionReferenceExtraction
	}
	defer client.Close()
	additions := make([]agentruntime.SessionReferenceCase, 0, len(opt.SessionUIDs))
	for _, uid := range opt.SessionUIDs {
		source, err := client.ExtractSessionReference(ctx, uid)
		if err != nil || ctx.Err() != nil {
			return errSessionReferenceExtraction
		}
		source.Store = address
		c, err := agentruntime.NewSessionReferenceCase(authored, source)
		if err != nil {
			return errSessionReferenceExtraction
		}
		additions = append(additions, c)
	}
	if err := appendSessionReferenceManifestContext(ctx, opt.Out, additions); err != nil {
		return err
	}
	// Print only validated identities/digests, never source text or authored
	// assertion operands, and never announce a partial batch as extracted.
	for _, c := range additions {
		if _, err := fmt.Fprintf(a.Out, "%s input=%s answer=%s assertions=%s\n", c.ID, c.Source.InputDigest, c.Source.AnswerDigest, c.AssertionsDigest); err != nil {
			return errors.New("session references saved but output unavailable")
		}
	}
	return nil
}

func safeSessionReferenceUID(uid string) bool {
	if len(uid) == 0 || len(uid) > 256 || secretshapes.Match(uid) != nil {
		return false
	}
	for _, r := range uid {
		if r < '!' || r > '~' {
			return false
		}
	}
	return true
}

// openSessionReferenceDirectory walks from the volume root one component at a
// time. Each handle is anchored before descending; symlinks are never accepted,
// even if they point inside the same tree. Sticky shared ancestors such as /tmp
// are allowed, but the immediate parent must not be group/other-writable.
func openSessionReferenceDirectory(path string) (*os.Root, string, os.FileInfo, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return nil, "", nil, errSessionReferenceDirectory
	}
	absolute, err := filepath.Abs(path)
	if err != nil || filepath.Base(absolute) == "." || filepath.Base(absolute) == string(filepath.Separator) {
		return nil, "", nil, errSessionReferenceDirectory
	}
	parent := filepath.Dir(absolute)
	volume := filepath.VolumeName(parent) + string(filepath.Separator)
	root, err := os.OpenRoot(volume)
	if err != nil {
		return nil, "", nil, errSessionReferenceDirectory
	}
	parts := strings.Split(strings.TrimPrefix(parent, volume), string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		info, err := root.Lstat(part)
		if err != nil || !safeSessionReferenceDirectory(info, false) {
			root.Close()
			return nil, "", nil, errSessionReferenceDirectory
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			root.Close()
			return nil, "", nil, errSessionReferenceDirectory
		}
		opened, err := next.Stat(".")
		current, currentErr := root.Lstat(part)
		root.Close()
		if err != nil || currentErr != nil || !safeSessionReferenceDirectory(current, false) || !os.SameFile(info, opened) || !os.SameFile(info, current) {
			next.Close()
			return nil, "", nil, errSessionReferenceDirectory
		}
		root = next
	}
	info, err := root.Stat(".")
	if err != nil || !safeSessionReferenceDirectory(info, true) {
		root.Close()
		return nil, "", nil, errSessionReferenceDirectory
	}
	return root, filepath.Base(absolute), info, nil
}

func safeSessionReferenceDirectory(info os.FileInfo, immediate bool) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 &&
		(info.Mode().Perm()&0022 == 0 || !immediate && info.Mode()&os.ModeSticky != 0)
}

func safeSessionReferenceFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0 && info.Size() <= maxSessionReferenceManifestBytes
}

func readSessionReferenceFile(root *os.Root, name string) ([]byte, os.FileInfo, error) {
	return readSessionReferenceFileMode(root, name, true)
}

func readSessionReferenceFileMode(root *os.Root, name string, private bool) ([]byte, os.FileInfo, error) {
	safe := func(info os.FileInfo) bool {
		return info != nil && info.Mode().IsRegular() && info.Size() <= maxSessionReferenceManifestBytes && (!private || info.Mode().Perm()&0077 == 0)
	}
	info, err := root.Lstat(name)
	if err != nil || !safe(info) {
		return nil, nil, errSessionReferenceFile
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, errSessionReferenceFile
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !safe(opened) || !os.SameFile(info, opened) {
		return nil, nil, errSessionReferenceFile
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSessionReferenceManifestBytes+1))
	current, currentErr := root.Lstat(name)
	if err != nil || len(raw) > maxSessionReferenceManifestBytes || currentErr != nil || !safe(current) || !os.SameFile(opened, current) {
		return nil, nil, errSessionReferenceFile
	}
	return raw, opened, nil
}

func readSessionReferenceManifest(path string) (agentruntime.SessionReferenceManifest, error) {
	root, name, _, err := openSessionReferenceDirectory(path)
	if err != nil {
		return agentruntime.SessionReferenceManifest{}, err
	}
	defer root.Close()
	raw, _, err := readSessionReferenceFile(root, name)
	if err != nil {
		return agentruntime.SessionReferenceManifest{}, err
	}
	manifest, err := agentruntime.ParseSessionReferenceManifest(raw)
	if err != nil {
		return agentruntime.SessionReferenceManifest{}, errSessionReferenceManifest
	}
	return manifest, nil
}

func readOptionalSessionReferenceManifest(root *os.Root, name string) (agentruntime.SessionReferenceManifest, []byte, os.FileInfo, error) {
	if _, err := root.Lstat(name); os.IsNotExist(err) {
		return agentruntime.SessionReferenceManifest{SchemaVersion: 1, Policy: agentruntime.SessionReferencePolicy}, nil, nil, nil
	}
	raw, info, err := readSessionReferenceFile(root, name)
	if err != nil {
		return agentruntime.SessionReferenceManifest{}, nil, nil, err
	}
	manifest, err := agentruntime.ParseSessionReferenceManifest(raw)
	if err != nil {
		return agentruntime.SessionReferenceManifest{}, nil, nil, errSessionReferenceManifest
	}
	return manifest, raw, info, nil
}

func appendSessionReferenceManifest(path string, additions []agentruntime.SessionReferenceCase) error {
	ctx, cancel := context.WithTimeout(context.Background(), sessionReferenceAppendTimeout)
	defer cancel()
	return appendSessionReferenceManifestContext(ctx, path, additions)
}

func appendSessionReferenceManifestContext(ctx context.Context, path string, additions []agentruntime.SessionReferenceCase) error {
	if err := checkSessionReferenceDuplicates(additions); err != nil {
		return err
	}
	if _, err := agentruntime.MarshalSessionReferenceManifest(agentruntime.SessionReferenceManifest{SchemaVersion: 1, Policy: agentruntime.SessionReferencePolicy, Cases: additions}); err != nil {
		return errSessionReferenceManifest
	}
	root, name, parent, err := openSessionReferenceDirectory(path)
	if err != nil {
		return err
	}
	defer root.Close()
	unlock, err := lockSessionReferenceManifest(ctx, root, name+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	manifest, originalRaw, original, err := readOptionalSessionReferenceManifest(root, name)
	if err != nil {
		return err
	}
	manifest.Cases = append(manifest.Cases, additions...)
	if err := checkSessionReferenceDuplicates(manifest.Cases); err != nil {
		return err
	}
	raw, err := agentruntime.MarshalSessionReferenceManifest(manifest)
	if err != nil {
		return errSessionReferenceManifest
	}
	token, err := randomHex(16)
	if err != nil {
		return errSessionReferenceAppend
	}
	stage := ".session-reference-" + token
	file, err := root.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errSessionReferenceAppend
	}
	defer root.Remove(stage)
	defer file.Close()
	if _, err := file.Write(raw); err != nil {
		return errSessionReferenceAppend
	}
	if err := file.Sync(); err != nil {
		return errSessionReferenceAppend
	}
	if err := file.Close(); err != nil {
		return errSessionReferenceAppend
	}
	// A moved/relinked parent must not redirect the append or make it appear to
	// have updated a path whose current directory is no longer the opened one.
	currentRoot, _, currentParent, err := openSessionReferenceDirectory(path)
	if err != nil {
		return errSessionReferenceChanged
	}
	currentRoot.Close()
	if !os.SameFile(parent, currentParent) || ctx.Err() != nil {
		return errSessionReferenceChanged
	}
	return replaceSessionReferenceManifest(root, name, stage, original, originalRaw)
}

func checkSessionReferenceDuplicates(cases []agentruntime.SessionReferenceCase) error {
	ids := make(map[string]bool, len(cases))
	sources := make(map[[2]string]bool, len(cases))
	for _, c := range cases {
		source := [2]string{c.Source.Store, c.Source.SessionUID}
		if ids[c.ID] || sources[source] {
			return errSessionReferenceDuplicate
		}
		if c.Source.Store != cases[0].Source.Store {
			return errSessionReferenceStore
		}
		ids[c.ID], sources[source] = true, true
	}
	return nil
}

// The lock is deliberately exclusive-create rather than advisory inode locking:
// cooperative writers never race a removed/recreated lock inode. A stale lock
// causes bounded refusal; it is never automatically broken by another writer.
func lockSessionReferenceManifest(ctx context.Context, root *os.Root, name string) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, sessionReferenceAppendTimeout)
	defer cancel()
	for {
		if ctx.Err() != nil {
			return nil, errSessionReferenceAppend
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			info, statErr := file.Stat()
			closeErr := file.Close()
			if statErr != nil || closeErr != nil {
				_ = root.Remove(name)
				return nil, errSessionReferenceAppend
			}
			return func() {
				if current, err := root.Lstat(name); err == nil && os.SameFile(info, current) && safeSessionReferenceFile(current) {
					_ = root.Remove(name)
				}
			}, nil
		}
		if !os.IsExist(err) {
			return nil, errSessionReferenceAppend
		}
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !safeSessionReferenceFile(info) {
			return nil, errSessionReferenceAppend
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, errSessionReferenceAppend
		case <-timer.C:
		}
	}
}

// Recheck original identity and exact bytes immediately before atomic rename.
// This protects cooperative writers and detects intervening replacement or
// in-place edits; it does not claim exclusion against a malicious same-user writer.
func replaceSessionReferenceManifest(root *os.Root, name, stage string, original os.FileInfo, originalRaw []byte) error {
	if original == nil {
		if _, err := root.Lstat(name); !os.IsNotExist(err) {
			return errSessionReferenceChanged
		}
	} else {
		raw, current, err := readSessionReferenceFile(root, name)
		if err != nil || !os.SameFile(original, current) || !bytes.Equal(raw, originalRaw) {
			return errSessionReferenceChanged
		}
	}
	if err := root.Rename(stage, name); err != nil {
		return errSessionReferenceAppend
	}
	return nil
}
