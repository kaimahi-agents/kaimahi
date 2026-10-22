//go:build windows

package fileaccess

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCheckDefaultPrivateOwnerMatchesRawToken(t *testing.T) {
	owner, err := rawTokenSID(windows.TokenOwner)
	if err != nil {
		t.Fatal(err)
	}
	user, err := rawTokenSID(windows.TokenUser)
	if err != nil {
		t.Fatal(err)
	}
	got := CheckDefaultPrivateOwner()
	if owner.Equals(user) {
		t.Log("raw token branch: USER-OWNER")
		if got != nil {
			t.Fatalf("user default owner: %v", got)
		}
	} else {
		t.Log("raw token branch: GROUP-OWNER")
		if !errors.Is(got, ErrDefaultOwnerNotUser) {
			t.Fatalf("non-user default owner: %v, want ErrDefaultOwnerNotUser", got)
		}
	}
}

func TestCheckDefaultOwnerRefusesGroupOwner(t *testing.T) {
	user, err := rawTokenSID(windows.TokenUser)
	if err != nil {
		t.Fatal(err)
	}
	group, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic users keep the negative case meaningful even on a group-owned token.
	ordinary, err := windows.StringToSid("S-1-5-21-1-2-3-1001")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ owner, user *windows.SID }{
		{group, ordinary}, {nil, user}, {user, nil},
	} {
		if err := checkDefaultOwner(tc.owner, tc.user); !errors.Is(err, ErrDefaultOwnerNotUser) {
			t.Fatalf("non-user default owner = %v, want ErrDefaultOwnerNotUser", err)
		}
	}
	if err := checkDefaultOwner(user, user); err != nil {
		t.Fatalf("user default owner = %v", err)
	}
}
