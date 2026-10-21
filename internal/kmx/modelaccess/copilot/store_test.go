package copilot

import (
	"errors"
	"reflect"
	"testing"

	"github.com/99designs/keyring"
)

type ringFixture struct {
	keyring.Keyring
	items map[string]keyring.Item
}

func (r *ringFixture) Get(k string) (keyring.Item, error) {
	v, ok := r.items[k]
	if !ok {
		return v, keyring.ErrKeyNotFound
	}
	return v, nil
}
func (r *ringFixture) Set(v keyring.Item) error { r.items[v.Key] = v; return nil }
func (r *ringFixture) Remove(k string) error    { delete(r.items, k); return nil }

type panicRing struct{ keyring.Keyring }

func (*panicRing) Get(string) (keyring.Item, error) { panic(canary) }
func (*panicRing) Set(keyring.Item) error           { panic(canary) }
func (*panicRing) Remove(string) error              { panic(canary) }

func TestNativeStorePanicsBecomeSafeErrors(t *testing.T) {
	for _, opening := range []bool{false, true} {
		for _, operation := range []string{"get", "set", "delete"} {
			name := operation
			if opening {
				name += "-open"
			}
			t.Run(name, func(t *testing.T) {
				defer func() {
					if recover() != nil {
						t.Error("native dependency panic escaped store boundary")
					}
				}()
				s := &keychainStore{open: func(keyring.Config) (keyring.Keyring, error) {
					if opening {
						panic(canary)
					}
					return &panicRing{}, nil
				}}
				var token string
				var err error
				switch operation {
				case "get":
					token, err = s.Get()
				case "set":
					err = s.Set(canary)
				case "delete":
					err = s.Delete()
				}
				if !errors.Is(err, ErrKeychain) || token != "" {
					t.Fatalf("native panic did not return safe empty result: err=%v", err)
				}
				safe(t, token, err)
			})
		}
	}
}

func TestNativeStoreOwnsOnlyItsCredential(t *testing.T) {
	ring := &ringFixture{items: map[string]keyring.Item{"editor": {Key: "editor", Data: []byte("unrelated")}}}
	s := &keychainStore{open: func(c keyring.Config) (keyring.Keyring, error) {
		if !reflect.DeepEqual(c.AllowedBackends, []keyring.BackendType{keyring.WinCredBackend, keyring.KeychainBackend, keyring.SecretServiceBackend}) || c.ServiceName != "kmx-model-copilot" || c.WinCredPrefix != "kmx-model-copilot:" || c.FileDir != "" || c.PassCmd != "" {
			t.Fatal("unsafe backend configuration")
		}
		return ring, nil
	}}
	if _, err := s.Get(); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("absent: %v", err)
	}
	if err := s.Set(canary); err != nil {
		t.Fatal(err)
	}
	v, err := s.Get()
	if err != nil || v != canary {
		t.Fatal("credential not retained")
	}
	if err := s.Delete(); err != nil {
		t.Fatal(err)
	}
	if len(ring.items) != 1 || string(ring.items["editor"].Data) != "unrelated" {
		t.Fatal("deleted another owner's credential")
	}
	s.open = func(keyring.Config) (keyring.Keyring, error) { return nil, errors.New(canary) }
	_, err = s.Get()
	if !errors.Is(err, ErrKeychain) {
		t.Fatal("fallback or raw error")
	}
	safe(t, err, s.Set(canary), s.Delete())
}
