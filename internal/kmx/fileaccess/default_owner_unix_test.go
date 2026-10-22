//go:build !windows

package fileaccess

import "testing"

func TestCheckDefaultPrivateOwnerIsUnixNoOp(t *testing.T) {
	if err := CheckDefaultPrivateOwner(); err != nil {
		t.Fatalf("CheckDefaultPrivateOwner() = %v, want nil outside Windows", err)
	}
}
