package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

const kagentLiftReceiptDir = "receipts/kagent-v0.10.3"
const kagentLiftLock = ".kmx-kagent-v0.10.3.lock"
const kagentVersionMarker = "kaimahi.dev/runtime-version"

func kagentLiftReceiptName(context, namespace, clusterUID string) string {
	key := sha256.Sum256([]byte(context + "\x00" + namespace + "\x00" + clusterUID))
	return fmt.Sprintf("%x.json", key)
}

const maxKagentFileBytes = 1 << 20

// Root.OpenFile follows even in-root symlinks. Use single-component openat
// calls instead, retaining each checked directory descriptor for the next step.
// O_DIRECTORY and O_NONBLOCK prevent a substituted FIFO from blocking an open.
func openKagentReceiptDir(bundle *os.File, create bool) (*os.File, error) {
	fd, err := unix.Openat(int(bundle.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), ".")
	for _, name := range strings.Split(kagentLiftReceiptDir, "/") {
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if errors.Is(err, os.ErrNotExist) && create {
			if err = unix.Mkdirat(int(dir.Fd()), name, 0700); err == nil {
				fd, err = unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			}
		}
		dir.Close()
		if err != nil {
			return nil, fmt.Errorf("Kagent receipts must be private directories, not links: %w", err)
		}
		dir = os.NewFile(uintptr(fd), name)
		info, err := dir.Stat()
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			dir.Close()
			return nil, fmt.Errorf("Kagent receipts must be private directories, not links")
		}
	}
	return dir, nil
}

func checkKagentReceiptDirs(bundle *os.File, create bool) error {
	dir, err := openKagentReceiptDir(bundle, create)
	if errors.Is(err, os.ErrNotExist) && !create {
		return nil
	}
	if err != nil {
		return err
	}
	return dir.Close()
}

func openKagentFile(dir *os.File, name string) (*os.File, error) {
	if name == "." || name == ".." || filepath.Base(name) != name {
		return nil, fmt.Errorf("Kagent file name must be a single path component")
	}
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open Kagent bounded regular file, not a link: %w", err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

// The size limit applies to bytes actually read, not just a pre-read stat.
// Revalidate the same descriptor to refuse growth, truncation or permission
// changes; pathname substitution cannot redirect this read to another file.
func readKagentOpenedFile(file *os.File, before os.FileInfo, private bool) ([]byte, error) {
	if !before.Mode().IsRegular() || before.Size() > maxKagentFileBytes || private && before.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("Kagent file must be a bounded regular file (private for receipts), not a link")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxKagentFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Kagent file: %w", err)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened Kagent file: %w", err)
	}
	if len(raw) > maxKagentFileBytes || int64(len(raw)) != before.Size() || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		return nil, fmt.Errorf("Kagent file changed or exceeded its bound during read")
	}
	return raw, nil
}

func lockKagentLift(bundle *os.File) (func(), error) {
	if err := unix.Mkdirat(int(bundle.Fd()), kagentLiftLock, 0700); err != nil {
		return nil, fmt.Errorf("Kagent lift operation locked; inspect any interrupted operation before removing its local lock: %w", err)
	}
	return func() { _ = unix.Unlinkat(int(bundle.Fd()), kagentLiftLock, unix.AT_REMOVEDIR) }, nil
}

type kagentReceiptSnapshot struct {
	Raw          []byte
	Parent, File os.FileInfo
}

func readKagentLiftReceiptAt(bundleDir *os.File, name string, target agentruntime.DeployTarget, clusterUID, bundle string) (*agentruntime.DeployReceipt, error) {
	return readKagentLiftReceiptEvidenceAt(bundleDir, name, target, clusterUID, bundle, nil)
}

// Keep the validated original bytes and descriptor identities for evaluation,
// rather than hashing a reserialized receipt or permitting identical replacement.
func readKagentLiftReceiptEvidenceAt(bundleDir *os.File, name string, target agentruntime.DeployTarget, clusterUID, bundle string, snapshot *kagentReceiptSnapshot) (*agentruntime.DeployReceipt, error) {
	dir, err := openKagentReceiptDir(bundleDir, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	file, err := openKagentFile(dir, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	raw, err := readKagentOpenedFile(file, info, true)
	if err != nil {
		return nil, err
	}
	var wrapped bundleLiftReceipt
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wrapped) != nil || requireKagentJSONEOF(decoder) != nil {
		return nil, fmt.Errorf("Kagent receipt is malformed")
	}
	r := wrapped.Receipt
	if r.Target != target || r.ClusterUID != clusterUID || r.Bundle != bundle || r.RuntimeVersion != "v0.10.3" || !r.Ready || r.ObservedAt.IsZero() || !validOrkaMarkerDigest(r.PortableDigest) || !validOrkaMarkerDigest(r.RenderedDigest) || len(r.Resources) != 2 || len(r.Proofs) != 2 || len(r.Workload) != 4 || len(r.Dependencies) == 0 {
		return nil, fmt.Errorf("Kagent receipt has incomplete or mismatched runtime/version/target evidence")
	}
	for i, kind := range []string{"ModelConfig", "Agent"} {
		id, p := r.Resources[i], r.Proofs[i]
		if id.Kind != kind || id.Name != bundle || id.Namespace != target.Namespace || id.UID == "" || id.Generation < 1 || p.Kind != kind || p.Name != bundle || p.Namespace != target.Namespace || p.UID != id.UID || p.Generation != id.Generation || !validOrkaMarkerDigest(p.SpecDigest) {
			return nil, fmt.Errorf("Kagent receipt primary identity is incomplete")
		}
	}
	for i, kind := range []string{"Deployment", "Service", "ServiceAccount", "Secret"} {
		p := r.Workload[i]
		if p.Kind != kind || p.Name != bundle || p.Namespace != target.Namespace || p.UID == "" || p.ResourceVersion == "" {
			return nil, fmt.Errorf("Kagent receipt workload identity is incomplete")
		}
	}
	for _, p := range r.Dependencies {
		if p.Namespace != target.Namespace || p.Name == "" || p.UID == "" || p.ResourceVersion == "" || (p.Kind != "Secret" && p.Kind != "ConfigMap" && p.Kind != "RemoteMCPServer") || (p.Kind == "RemoteMCPServer" && !validOrkaMarkerDigest(p.SpecDigest)) {
			return nil, fmt.Errorf("Kagent receipt dependency identity is incomplete")
		}
	}
	if snapshot != nil {
		parent, err := dir.Stat()
		if err != nil {
			return nil, err
		}
		*snapshot = kagentReceiptSnapshot{Raw: raw, Parent: parent, File: info}
	}
	return &r, nil
}

func writeKagentLiftReceipt(bundle *os.File, name string, wrapped bundleLiftReceipt) error {
	if name == "." || name == ".." || filepath.Base(name) != name {
		return fmt.Errorf("Kagent receipt name must be a single path component")
	}
	history, err := openKagentReceiptDir(bundle, true)
	if err != nil {
		return err
	}
	defer history.Close()
	raw, err := json.MarshalIndent(wrapped, "", "  ")
	if err != nil {
		return err
	}
	suffix, err := randomHex(16)
	if err != nil {
		return err
	}
	tmp := ".receipt-" + suffix
	fd, err := unix.Openat(int(history.Fd()), tmp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), tmp)
	defer unix.Unlinkat(int(history.Fd()), tmp, 0)
	if _, err = file.Write(append(raw, '\n')); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	// Link publishes complete bytes atomically without overwriting a receipt.
	if err = unix.Linkat(int(history.Fd()), tmp, int(history.Fd()), name, 0); err != nil {
		return fmt.Errorf("publish Kagent receipt without overwrite: %w", err)
	}
	return nil
}
