package copilot

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func fixtureStore(t *testing.T) *keychainStore {
	t.Helper()
	// Replace credential operations, but let the real Linux bus preflight
	// connect only to this owned socket. No authentication or OS keychain.
	if runtime.GOOS == "linux" {
		dir, err := os.MkdirTemp("", "kmx-store-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		path := filepath.Join(dir, "bus")
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()
		t.Cleanup(func() { _ = listener.Close(); <-done })
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+path)
	}
	return &keychainStore{}
}

func TestNativeStorePanicsBecomeSafeErrors(t *testing.T) {
	s := fixtureStore(t)
	s.get = func(string, string) (string, error) { panic(canary) }
	s.set = func(string, string, string) error { panic(canary) }
	s.delete = func(string, string) error { panic(canary) }
	token, err := s.Get()
	if token != "" || !errors.Is(err, ErrKeychain) {
		t.Fatal("native panic escaped safe Get boundary")
	}
	for _, err := range []error{s.Set(canary), s.Delete()} {
		if !errors.Is(err, ErrKeychain) {
			t.Fatal("native panic escaped safe write boundary")
		}
		safe(t, err)
	}
	safe(t, token, err)
}

func TestNativeStoreOwnsOnlyItsCredential(t *testing.T) {
	s := fixtureStore(t)
	items := map[[2]string]string{
		{"other-service", "github-reusable-token"}: "unrelated",
		{"kmx-model-copilot", "editor"}:            "unrelated",
	}
	check := func(service, account string) [2]string {
		t.Helper()
		if service != "kmx-model-copilot" || account != "github-reusable-token" {
			t.Fatal("operation escaped kmx's exact owned item")
		}
		return [2]string{service, account}
	}
	s.get = func(service, account string) (string, error) {
		v, ok := items[check(service, account)]
		if !ok {
			return "", keyring.ErrNotFound
		}
		return v, nil
	}
	s.set = func(service, account, token string) error {
		items[check(service, account)] = token
		return nil
	}
	s.delete = func(service, account string) error {
		key := check(service, account)
		if _, ok := items[key]; !ok {
			return keyring.ErrNotFound
		}
		delete(items, key)
		return nil
	}
	if _, err := s.Get(); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("absent: %v", err)
	}
	if err := s.Set(canary); err != nil {
		t.Fatal(err)
	}
	if token, err := s.Get(); token != canary || err != nil {
		t.Fatal("credential not retained")
	}
	if err := s.Delete(); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("absent delete: %v", err)
	}
	if len(items) != 2 || items[[2]string{"other-service", "github-reusable-token"}] != "unrelated" ||
		items[[2]string{"kmx-model-copilot", "editor"}] != "unrelated" {
		t.Fatal("changed another owner's credential")
	}
}

func TestNativeStoreErrorsDiscardPayloads(t *testing.T) {
	s := fixtureStore(t)
	s.get = func(string, string) (string, error) { return canary, errors.New(canary) }
	s.set = func(string, string, string) error { return errors.New(canary) }
	s.delete = func(string, string) error { return errors.New(canary) }
	token, err := s.Get()
	if token != "" || !errors.Is(err, ErrKeychain) {
		t.Fatal("unsafe Get result")
	}
	for _, err := range []error{s.Set(canary), s.Delete()} {
		if !errors.Is(err, ErrKeychain) {
			t.Fatal("unsafe native error")
		}
		safe(t, err)
	}
	safe(t, token, err)
}

func TestDarwinStoreRejectsOversizedSecretBeforeNativeSet(t *testing.T) {
	s := fixtureStore(t)
	calls := 0
	s.set = func(service, account, token string) error {
		calls++
		if len(token) != 2048 {
			t.Fatal("unexpected token passed to native Set")
		}
		return nil
	}
	if err := s.setToken("darwin", strings.Repeat("x", 2048)); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{strings.Repeat("x", 2049), strings.Repeat("é", 1025)} {
		if err := s.setToken("darwin", token); !errors.Is(err, ErrKeychain) {
			t.Fatal("oversized secret accepted")
		}
	}
	if calls != 1 {
		t.Fatal("oversized secret reached native Set")
	}
}

func TestLinuxStoreRequiresExistingBusBeforeNativeOperations(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux preflight")
	}
	missing := "unix:path=" + filepath.Join(t.TempDir(), "missing")
	for _, address := range []string{"", missing, "autolaunch:", "tcp:host=localhost", "unix:path=/missing;autolaunch:"} {
		t.Run(address, func(t *testing.T) {
			if address == "" {
				// Never probe a developer's existing bus. Headless CI can
				// exercise no-address refusal without a real daemon.
				for _, name := range []string{"bus", "dbus-session"} {
					if _, err := os.Stat(filepath.Join("/run/user", strconv.Itoa(os.Getuid()), name)); !os.IsNotExist(err) {
						t.Skip("host has a discoverable session bus")
					}
				}
			}
			s := fixtureStore(t)
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
			dir := t.TempDir()
			t.Setenv("XDG_RUNTIME_DIR", dir)
			t.Setenv("PATH", dir)
			marker := filepath.Join(dir, "launched")
			t.Setenv("KMX_TEST_DBUS_MARKER", marker)
			if err := os.WriteFile(filepath.Join(dir, "dbus-launch"), []byte("#!/bin/sh\nprintf called > \"$KMX_TEST_DBUS_MARKER\"\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			s.get = func(string, string) (string, error) { t.Fatal("native Get called without safe bus"); return "", nil }
			s.set = func(string, string, string) error { t.Fatal("native Set called without safe bus"); return nil }
			s.delete = func(string, string) error { t.Fatal("native Delete called without safe bus"); return nil }
			token, err := s.Get()
			if token != "" || !errors.Is(err, ErrKeychain) {
				t.Fatal("unsafe Get result")
			}
			for _, err := range []error{s.Set(canary), s.Delete()} {
				if !errors.Is(err, ErrKeychain) {
					t.Fatal("missing-bus operation accepted")
				}
				safe(t, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("credential operation invoked dbus-launch")
			}
		})
	}
}

func TestLinuxBusPreflightRejectsRegularFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux socket fixture")
	}
	path := filepath.Join(t.TempDir(), "bus")
	if err := os.WriteFile(path, []byte("not a bus"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+path)
	if err := nativeKeychainAvailable("linux"); !errors.Is(err, ErrKeychain) {
		t.Fatal("regular file accepted")
	}
}
