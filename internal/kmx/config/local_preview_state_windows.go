//go:build windows

package config

func localPreviewStateRoot() (string, error) {
	return PrepareStateDir()
}
