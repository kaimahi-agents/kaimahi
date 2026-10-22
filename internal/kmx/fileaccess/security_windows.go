//go:build windows

package fileaccess

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var volumeInformationByHandle = windows.GetVolumeInformationByHandle

// CheckPrivate refuses file unless it is a disk file or directory, not a
// reparse point or device, on a filesystem with persistent ACLs, owned by the
// current token user, whose DACL is present and holds only ordinary allow
// ACEs for that owner (explicit or inherited). A root must also be a
// directory with a protected DACL. This matches the AgentSuite private
// directory policy: SYSTEM and Administrators are not granted access to
// private state, and nothing is repaired.
func CheckPrivate(file *os.File, root bool) error {
	return withHandle(file, func(h windows.Handle) error { return checkPrivateHandle(h, root) })
}

func checkPrivateHandle(handle windows.Handle, root bool) error {
	kind, err := windows.GetFileType(handle)
	if err != nil {
		return fmt.Errorf("query private object type: %w", err)
	}
	if kind != windows.FILE_TYPE_DISK {
		return errors.New("private object is not a disk file or directory")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("query private object attributes: %w", err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return errors.New("private object is a reparse point or device")
	}
	if root && info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return errors.New("private root is not a directory")
	}
	if err := requirePersistentACLs(handle); err != nil {
		return err
	}
	user, err := currentUser()
	if err != nil {
		return err
	}
	owner, control, dacl, err := securityOf(handle)
	if err != nil {
		return err
	}
	// A token's default owner may be a group (notably Administrators).
	if !owner.Equals(user) {
		return errors.New("private object owner is not the current user")
	}
	if root && control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("private root DACL is not protected")
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		ace, sid, err := aceAt(dacl, i)
		if err != nil {
			return err
		}
		// Recognize only ordinary allow ACEs; object, callback and deny
		// forms are refused rather than approximating access evaluation.
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("private object has an unsupported ACE")
		}
		if !sid.Equals(owner) {
			return errors.New("private object has a non-owner grant")
		}
	}
	return nil
}

// Untrusted principals may hold only these rights on a public directory.
const readOnlyDirectoryRights = windows.FILE_LIST_DIRECTORY | windows.FILE_READ_EA | windows.FILE_TRAVERSE |
	windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE |
	windows.GENERIC_READ | windows.GENERIC_EXECUTE

// fileAddSubdirectory lets a principal create a new subdirectory but not
// replace, rename or delete existing entries; like a sticky directory it is
// tolerated only on ancestors, never on an immediate parent.
const fileAddSubdirectory = 0x4

// CheckSafeDirectory applies the public ancestor policy to a directory: it
// must be an on-disk, non-reparse directory on a filesystem with persistent
// ACLs, owned by the current user, SYSTEM, Administrators or TrustedInstaller.
// Every other principal may hold only read, list and traverse rights; on a
// non-immediate ancestor it may also create subdirectories. Deny ACEs only
// remove access and are accepted; inherit-only ACEs do not apply to the
// directory itself. Any other ACE form fails closed.
func CheckSafeDirectory(file *os.File, immediate bool) error {
	return withHandle(file, func(h windows.Handle) error { return checkSafeDirectoryHandle(h, immediate) })
}

func checkSafeDirectoryHandle(handle windows.Handle, immediate bool) error {
	if err := checkObject(handle, true); err != nil {
		return err
	}
	if err := requirePersistentACLs(handle); err != nil {
		return err
	}
	owner, _, dacl, err := securityOf(handle)
	if err != nil {
		return err
	}
	return checkPublicSecurity(owner, dacl, immediate)
}

// checkPublicSecurity evaluates an already validated owner and DACL under the
// public ancestor policy.
func checkPublicSecurity(owner *windows.SID, dacl *windows.ACL, immediate bool) error {
	trusted, err := trustedPrincipals()
	if err != nil {
		return err
	}
	if !anyEqual(trusted, owner) {
		return errors.New("directory owner is not trusted")
	}
	allowed := uint32(readOnlyDirectoryRights)
	if !immediate {
		allowed |= fileAddSubdirectory
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		ace, sid, err := aceAt(dacl, i)
		if err != nil {
			return err
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		default:
			return errors.New("directory has an unsupported ACE")
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || anyEqual(trusted, sid) {
			continue
		}
		if uint32(ace.Mask)&^allowed != 0 {
			return fmt.Errorf("directory grants untrusted access %#x", uint32(ace.Mask))
		}
	}
	return nil
}

func requirePersistentACLs(handle windows.Handle) error {
	var flags uint32
	if err := volumeInformationByHandle(handle, nil, 0, nil, nil, &flags, nil, 0); err != nil {
		return fmt.Errorf("query filesystem: %w", err)
	}
	if flags&windows.FILE_PERSISTENT_ACLS == 0 {
		return errors.New("filesystem does not enforce persistent ACLs")
	}
	return nil
}

// requireNTFS refuses filesystems other than NTFS rather than assume their
// file identifiers provide the same stable physical identity.
func requireNTFS(handle windows.Handle) error {
	name := make([]uint16, windows.MAX_PATH+1)
	if err := volumeInformationByHandle(handle, nil, 0, nil, nil, nil, &name[0], uint32(len(name))); err != nil {
		return fmt.Errorf("query filesystem: %w", err)
	}
	if fs := windows.UTF16ToString(name); fs != "NTFS" {
		return fmt.Errorf("filesystem %q is not qualified for private state (NTFS required)", fs)
	}
	return nil
}

func currentUser() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("query current user: %w", err)
	}
	return user.User.Sid, nil
}

// trustedInstaller is NT SERVICE\TrustedInstaller, owner of OS directories.
const trustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

func trustedPrincipals() ([]*windows.SID, error) {
	user, err := currentUser()
	if err != nil {
		return nil, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	installer, err := windows.StringToSid(trustedInstaller)
	if err != nil {
		return nil, err
	}
	return []*windows.SID{user, system, admins, installer}, nil
}

func anyEqual(sids []*windows.SID, sid *windows.SID) bool {
	for _, s := range sids {
		if s.Equals(sid) {
			return true
		}
	}
	return false
}

// securityOf returns the owner, control bits and DACL of handle, refusing an
// invalid descriptor, missing owner or missing/null DACL.
func securityOf(handle windows.Handle) (*windows.SID, windows.SECURITY_DESCRIPTOR_CONTROL, *windows.ACL, error) {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("query security: %w", err)
	}
	if !sd.IsValid() {
		return nil, 0, nil, errors.New("invalid security descriptor")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return nil, 0, nil, fmt.Errorf("query owner: %w", err)
	}
	if owner == nil || !owner.IsValid() {
		return nil, 0, nil, errors.New("missing or invalid owner")
	}
	control, _, err := sd.Control()
	if err != nil {
		return nil, 0, nil, fmt.Errorf("query DACL control: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return nil, 0, nil, fmt.Errorf("query DACL: %w", err)
	}
	if control&windows.SE_DACL_PRESENT == 0 || dacl == nil {
		return nil, 0, nil, errors.New("missing or null DACL")
	}
	return owner, control, dacl, nil
}

// aceAt returns ACE i of dacl and its SID, refusing unknown flags and
// malformed sizes. Only allow/deny ACE layouts carry a SID at SidStart; other
// types are returned with a nil SID for the caller to refuse.
func aceAt(dacl *windows.ACL, i uint32) (*windows.ACCESS_ALLOWED_ACE, *windows.SID, error) {
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, i, &ace); err != nil {
		return nil, nil, fmt.Errorf("query ACE %d: %w", i, err)
	}
	if ace == nil || ace.Header.AceFlags&^uint8(windows.VALID_INHERIT_FLAGS) != 0 {
		return nil, nil, errors.New("unsupported ACE")
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE && ace.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE {
		return ace, nil, nil
	}
	// The SID begins at SidStart within the ACE; eight bytes is the SID
	// header, so a smaller ACE cannot hold one.
	if ace.Header.AceSize < uint16(unsafe.Offsetof(ace.SidStart))+8 {
		return nil, nil, errors.New("malformed ACE")
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !sid.IsValid() || int(unsafe.Offsetof(ace.SidStart))+sid.Len() > int(ace.Header.AceSize) {
		return nil, nil, errors.New("ACE has an invalid SID")
	}
	return ace, sid, nil
}
