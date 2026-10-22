package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

const maxSuitePublicMetadataBytes = 4096

var errSuitePublicMetadata = errors.New("public evaluation export unavailable: provenance missing or invalid; use a tagged module with a checksum or a clean VCS-recorded kmx build and re-evaluate")
var errSuitePublicCapacity = errors.New("public evaluation export unavailable: private metadata storage unsafe or full; select another private KMX_HOME and re-evaluate (no automatic cleanup)")
var suitePublicRevision = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var suitePublicToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var suitePublicRelease = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
var suitePublicPseudo = regexp.MustCompile(`^v[0-9]+\.(?:0\.0-|[0-9]+\.[0-9]+-(?:[^+]*\.)?0\.)[0-9]{14}-[A-Za-z0-9]+$`)

type suitePublicProducer struct {
	Module   string `json:"module"`
	Version  string `json:"version"`
	Sum      string `json:"sum,omitempty"`
	Revision string `json:"revision,omitempty"`
	Modified *bool  `json:"modified,omitempty"`
}

// The optional executable digest is private and has no role in public identity.
// Producer describes Go build SOURCE provenance, not a binary attestation.
type suitePublicMetadata struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Workspace     string              `json:"workspace"`
	SuiteDigest   string              `json:"suiteDigest"`
	Sequence      uint64              `json:"sequence"`
	ReceiptDigest string              `json:"receiptDigest"`
	StartedAt     string              `json:"startedAt"`
	CompletedAt   string              `json:"completedAt"`
	Producer      suitePublicProducer `json:"producer"`
	BinaryDigest  string              `json:"binaryDigest,omitempty"`
}

func suitePublicHash(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func suitePublicDigest(v any) (string, error) {
	raw, err := suitecore.Marshal(v)
	if err != nil {
		return "", errSuitePublicMetadata
	}
	return suitePublicHash(raw), nil
}
func (p suitePublicProducer) publicVersion() (string, error) {
	if p.Module != "github.com/kaimahi-agents/kaimahi" || len(p.Version) > 128 {
		return "", errSuitePublicMetadata
	}
	if (p.Revision != "" && !suitePublicRevision.MatchString(p.Revision)) || (p.Modified != nil && *p.Modified) {
		return "", errSuitePublicMetadata
	}
	var sum []byte
	if p.Sum != "" {
		var err error
		if !strings.HasPrefix(p.Sum, "h1:") || len(p.Sum) != 47 {
			return "", errSuitePublicMetadata
		}
		sum, err = base64.StdEncoding.Strict().DecodeString(p.Sum[3:])
		if err != nil || len(sum) != sha256.Size || base64.StdEncoding.EncodeToString(sum) != p.Sum[3:] {
			return "", errSuitePublicMetadata
		}
	}
	version := p.Version
	if sum != nil && suitePublicTaggedVersion(version) {
		// Lossless checksum encoding fits the public token alphabet. Main.Sum is
		// a recorded build claim, not an independently verified checksumDB proof.
		version += fmt.Sprintf("-mod-%x", sum)
	} else {
		if p.Revision == "" || p.Modified == nil {
			return "", errSuitePublicMetadata
		}
		if version == "(devel)" {
			version = "devel"
		}
		if version != "" {
			version += "-"
		}
		version += "git-" + p.Revision
	}
	if !suitePublicID(version) {
		return "", errSuitePublicMetadata
	}
	return version, nil
}

func suitePublicTaggedVersion(version string) bool {
	if !suitePublicRelease.MatchString(version) || suitePublicPseudo.MatchString(version) {
		return false
	}
	_, prerelease, _ := strings.Cut(version, "-")
	for _, part := range strings.Split(prerelease, ".") {
		if len(part) < 2 || part[0] != '0' {
			continue
		}
		if strings.Trim(part, "0123456789") == "" {
			return false
		}
	}
	return true
}
func suitePublicBuildInfo(b *debug.BuildInfo) (suitePublicProducer, error) {
	if b == nil || b.Main.Replace != nil {
		return suitePublicProducer{}, errSuitePublicMetadata
	}
	p := suitePublicProducer{Module: b.Main.Path, Version: b.Main.Version, Sum: b.Main.Sum}
	seen := map[string]bool{}
	for _, s := range b.Settings {
		if s.Key != "vcs" && !strings.HasPrefix(s.Key, "vcs.") {
			continue
		}
		if seen[s.Key] {
			return p, errSuitePublicMetadata
		}
		seen[s.Key] = true
		switch s.Key {
		case "vcs.revision":
			if !suitePublicRevision.MatchString(s.Value) {
				return p, errSuitePublicMetadata
			}
			p.Revision = s.Value
		case "vcs.modified":
			if s.Value != "false" {
				return p, errSuitePublicMetadata
			}
			modified := false
			p.Modified = &modified
		}
	}
	_, err := p.publicVersion()
	return p, err
}
func captureSuitePublicMetadata() *suitePublicMetadata {
	m := &suitePublicMetadata{SchemaVersion: 1, StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	b, ok := debug.ReadBuildInfo()
	if !ok {
		return m
	}
	p, err := suitePublicBuildInfo(b)
	if err != nil {
		return m
	}
	m.Producer = p
	if runtime.GOOS == "linux" {
		m.BinaryDigest = suitePublicBinaryDigest()
	}
	return m
}
func suitePublicBinaryDigest() string {
	f, err := os.Open("/proc/self/exe")
	if err != nil {
		return ""
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > 512<<20 {
		return ""
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (512<<20)+1))
	after, e := f.Stat()
	if err != nil || e != nil || n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return ""
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}
func (m *suitePublicMetadata) valid() bool {
	if m == nil || m.SchemaVersion != 1 || !verificationDigest(m.Workspace) || !suitePrefixedDigest(m.SuiteDigest) || m.Sequence == 0 || !suitePrefixedDigest(m.ReceiptDigest) {
		return false
	}
	if _, err := m.Producer.publicVersion(); err != nil {
		return false
	}
	start, e := time.Parse(time.RFC3339Nano, m.StartedAt)
	end, f := time.Parse(time.RFC3339Nano, m.CompletedAt)
	return e == nil && f == nil && strings.HasSuffix(m.StartedAt, "Z") && strings.HasSuffix(m.CompletedAt, "Z") && !end.Before(start) && (m.BinaryDigest == "" || suitePrefixedDigest(m.BinaryDigest))
}
func (m *suitePublicMetadata) filename() string {
	return fmt.Sprintf("public-%020d-%s.json", m.Sequence, strings.TrimPrefix(m.ReceiptDigest, "sha256:"))
}

func (s *suitePassStore) publishPublicMetadata(ctx context.Context, raw []byte, suite *workspace.NativeSuite, m *suitePublicMetadata) error {
	if err := suite.RequireUnprofiled(); err != nil {
		return err
	}
	if s.completion == nil || m == nil {
		return errSuitePublicMetadata
	}
	receipt, err := parseSuitePassingReceipt(raw)
	if err != nil {
		return errSuitePublicMetadata
	}
	canonical, err := canonicalSuitePassingReceipt(ctx, receipt, suite)
	if err != nil {
		return errSuitePublicMetadata
	}
	seq, err := strconv.ParseUint(strings.TrimSuffix(s.completion.name, ".json"), 10, 64)
	if err != nil {
		return errSuitePublicMetadata
	}
	m.Workspace, m.SuiteDigest, m.Sequence, m.ReceiptDigest = s.identity, s.suiteDigest, seq, suitePublicHash(canonical)
	if !m.valid() {
		return errSuitePublicMetadata
	}
	raw, err = suitecore.Marshal(m)
	if err != nil || len(raw) > maxSuitePublicMetadataBytes {
		return errSuitePublicMetadata
	}
	if s.unchanged() != nil {
		return errSuitePublicCapacity
	}
	// All companion-looking entries count, including incomplete/orphaned writes.
	root, _, _, err := openSessionReferenceDirectory(filepath.Join(s.partition, "entry"))
	if err != nil {
		return errSuitePublicCapacity
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return errSuitePublicCapacity
	}
	defer dir.Close()
	count, total := 0, int64(0)
	for {
		entries, e := dir.ReadDir(64)
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "public-") {
				continue
			}
			count++
			info, e := entry.Info()
			if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxSuitePublicMetadataBytes {
				return errSuitePublicCapacity
			}
			total += info.Size()
		}
		if count >= 256 || total+int64(len(raw)) > 1<<20 {
			return errSuitePublicCapacity
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return errSuitePublicCapacity
		}
	}
	out, err := preflightSuitePassingExport(ctx, filepath.Join(s.partition, m.filename()), s.workspace, s.cache)
	if err != nil {
		return errSuitePublicCapacity
	}
	err = errors.Join(out.publishBytes(ctx, raw), out.Close())
	if err != nil {
		return errSuitePublicCapacity
	}
	return nil
}
func (s *suitePassStore) readPublicMetadata(ctx context.Context, suite *workspace.NativeSuite) (_ *suitePublicMetadata, err error) {
	scan, err := s.scan(ctx, suite)
	if err != nil || scan.latest == nil {
		return nil, errSuitePublicMetadata
	}
	canonical, err := canonicalSuitePassingReceipt(ctx, scan.latest, suite)
	if err != nil {
		return nil, errSuitePublicMetadata
	}
	expected := suitePublicMetadata{Workspace: s.identity, SuiteDigest: s.suiteDigest, Sequence: scan.selectedSequence, ReceiptDigest: suitePublicHash(canonical)}
	root, name, parent, err := openSessionReferenceDirectory(filepath.Join(s.partition, expected.filename()))
	if err != nil || parent.Mode().Perm()&0077 != 0 {
		if root != nil {
			root.Close()
		}
		return nil, errSuitePublicMetadata
	}
	defer func() {
		if root.Close() != nil {
			err = errSuitePublicMetadata
		}
	}()
	dir, err := root.Open(".")
	if err != nil {
		return nil, errSuitePublicMetadata
	}
	defer dir.Close()
	f, err := openKagentFile(dir, name)
	if err != nil {
		return nil, errSuitePublicMetadata
	}
	before, e := f.Stat()
	if e != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() > maxSuitePublicMetadataBytes {
		f.Close()
		return nil, errSuitePublicMetadata
	}
	raw, e := io.ReadAll(io.LimitReader(f, maxSuitePublicMetadataBytes+1))
	after, a := f.Stat()
	closeErr := f.Close()
	current, c := root.Lstat(name)
	if e != nil || a != nil || c != nil || closeErr != nil || !os.SameFile(before, current) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) || int64(len(raw)) != before.Size() {
		return nil, errSuitePublicMetadata
	}
	var m suitePublicMetadata
	if suitecore.Unmarshal(raw, &m) != nil || !m.valid() {
		return nil, errSuitePublicMetadata
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return nil, errSuitePublicMetadata
	}
	original, e := suitecore.Marshal(value)
	typed, a := suitecore.Marshal(m)
	if e != nil || a != nil || !bytes.Equal(original, typed) || m.Workspace != expected.Workspace || m.SuiteDigest != expected.SuiteDigest || m.Sequence != expected.Sequence || m.ReceiptDigest != expected.ReceiptDigest || s.unchanged() != nil {
		return nil, errSuitePublicMetadata
	}
	fresh, _, now, openErr := openSessionReferenceDirectory(filepath.Join(s.partition, expected.filename()))
	if openErr != nil {
		return nil, errSuitePublicMetadata
	}
	if fresh.Close() != nil || !os.SameFile(parent, now) || now.Mode().Perm()&0077 != 0 {
		return nil, errSuitePublicMetadata
	}
	return &m, nil
}
