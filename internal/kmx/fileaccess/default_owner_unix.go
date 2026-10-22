//go:build !windows

package fileaccess

// CheckDefaultPrivateOwner has nothing to check outside Windows: new objects
// belong to the creating process's effective user, and KMX's private modes
// are applied as before.
func CheckDefaultPrivateOwner() error { return nil }
