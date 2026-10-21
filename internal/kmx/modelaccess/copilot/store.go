package copilot

import (
	"errors"

	"github.com/99designs/keyring"
)

var keyringOpen = keyring.Open

const credentialKey = "github-reusable-token"

type keychainStore struct {
	open func(keyring.Config) (keyring.Keyring, error)
}

func (s *keychainStore) ring() (keyring.Keyring, error) {
	ring, err := s.open(keyring.Config{AllowedBackends: []keyring.BackendType{keyring.WinCredBackend, keyring.KeychainBackend, keyring.SecretServiceBackend}, ServiceName: "kmx-model-copilot", WinCredPrefix: "kmx-model-copilot:", KeychainAccessibleWhenUnlocked: true})
	if err != nil {
		return nil, ErrKeychain
	}
	return ring, nil
}
func (s *keychainStore) Get() (token string, err error) {
	// Native dependency calls can panic on unexpected OS service responses.
	// Keep that external failure payload out of both results and diagnostics.
	defer func() {
		if recover() != nil {
			token, err = "", ErrKeychain
		}
	}()
	ring, err := s.ring()
	if err != nil {
		return "", err
	}
	item, err := ring.Get(credentialKey)
	if errors.Is(err, keyring.ErrKeyNotFound) {
		return "", ErrAuthRequired
	}
	if err != nil {
		return "", ErrKeychain
	}
	return string(item.Data), nil
}
func (s *keychainStore) Set(token string) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrKeychain
		}
	}()
	ring, err := s.ring()
	if err != nil {
		return err
	}
	if err := ring.Set(keyring.Item{Key: credentialKey, Data: []byte(token), Label: "kmx Copilot model access", KeychainNotSynchronizable: true}); err != nil {
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
	ring, err := s.ring()
	if err != nil {
		return err
	}
	if err := ring.Remove(credentialKey); err != nil && !errors.Is(err, keyring.ErrKeyNotFound) {
		return ErrKeychain
	}
	return nil
}
