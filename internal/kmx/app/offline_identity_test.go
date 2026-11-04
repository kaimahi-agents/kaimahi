package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Reading os.Executable's pathname would hash an atomic replacement rather than
// the process whose metadata runningOfflineIdentity has already checked.
func TestOfflineRunningBinaryDigestAtomicReplacement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux running-executable identity")
	}
	if os.Getenv("KMX_OFFLINE_DIGEST_CHILD") == "1" {
		fmt.Fprintln(os.Stdout, "ready")
		var signal [1]byte
		if _, err := io.ReadFull(os.Stdin, signal[:]); err != nil {
			t.Fatal(err)
		}
		digest, err := offlineRunningBinaryDigest()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, digest)
		return
	}

	// Copy only our test process's actual inode, never overwrite a source or
	// cached test executable. Derive the expected digest while copying bytes.
	source, err := os.Open("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.test")
	dest, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(dest, hash), source)
	closeErr := dest.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy running test executable: %v; close: %v", copyErr, closeErr)
	}
	want := fmt.Sprintf("%x", hash.Sum(nil))

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-test.run=^TestOfflineRunningBinaryDigestAtomicReplacement$")
	cmd.Env = append(os.Environ(), "KMX_OFFLINE_DIGEST_CHILD=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	reader := bufio.NewReader(stdout)
	ready, err := reader.ReadString('\n')
	if err != nil || ready != "ready\n" {
		t.Fatalf("child did not start: %q, %v; %s", ready, err, stderr.String())
	}
	replacement := []byte("not the running executable\n")
	staged := filepath.Join(dir, "replacement")
	if err := os.WriteFile(staged, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, path); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	result, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read child digest: %v; %s", err, stderr.String())
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child digest failed: %v; %s", err, stderr.String())
	}
	if got := strings.TrimSpace(result); got != want {
		t.Fatalf("running digest = %s; want original %s; replacement digest = %x", got, want, sha256.Sum256(replacement))
	}
}
