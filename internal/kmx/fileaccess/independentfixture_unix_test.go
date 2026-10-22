//go:build !windows

package fileaccess

import "os"

func fixtureMkdir(path string, mode os.FileMode) error { return os.Mkdir(path, mode) }
func faWriteFile(path string, data []byte, mode os.FileMode) error {
	return os.WriteFile(path, data, mode)
}
