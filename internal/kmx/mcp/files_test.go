package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// This fixture substitutes only the pending parser binding, not a runner.
func fixtureValidator(_ string, b []byte) error {
	if !strings.HasPrefix(string(b), "valid:") {
		return errors.New("private source detail")
	}
	return nil
}
func filesFixture(t *testing.T) (*Files, string) {
	t.Helper()
	root := t.TempDir()
	f, err := OpenFiles(root, fixtureValidator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, root
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatal("unsafe error")
	}
}
func TestFilesCreateEdit(t *testing.T) {
	f, root := filesFixture(t)
	ctx := context.Background()
	a, err := f.Create(ctx, "greeter.agent.md", []byte("valid:before\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Path != "greeter.agent.md" || a.Revision != revision([]byte("valid:before\r\n")) {
		t.Fatalf("identity: %+v", a)
	}
	_, err = f.Create(ctx, a.Path, []byte("valid:overwrite"))
	requireCode(t, err, "already_exists")
	_, err = f.Edit(ctx, a.Path, a.Revision, []byte("invalid"))
	requireCode(t, err, "invalid_file")
	_, err = f.Edit(ctx, a.Path, "sha256:"+strings.Repeat("0", 64), []byte("valid:stale"))
	requireCode(t, err, "stale_revision")
	b, err := f.Edit(ctx, a.Path, a.Revision, []byte("valid:after"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Revision == a.Revision {
		t.Fatal("unchanged revision")
	}
	got, err := os.ReadFile(filepath.Join(root, a.Path))
	if err != nil || string(got) != "valid:after" {
		t.Fatalf("content %q %v", got, err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatalf("residue: %v", entries)
	}
}
func TestFilesBoundaries(t *testing.T) {
	f, root := filesFixture(t)
	ctx := context.Background()
	for _, p := range []string{"../x.agent.md", "/x.agent.md", "x/../x.agent.md", "x\\y.agent.md", "C:x.agent.md", ".git/config", "x.txt", ".squad/team.md", ".squad/agents/x/history.md", "CON.agent.md", "x .agent.md", "x.agent.md "} {
		t.Run(p, func(t *testing.T) { _, err := f.Create(ctx, p, []byte("valid:x")); requireCode(t, err, "unsafe_path") })
	}
	_, missingErr := f.Edit(ctx, "missing.agent.md", "sha256:"+strings.Repeat("0", 64), []byte("valid:x"))
	requireCode(t, missingErr, "not_found")
	_, missingErr = f.Edit(ctx, ".squad/agents/missing/charter.md", "sha256:"+strings.Repeat("0", 64), []byte("valid:x"))
	requireCode(t, missingErr, "not_found")
	_, err := f.Create(ctx, "bad.agent.md", []byte("bad"))
	requireCode(t, err, "invalid_file")
	_, err = f.Create(ctx, "big.agent.md", []byte("valid:"+strings.Repeat("x", maxFileBytes)))
	requireCode(t, err, "invalid_file")
	c, cancel := context.WithCancel(ctx)
	cancel()
	_, err = f.Create(c, "cancel.agent.md", []byte("valid:x"))
	requireCode(t, err, "cancelled")
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("unexpected effects: %v", entries)
	}
	for _, p := range []string{".", filepath.Join(root, "absent")} {
		_, err := OpenFiles(p, fixtureValidator)
		requireCode(t, err, "invalid_root")
	}
	_, err = OpenFiles(root, nil)
	requireCode(t, err, "invalid_options")
}
func TestFilesSquadPlacement(t *testing.T) {
	f, root := filesFixture(t)
	_, err := f.Create(context.Background(), ".squad/agents/greeter/charter.md", []byte("valid:x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".squad/agents/greeter/charter.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".squad/team.md")); !os.IsNotExist(err) {
		t.Fatal("team metadata touched")
	}
}
func TestFilesSymlinks(t *testing.T) {
	f, root := filesFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".squad")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := f.Create(context.Background(), ".squad/agents/x/charter.md", []byte("valid:x"))
	requireCode(t, err, "unsafe_path")
	if err := os.WriteFile(filepath.Join(outside, "x.agent.md"), []byte("valid:outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "x.agent.md"), filepath.Join(root, "x.agent.md")); err != nil {
		t.Fatal(err)
	}
	_, err = f.Edit(context.Background(), "x.agent.md", revision([]byte("valid:outside")), []byte("valid:changed"))
	requireCode(t, err, "unsafe_path")
	got, _ := os.ReadFile(filepath.Join(outside, "x.agent.md"))
	if string(got) != "valid:outside" {
		t.Fatal("outside changed")
	}
}
func TestFilesConcurrentCreate(t *testing.T) {
	f, _ := filesFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() { _, err := f.Create(context.Background(), "x.agent.md", []byte("valid:x")); results <- err })
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else {
			requireCode(t, err, "already_exists")
		}
	}
	if wins != 1 {
		t.Fatalf("winners %d", wins)
	}
}
func TestFilesCooperativeEditLock(t *testing.T) {
	f, root := filesFixture(t)
	a, err := f.Create(context.Background(), "x.agent.md", []byte("valid:old"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	held, err := openTarget(p, "x.agent.md")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	unlock, err := lockTarget(held)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Edit(context.Background(), a.Path, a.Revision, []byte("valid:new"))
	requireCode(t, err, "busy")
	unlock()
	_, err = f.Edit(context.Background(), a.Path, a.Revision, []byte("valid:new"))
	if err != nil {
		t.Fatal(err)
	}
}
func TestLockFailureDistinguishesUnavailableFromBusy(t *testing.T) {
	_, root := filesFixture(t)
	parent, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "x.agent.md"), []byte("valid:x"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := openTarget(parent, "x.agent.md")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	parent.Close()
	_, err = lockTarget(file)
	if err == nil {
		t.Fatal("closed descriptor locked")
	}
	requireCode(t, lockFailure(err), "lock_unavailable")
}

func TestFilesEditCancellationAfterValidation(t *testing.T) {
	f, root := filesFixture(t)
	a, err := f.Create(context.Background(), "x.agent.md", []byte("valid:old"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.validate = func(_ string, b []byte) error {
		if string(b) == "valid:new" {
			cancel()
		}
		return nil
	}
	_, err = f.Edit(ctx, a.Path, a.Revision, []byte("valid:new"))
	requireCode(t, err, "cancelled")
	got, _ := os.ReadFile(filepath.Join(root, a.Path))
	if string(got) != "valid:old" {
		t.Fatal("cancel modified target")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatal("temp residue")
	}
}
