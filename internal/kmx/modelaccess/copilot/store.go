package copilot

import (
	"errors"
	"os"
	"runtime"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/zalando/go-keyring"
)

const credentialService = "kmx-model-copilot"
const credentialKey = "github-reusable-token"

type keychainStore struct {
	get    func(service, account string) (string, error)
	set    func(service, account, token string) error
	delete func(service, account string) error
}

// Only explicit credential operations probe OS services. Refuse autolaunch and
// alternative transports rather than starting a session bus in headless use.
func nativeKeychainAvailable(goos string) error {
	if goos != "linux" {
		return nil
	}
	if address := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); address != "" &&
		(!strings.HasPrefix(address, "unix:") || strings.Contains(address, ";")) {
		return ErrKeychain
	}
	// Use godbus's actual discovery, not an XDG_RUNTIME_DIR approximation.
	// Discovery retains the existing address in the environment for go-keyring;
	// this private connection only validates availability and is always closed.
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		return ErrKeychain
	}
	if err := conn.Close(); err != nil {
		return ErrKeychain
	}
	return nil
}

func (s *keychainStore) Get() (token string, err error) {
	// Native dependency calls can panic on unexpected OS service responses.
	// Keep that external failure payload out of both results and diagnostics.
	defer func() {
		if recover() != nil {
			token, err = "", ErrKeychain
		}
	}()
	if err := nativeKeychainAvailable(runtime.GOOS); err != nil {
		return "", err
	}
	token, err = s.get(credentialService, credentialKey)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrAuthRequired
	}
	if err != nil {
		return "", ErrKeychain
	}
	return token, nil
}

func (s *keychainStore) Set(token string) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrKeychain
		}
	}()
	return s.setToken(runtime.GOOS, token)
}

func (s *keychainStore) setToken(goos, token string) error {
	// v0.2.8 starts security -i before its 4096-byte encoded command check.
	// Bound raw bytes first: base64 plus our fixed item metadata fits safely.
	if goos == "darwin" && len(token) > 2048 {
		return ErrKeychain
	}
	if err := nativeKeychainAvailable(goos); err != nil {
		return err
	}
	if err := s.set(credentialService, credentialKey, token); err != nil {
		return ErrKeychain
	}
	return nil
}

func (s *keychainStore) Delete() (err error) {
	defer func() {
		if recover() != nil {
			err = ErrKeychain
		}
	}()
	if err := nativeKeychainAvailable(runtime.GOOS); err != nil {
		return err
	}
	if err := s.delete(credentialService, credentialKey); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return ErrKeychain
	}
	return nil
}
