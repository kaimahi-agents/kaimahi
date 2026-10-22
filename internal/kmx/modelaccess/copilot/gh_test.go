package copilot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// The copied test executable is the only gh launched. No installed gh or real
// credentials are used, including on native Windows runners without a shell.
func init() {
	if os.Getenv("KMX_TEST_COPILOT_GH_HELPER") != "1" {
		return
	}
	if !slices.Equal(os.Args[1:], []string{"auth", "token", "--hostname", "github.com"}) {
		os.Exit(91)
	}
	fmt.Fprint(os.Stderr, canary)
	switch os.Getenv("KMX_TEST_COPILOT_GH_MODE") {
	case "overflow":
		fmt.Print(strings.Repeat("x", credentialLimit+1))
	case "failure":
		fmt.Print(canary)
		os.Exit(1)
	case "timeout":
		time.Sleep(time.Minute)
	default:
		fmt.Println(canary)
	}
	os.Exit(0)
}
func TestGHOutputCannotBypassLimitThroughReadFrom(t *testing.T) {
	var out limitedOutput
	_, err := io.Copy(&out, struct{ io.Reader }{strings.NewReader(strings.Repeat("x", 1<<20))})
	if !errors.Is(err, ErrMalformed) || out.Len() > credentialLimit {
		t.Fatalf("unbounded output: n=%d err=%v", out.Len(), err)
	}
}
func TestGHProcessIsBoundedAndHasNoSecretArguments(t *testing.T) {
	dir := t.TempDir()
	name := "gh"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("helper copy: %v %v", copyErr, closeErr)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TEST_COPILOT_GH_HELPER", "1")
	s := &memoryStore{}
	p := New(s)
	if err := p.LoginGH(context.Background()); err != nil || s.token != canary {
		t.Fatalf("hermetic gh success: %v", err)
	}
	t.Setenv("KMX_TEST_COPILOT_GH_MODE", "overflow")
	err = p.LoginGH(context.Background())
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("overflow: %v", err)
	}
	safe(t, err)
	t.Setenv("KMX_TEST_COPILOT_GH_MODE", "failure")
	err = p.LoginGH(context.Background())
	if err == nil {
		t.Fatal("failed process accepted")
	}
	safe(t, err)
	t.Setenv("KMX_TEST_COPILOT_GH_MODE", "timeout")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = p.LoginGH(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	safe(t, err)
}
