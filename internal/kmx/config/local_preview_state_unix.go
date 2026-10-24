//go:build !windows

package config

// Unix preserves the existing state-root compatibility boundary: an
// owner-controlled, non-writable-to-others settings directory may be a safe
// ancestor, while the managed local-preview children are private.
func localPreviewStateRoot() (string, error) {
	return StateDir()
}
