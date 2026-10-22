//go:build windows

package fileaccess

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrDefaultOwnerNotUser identifies a process token whose default owner is
// not the token user (an elevated administrator's token typically defaults
// to the Administrators group). Objects that third-party code creates with
// inherited security then belong to that group, so private state is refused
// before any such write rather than relabeled afterwards.
var ErrDefaultOwnerNotUser = errors.New("the process token's default owner is not the current user; run kmx from an ordinary (non-elevated) session")

// CheckDefaultPrivateOwner refuses a process whose token default owner is not
// its user. Call it before code KMX does not control (for example ORAS
// stores) creates objects under a private directory. It only queries the
// token; it never changes the token, policy or privileges.
func CheckDefaultPrivateOwner() error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("query current user: %w", err)
	}
	owner, err := tokenDefaultOwner(token)
	if err != nil {
		return err
	}
	return checkDefaultOwner(owner, user.User.Sid)
}

func checkDefaultOwner(owner, user *windows.SID) error {
	if owner == nil || user == nil || !owner.IsValid() || !owner.Equals(user) {
		return ErrDefaultOwnerNotUser
	}
	return nil
}

// tokenDefaultOwner reads TOKEN_OWNER, which is a single SID pointer into the
// returned buffer. x/sys/windows has no typed accessor for it.
func tokenDefaultOwner(token windows.Token) (*windows.SID, error) {
	n := uint32(64)
	for {
		buf := make([]byte, n)
		err := windows.GetTokenInformation(token, windows.TokenOwner, &buf[0], uint32(len(buf)), &n)
		if err == nil {
			owner := (*struct{ Owner *windows.SID })(unsafe.Pointer(&buf[0])).Owner
			if owner == nil || !owner.IsValid() {
				return nil, errors.New("query token default owner: invalid SID")
			}
			// Copy out of buf so the SID does not alias the buffer.
			return owner.Copy()
		}
		if err != windows.ERROR_INSUFFICIENT_BUFFER || n <= uint32(len(buf)) {
			return nil, fmt.Errorf("query token default owner: %w", err)
		}
	}
}
