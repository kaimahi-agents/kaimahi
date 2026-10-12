package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"golang.org/x/sys/unix"
)

// The status receipt boundary must reject a changed bundle before any receipt
// or live target read. A link back to the original directory tests no-follow
// independently of SameFile; an identical replacement tests SameFile itself.
func TestKagentFilesStatusBundleBoundaryRejectsReplacement(t *testing.T) {
	for _, replacement := range []string{"fifo", "symlink", "identical directory"} {
		t.Run(replacement, func(t *testing.T) {
			a, opt, _ := kagentLiftFixture(t, "")
			_, bundle, adapter, _, _, err := a.prepareKagentBundle(opt.BundleDir, "", opt.ToContext, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(bundle, bundle+"-old"); err != nil {
				t.Fatal(err)
			}
			switch replacement {
			case "fifo":
				err = unix.Mkfifo(bundle, 0600)
			case "symlink":
				err = os.Symlink(bundle+"-old", bundle)
			case "identical directory":
				err = os.Mkdir(bundle, 0700)
				if err == nil {
					for _, name := range []string{"agent.yaml", "bindings.yaml"} {
						raw, readErr := os.ReadFile(filepath.Join(bundle+"-old", name))
						if readErr != nil {
							t.Fatal(readErr)
						}
						if err = os.WriteFile(filepath.Join(bundle, name), raw, 0600); err != nil {
							break
						}
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			type result struct {
				root *os.File
				err  error
			}
			done := make(chan result, 1)
			go func() {
				root, err := openKagentStatusBundle(bundle, adapter.lift.bundleInfo)
				done <- result{root, err}
			}()
			var got result
			select {
			case got = <-done:
			case <-time.After(time.Second):
				// Unblock the old FIFO open so RED does not leak a goroutine.
				if replacement == "fifo" {
					rescue, err := os.OpenFile(bundle, os.O_RDWR|unix.O_NONBLOCK, 0)
					if err != nil {
						t.Fatal(err)
					}
					defer rescue.Close()
				}
				select {
				case got = <-done:
				case <-time.After(time.Second):
					t.Fatal("status bundle open did not unblock")
				}
				t.Error("status bundle-to-FIFO open blocked")
			}
			if got.root != nil {
				got.root.Close()
				t.Fatal("invalid bundle supplied a descriptor for receipt/live status reads")
			}
			if got.err == nil || replacement == "identical directory" && !strings.Contains(got.err.Error(), "bundle directory changed") {
				t.Fatalf("wrong status bundle refusal: %v", got.err)
			}
		})
	}
}

func TestKagentFilesStatusDoesNotWriteReceiptHistory(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(map[bool]string{false: "no receipt", true: "ready receipt"}[saved], func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, "")
			receiptPath := filepath.Join(opt.BundleDir, kagentLiftReceiptDir, kagentLiftReceiptName(opt.ToContext, "agents", "cluster-uid"))
			lockPath := filepath.Join(opt.BundleDir, kagentLiftLock)
			var receipt []byte
			var receiptInfo, lockInfo os.FileInfo
			if saved {
				if err := a.LiftAgentBundle(opt); err != nil {
					t.Fatal(err)
				}
				var err error
				receipt, err = os.ReadFile(receiptPath)
				if err != nil {
					t.Fatal(err)
				}
				receiptInfo, err = os.Stat(receiptPath)
				if err != nil {
					t.Fatal(err)
				}
				// An existing lift lock must neither block status nor be removed.
				if err := os.Mkdir(lockPath, 0700); err != nil {
					t.Fatal(err)
				}
				lockInfo, err = os.Stat(lockPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := len(kagentCalls(t, dir))
			got := readKagentStatusForTest(t, a, opt)
			if saved {
				if got.State != "ready" || !got.Ready || got.NoReceipt {
					t.Fatalf("saved status=%+v", got)
				}
				after, err := os.ReadFile(receiptPath)
				if err != nil || !bytes.Equal(after, receipt) {
					t.Fatalf("status changed receipt bytes: %v", err)
				}
				afterInfo, err := os.Stat(receiptPath)
				if err != nil || !os.SameFile(receiptInfo, afterInfo) || receiptInfo.Mode() != afterInfo.Mode() || !receiptInfo.ModTime().Equal(afterInfo.ModTime()) {
					t.Fatalf("status rewrote receipt: %v", err)
				}
				afterLock, err := os.Stat(lockPath)
				if err != nil || !os.SameFile(lockInfo, afterLock) {
					t.Fatalf("status changed existing lift lock: %v", err)
				}
			} else {
				if got.State != "absent" || got.Ready || !got.NoReceipt {
					t.Fatalf("no-receipt status=%+v", got)
				}
				for _, path := range []string{filepath.Join(opt.BundleDir, "receipts"), lockPath} {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatalf("status created receipt history or lift lock: %s: %v", path, err)
					}
				}
			}
			assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[before:])
			if _, err := os.Lstat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
				t.Fatalf("status wrote remembered target state: %v", err)
			}
		})
	}
}

func TestKagentFilesOpenedReadPinsFileAndBoundsGrowth(t *testing.T) {
	for _, change := range []string{"symlink swap", "growth", "truncation", "permission"} {
		t.Run(change, func(t *testing.T) {
			dirPath := t.TempDir()
			path := filepath.Join(dirPath, "input")
			if err := os.WriteFile(path, []byte("reviewed"), 0600); err != nil {
				t.Fatal(err)
			}
			dir, err := os.Open(dirPath)
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Close()
			file, err := openKagentFile(dir, "input")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "symlink swap":
				outside := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(outside, []byte("not reviewed"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path, path+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "growth":
				if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 2<<20), 0600); err != nil {
					t.Fatal(err)
				}
			case "truncation":
				if err := os.Truncate(path, 1); err != nil {
					t.Fatal(err)
				}
			case "permission":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := readKagentOpenedFile(file, info, true)
			if change == "growth" {
				position, seekErr := file.Seek(0, 1)
				if seekErr != nil || position > (1<<20)+1 {
					t.Fatalf("growth caused unbounded read: position=%d err=%v", position, seekErr)
				}
			}
			if change == "symlink swap" {
				if err != nil || string(raw) != "reviewed" {
					t.Fatalf("opened read followed replacement: %q %v", raw, err)
				}
				if replacement, err := openKagentFile(dir, "input"); err == nil {
					replacement.Close()
					t.Fatal("substituted symlink was opened")
				}
			} else if err == nil || raw != nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("changed file accepted: len=%d err=%v", len(raw), err)
			}
		})
	}
}

func TestKagentFilesRejectNonregularWithoutBlocking(t *testing.T) {
	dirPath := t.TempDir()
	dir, err := os.Open(dirPath)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := unix.Mkfifo(filepath.Join(dirPath, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dirPath, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fifo", "directory"} {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				file, err := openKagentFile(dir, name)
				if err == nil {
					defer file.Close()
					var info os.FileInfo
					info, err = file.Stat()
					if err == nil {
						_, err = readKagentOpenedFile(file, info, true)
					}
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "regular") {
					t.Fatalf("nonregular input accepted: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("nonregular open/read blocked")
			}
		})
	}
}

func TestKagentFilesReplacementAfterModelCreateStopsAgent(t *testing.T) {
	for _, replacement := range []string{"identical file", "symlink", "growth", "fifo"} {
		t.Run(replacement, func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, "")
			changed := false
			a.waitTiming.timeout = func(parent context.Context, phase string, duration time.Duration) (context.Context, context.CancelFunc) {
				if phase == "kagent-readiness-ModelConfig" && !changed {
					path := filepath.Join(opt.BundleDir, "agent.yaml")
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(path, path+"-old"); err != nil {
						t.Fatal(err)
					}
					switch replacement {
					case "identical file":
						err = os.WriteFile(path, raw, 0600)
					case "symlink":
						err = os.Symlink("agent.yaml-old", path)
					case "growth":
						err = os.WriteFile(path, bytes.Repeat([]byte("x"), (1<<20)+1), 0600)
					case "fifo":
						err = unix.Mkfifo(path, 0600)
					}
					if err != nil {
						t.Fatal(err)
					}
					changed = true
				}
				if duration == 0 {
					return parent, func() {}
				}
				return context.WithTimeout(parent, duration)
			}
			err := a.LiftAgentBundle(opt)
			if !changed || err == nil || !strings.Contains(err.Error(), "agent.yaml changed") {
				t.Fatalf("replacement after ModelConfig accepted: changed=%v err=%v", changed, err)
			}
			assertKagentWriteKinds(t, kagentCalls(t, dir), "ModelConfig")
			if _, err := os.Stat(filepath.Join(opt.BundleDir, kagentLiftReceiptDir)); !os.IsNotExist(err) {
				t.Fatalf("failed placement wrote receipt history: %v", err)
			}
		})
	}
}

func TestKagentFilesReceiptSafetyAndNoOverwrite(t *testing.T) {
	dirPath := t.TempDir()
	root, err := os.Open(dirPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := writeKagentLiftReceipt(root, "saved.json", bundleLiftReceipt{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dirPath, kagentLiftReceiptDir, "saved.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKagentLiftReceipt(root, "saved.json", bundleLiftReceipt{GitCommit: "replacement"}); err == nil {
		t.Fatal("receipt overwritten")
	}
	after, err := os.ReadFile(filepath.Join(dirPath, kagentLiftReceiptDir, "saved.json"))
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatalf("publication changed existing receipt: %v", err)
	}
	for _, name := range []string{"../outside", ".", ".."} {
		if err := writeKagentLiftReceipt(root, name, bundleLiftReceipt{}); err == nil {
			t.Fatalf("unsafe receipt name accepted: %q", name)
		}
	}
	for _, kind := range []string{"symlink", "directory", "fifo", "insecure", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(dirPath, kagentLiftReceiptDir, kind+".json")
			switch kind {
			case "symlink":
				err = os.Symlink("saved.json", path)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "fifo":
				err = unix.Mkfifo(path, 0600)
			case "insecure":
				err = os.WriteFile(path, []byte("{}"), 0600)
				if err == nil {
					err = os.Chmod(path, 0644)
				}
			case "oversized":
				err = os.WriteFile(path, bytes.Repeat([]byte("x"), (1<<20)+1), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readKagentLiftReceiptAt(root, kind+".json", agentruntime.DeployTarget{}, "", ""); err == nil || (!strings.Contains(err.Error(), "regular") && !strings.Contains(err.Error(), "link")) {
				t.Fatalf("unsafe receipt accepted or wrong refusal: %v", err)
			}
		})
	}
}

func TestKagentFilesReceiptDirectoryDescriptorSurvivesReplacement(t *testing.T) {
	dirPath := t.TempDir()
	root, err := os.Open(dirPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	history, err := openKagentReceiptDir(root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	path := filepath.Join(dirPath, "receipts")
	if err := os.Rename(path, path+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("receipts-old", path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path+"-old", "kagent-v0.10.3", "input"), []byte("anchored"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := openKagentFile(history, "input")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := readKagentOpenedFile(file, info, true)
	if err != nil || string(raw) != "anchored" {
		t.Fatalf("directory replacement redirected read: %q %v", raw, err)
	}
	if err := writeKagentLiftReceipt(root, "new.json", bundleLiftReceipt{}); err == nil || !strings.Contains(err.Error(), "private directories") {
		t.Fatalf("intermediate symlink authorized publication: %v", err)
	}
}

// A replacement with identical bytes must not hide a pathname/parent rebind
// between preparing the reviewed inputs and a later mutation boundary.
func TestKagentFilesRecheckRejectsIdenticalReplacement(t *testing.T) {
	for _, which := range []string{"agent.yaml", "bindings.yaml", "bundle", "external parent"} {
		t.Run(which, func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, "")
			if which == "external parent" {
				parent := filepath.Join(dir, "external")
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
				opt.Bindings = filepath.Join(parent, "bindings.yaml")
				if err := os.Rename(filepath.Join(opt.BundleDir, "bindings.yaml"), opt.Bindings); err != nil {
					t.Fatal(err)
				}
			}
			_, _, _, _, recheck, err := a.prepareKagentBundle(opt.BundleDir, opt.Bindings, opt.ToContext, "")
			if err != nil {
				t.Fatal(err)
			}
			switch which {
			case "bundle", "external parent":
				parent, names := opt.BundleDir, []string{"agent.yaml", "bindings.yaml"}
				if which == "external parent" {
					parent, names = filepath.Dir(opt.Bindings), []string{"bindings.yaml"}
				}
				if err := os.Rename(parent, parent+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range names {
					raw, err := os.ReadFile(filepath.Join(parent+"-old", name))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(parent, name), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
			default:
				path := filepath.Join(opt.BundleDir, which)
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path, path+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := recheck(); err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("identical-byte replacement authorized later writes: %v", err)
			}
		})
	}
}
