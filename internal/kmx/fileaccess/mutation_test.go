package fileaccess

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func privateTestDirectory(t *testing.T) (string, *Directory) {
	t.Helper()
	base := mutationTestBase(t)
	path := filepath.Join(base, "private")
	directory, err := EnsurePrivateDirectory(t.Context(), path)
	if err != nil {
		t.Fatalf("EnsurePrivateDirectory: %v", err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	return path, directory
}

func TestPrivateFirstCreationAndExistingRefusal(t *testing.T) {
	path, directory := privateTestDirectory(t)
	file, err := CreatePrivateFileAt(t.Context(), directory, "lock")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := CheckPrivate(t.Context(), file, false); err != nil {
		t.Fatalf("created private file = %v", err)
	}
	if _, err := CreatePrivateFileAt(t.Context(), directory, "lock"); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("exclusive private file create = %v, want unsafe collision", err)
	}
	if got, err := EnsurePrivateFileAt(t.Context(), directory, "lock"); err != nil {
		t.Fatalf("ensure existing private file = %v", err)
	} else {
		got.Close()
	}

	child, err := CreatePrivateDirectoryAt(t.Context(), directory, "child")
	if err != nil {
		t.Fatal(err)
	}
	child.Close()
	if _, err := CreatePrivateDirectoryAt(t.Context(), directory, "child"); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("exclusive private directory create = %v, want unsafe collision", err)
	}
	if err := makeExistingPrivateUnsafe(t, filepath.Join(path, "lock")); err != nil {
		t.Skipf("cannot construct an unsafe existing private object on this host: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(path, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnsurePrivateFileAt(t.Context(), directory, "lock"); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("unsafe existing private file = %v, want unsafe", err)
	}
	after, err := os.ReadFile(filepath.Join(path, "lock"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("refusal changed existing bytes: before=%q after=%q err=%v", before, after, err)
	}
}

func TestRemovePrivateFileAtRequiresExactObservedObject(t *testing.T) {
	path, directory := privateTestDirectory(t)
	first, err := CreatePrivateFileAt(t.Context(), directory, "first")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := CreatePrivateFileAt(t.Context(), directory, "second")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := RemovePrivateFileAt(t.Context(), directory, "first", second); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("mismatched removal = %v, want unsafe", err)
	}
	if _, err := os.Stat(filepath.Join(path, "first")); err != nil {
		t.Fatalf("mismatched removal changed first: %v", err)
	}
	if err := RemovePrivateFileAt(t.Context(), directory, "first", first); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(path, "first")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exact removal left first: %v", err)
	}
	if err := RemovePrivateFileAt(t.Context(), directory, "first", first); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing removal = %v, want missing", err)
	}
}

func TestPrivateCreationHonorsCancellationBeforeEffects(t *testing.T) {
	path, directory := privateTestDirectory(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if file, err := CreatePrivateFileAt(ctx, directory, "file"); !errors.Is(err, context.Canceled) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("cancelled file creation = %v", err)
	}
	if child, err := CreatePrivateDirectoryAt(ctx, directory, "child"); !errors.Is(err, context.Canceled) {
		if child != nil {
			child.Close()
		}
		t.Fatalf("cancelled directory creation = %v", err)
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled creation changed directory: %v, %v", entries, err)
	}
}

func TestPublishPrivateExclusiveAndDurable(t *testing.T) {
	path, directory := privateTestDirectory(t)
	result, err := PublishPrivate(t.Context(), directory, "result", []byte("complete"))
	if err != nil || !result.Visible || !result.VisibilityKnown {
		t.Fatalf("PublishPrivate = %+v, %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(path, "result"))
	if err != nil || string(data) != "complete" {
		t.Fatalf("published bytes = %q, %v", data, err)
	}
	if result, err := PublishPrivate(t.Context(), directory, "result", []byte("replacement")); err == nil || result.Visible || !result.VisibilityKnown {
		t.Fatalf("occupied publication = %+v, %v", result, err)
	}
	data, err = os.ReadFile(filepath.Join(path, "result"))
	if err != nil || string(data) != "complete" {
		t.Fatalf("collision changed published bytes = %q, %v", data, err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".kmx-stage-") {
			t.Fatalf("publication retained stage %q", entry.Name())
		}
	}
}

func TestPublishPrivateCompetingPublishersHaveOneWinner(t *testing.T) {
	path, directory := privateTestDirectory(t)
	start := make(chan struct{})
	type outcome struct {
		result PublicationResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for _, data := range [][]byte{[]byte("first"), []byte("second")} {
		data := data
		go func() {
			<-start
			result, err := PublishPrivate(context.Background(), directory, "result", data)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	successes := 0
	for range 2 {
		got := <-outcomes
		if got.err == nil {
			successes++
			if !got.result.Visible || !got.result.VisibilityKnown {
				t.Fatal("successful publisher did not cross visibility")
			}
		} else if got.result.Visible || !got.result.VisibilityKnown {
			t.Fatalf("losing publisher reported visibility: %v", got.err)
		}
	}
	if successes != 1 {
		t.Fatalf("publisher successes = %d, want 1", successes)
	}
	data, err := os.ReadFile(filepath.Join(path, "result"))
	if err != nil || string(data) != "first" && string(data) != "second" {
		t.Fatalf("winning bytes = %q, %v", data, err)
	}
}

func TestPublishPrivateFaultBoundaries(t *testing.T) {
	injected := errors.New("injected publication failure")
	t.Run("cancelled-before-effects", func(t *testing.T) {
		path, directory := privateTestDirectory(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result, err := PublishPrivate(ctx, directory, "result", []byte("complete"))
		if !errors.Is(err, context.Canceled) || result.Visible || !result.VisibilityKnown {
			t.Fatalf("preflight cancellation = %+v, %v", result, err)
		}
		entries, readErr := os.ReadDir(path)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("cancelled publication changed directory: %v, %v", entries, readErr)
		}
	})

	t.Run("write-before-visibility", func(t *testing.T) {
		path, directory := privateTestDirectory(t)
		oldWrite := writePrivateBytes
		writePrivateBytes = func(*PrivateFile, []byte) error { return injected }
		t.Cleanup(func() { writePrivateBytes = oldWrite })
		result, err := PublishPrivate(t.Context(), directory, "result", []byte("complete"))
		if err == nil || result.Visible || !result.VisibilityKnown {
			t.Fatalf("write failure = %+v, %v", result, err)
		}
		if _, statErr := os.Lstat(filepath.Join(path, "result")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("write failure published output: %v", statErr)
		}
	})

	t.Run("file-sync-before-visibility", func(t *testing.T) {
		path, directory := privateTestDirectory(t)
		oldSync := syncPrivateFile
		syncPrivateFile = func(*PrivateFile) error { return injected }
		t.Cleanup(func() { syncPrivateFile = oldSync })
		result, err := PublishPrivate(t.Context(), directory, "result", []byte("complete"))
		if err == nil || result.Visible || !result.VisibilityKnown {
			t.Fatalf("file sync failure = %+v, %v", result, err)
		}
		if _, statErr := os.Lstat(filepath.Join(path, "result")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("file sync failure published output: %v", statErr)
		}
	})

	t.Run("link-before-visibility", func(t *testing.T) {
		path, directory := privateTestDirectory(t)
		oldLink := linkPublishedEntry
		linkPublishedEntry = func(context.Context, *Directory, string, string, string) (bool, error) { return false, injected }
		t.Cleanup(func() { linkPublishedEntry = oldLink })
		result, err := PublishPrivate(t.Context(), directory, "result", []byte("complete"))
		if err == nil || result.Visible || !result.VisibilityKnown {
			t.Fatalf("link failure = %+v, %v", result, err)
		}
		if _, statErr := os.Lstat(filepath.Join(path, "result")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("link failure published output: %v", statErr)
		}
	})

	t.Run("link-error-after-visibility", func(t *testing.T) {
		path, directory := privateTestDirectory(t)
		oldAfter := afterPrivateLink
		afterPrivateLink = func() error { return injected }
		t.Cleanup(func() { afterPrivateLink = oldAfter })
		result, err := PublishPrivate(t.Context(), directory, "result", []byte("complete"))
		if err == nil || !result.Visible || !result.VisibilityKnown {
			t.Fatalf("post-effect link error = %+v, %v", result, err)
		}
		data, readErr := os.ReadFile(filepath.Join(path, "result"))
		if readErr != nil || string(data) != "complete" {
			t.Fatalf("post-effect link output = %q, %v", data, readErr)
		}
	})

	t.Run("cancellation-with-cleanup-failure", func(t *testing.T) {
		path, directory := privateTestDirectory(t)
		ctx, cancel := context.WithCancel(context.Background())
		oldWrite := writePrivateBytes
		oldRemove := removeStagedEntry
		writePrivateBytes = func(*PrivateFile, []byte) error {
			cancel()
			return context.Canceled
		}
		removeStagedEntry = func(context.Context, *Directory, string, string) error { return injected }
		t.Cleanup(func() {
			writePrivateBytes = oldWrite
			removeStagedEntry = oldRemove
			entries, _ := os.ReadDir(path)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".kmx-stage-") {
					_ = os.Remove(filepath.Join(path, entry.Name()))
				}
			}
		})
		result, err := PublishPrivate(ctx, directory, "result", []byte("complete"))
		if result.Visible || !result.VisibilityKnown || !errors.Is(err, ErrUnreadable) || errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation plus cleanup failure = %+v, %v", result, err)
		}
	})

	t.Run("stage-retirement-after-visibility", func(t *testing.T) {
		path, directory := privateTestDirectory(t)
		oldRemove := removeStagedEntry
		calls := 0
		removeStagedEntry = func(context.Context, *Directory, string, string) error {
			calls++
			return injected
		}
		t.Cleanup(func() {
			removeStagedEntry = oldRemove
			entries, _ := os.ReadDir(path)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".kmx-stage-") {
					_ = os.Remove(filepath.Join(path, entry.Name()))
				}
			}
		})
		result, err := PublishPrivate(t.Context(), directory, "result", []byte("complete"))
		if err == nil || !result.Visible || !result.VisibilityKnown {
			t.Fatalf("retirement failure = %+v, %v", result, err)
		}
		data, readErr := os.ReadFile(filepath.Join(path, "result"))
		if readErr != nil || string(data) != "complete" {
			t.Fatalf("visible retirement output = %q, %v", data, readErr)
		}
		if calls != 1 {
			t.Fatalf("ambiguous stage retirement retried %d times", calls)
		}
		entries, readErr := os.ReadDir(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		stages := 0
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".kmx-stage-") {
				stages++
			}
		}
		if stages != 1 {
			t.Fatalf("ambiguous retirement stages = %d, want retained private stage", stages)
		}
	})

	t.Run("directory-sync-after-visibility", func(t *testing.T) {
		path, directory := privateTestDirectory(t)
		oldSync := syncPublishedDir
		syncPublishedDir = func(context.Context, *Directory) error { return injected }
		t.Cleanup(func() { syncPublishedDir = oldSync })
		result, err := PublishPrivate(t.Context(), directory, "result", []byte("complete"))
		if err == nil || !result.Visible || !result.VisibilityKnown {
			t.Fatalf("directory sync failure = %+v, %v", result, err)
		}
		var access *AccessError
		if !errors.As(err, &access) || access.Operation != "persist visible private file" {
			t.Fatalf("post-link failure = %v, want visible-persistence operation", err)
		}
		data, readErr := os.ReadFile(filepath.Join(path, "result"))
		if readErr != nil || string(data) != "complete" {
			t.Fatalf("visible uncertain output = %q, %v", data, readErr)
		}
	})

	t.Run("cancellation-after-visibility", func(t *testing.T) {
		path, directory := privateTestDirectory(t)
		ctx, cancel := context.WithCancel(context.Background())
		oldLink := linkPublishedEntry
		linkPublishedEntry = func(ctx context.Context, directory *Directory, stage, name, identity string) (bool, error) {
			visible, err := oldLink(ctx, directory, stage, name, identity)
			if err == nil {
				cancel()
			}
			return visible, err
		}
		t.Cleanup(func() { linkPublishedEntry = oldLink })
		result, err := PublishPrivate(ctx, directory, "result", []byte("complete"))
		if !errors.Is(err, context.Canceled) || !result.Visible || !result.VisibilityKnown {
			t.Fatalf("post-link cancellation = %+v, %v", result, err)
		}
		data, readErr := os.ReadFile(filepath.Join(path, "result"))
		if readErr != nil || string(data) != "complete" {
			t.Fatalf("cancelled visible output = %q, %v", data, readErr)
		}
	})
}

func TestSyncDirectoryStaysAnchoredAfterPathReplacement(t *testing.T) {
	base := mutationTestBase(t)
	original := filepath.Join(base, "original")
	directory, err := EnsurePrivateDirectory(t.Context(), original)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	want, err := PhysicalIdentity(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(base, "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	replacement, err := EnsurePrivateDirectory(t.Context(), original)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if got, err := PhysicalIdentity(t.Context(), replacement); err != nil || got == want {
		t.Fatalf("replacement identity = %q, %v; want distinct from %q", got, err, want)
	}
	if err := SyncDirectory(t.Context(), directory); err != nil {
		t.Fatalf("sync retained directory = %v", err)
	}
	opened, err := OpenPrivateDirectory(t.Context(), moved)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if got, err := PhysicalIdentity(t.Context(), opened); err != nil || got != want {
		t.Fatalf("moved identity = %q, %v; want %q", got, err, want)
	}
}

const privateLockHelperEnv = "KMX_FILEACCESS_PRIVATE_LOCK_HELPER"

func TestPrivateLockHelperProcess(t *testing.T) {
	path := os.Getenv(privateLockHelperEnv)
	if path == "" {
		return
	}
	directory, err := OpenPrivateDirectory(t.Context(), filepath.Dir(path))
	if err != nil {
		os.Stdout.WriteString("error\n")
		os.Exit(2)
	}
	file, err := EnsurePrivateFileAt(t.Context(), directory, filepath.Base(path))
	if err != nil {
		os.Stdout.WriteString("error\n")
		os.Exit(2)
	}
	if TryLock(t.Context(), file) != nil {
		os.Stdout.WriteString("busy\n")
	} else {
		os.Stdout.WriteString("locked\n")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

type privateLockHelper struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	state string
}

func startPrivateLockHelper(t *testing.T, path string) *privateLockHelper {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestPrivateLockHelperProcess$")
	cmd.Env = append(os.Environ(), privateLockHelperEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	helper := &privateLockHelper{cmd: cmd, stdin: stdin}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read lock helper: %v", err)
	}
	helper.state = strings.TrimSpace(line)
	return helper
}

func (h *privateLockHelper) release(t *testing.T) {
	t.Helper()
	_ = h.stdin.Close()
	if err := h.cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func (h *privateLockHelper) kill(t *testing.T) {
	t.Helper()
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = h.cmd.Wait()
}

func tryPrivateLockEventually(t *testing.T, file *PrivateFile) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := TryLock(t.Context(), file); err == nil {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("lock was not released: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestPrivateLockAcrossProcessesAndAliases(t *testing.T) {
	path, directory := privateTestDirectory(t)
	file, err := EnsurePrivateFileAt(t.Context(), directory, "lock")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lockPath := filepath.Join(path, "lock")
	aliasPath := filepath.Join(path, "lock-alias")
	if err := os.Link(lockPath, aliasPath); err != nil {
		t.Fatal(err)
	}

	helper := startPrivateLockHelper(t, aliasPath)
	if helper.state != "locked" {
		t.Fatalf("helper state %q", helper.state)
	}
	if err := TryLock(t.Context(), file); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("alias contention = %v, want unsafe busy", err)
	}
	helper.release(t)
	tryPrivateLockEventually(t, file)
	file.Close()

	second, err := EnsurePrivateFileAt(t.Context(), directory, "lock")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	helper = startPrivateLockHelper(t, aliasPath)
	if helper.state != "locked" {
		t.Fatalf("death helper state %q", helper.state)
	}
	helper.kill(t)
	tryPrivateLockEventually(t, second)
}
