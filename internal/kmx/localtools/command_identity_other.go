//go:build !linux && !darwin

package localtools

import "os"

func commandFileIdentity(os.FileInfo) (string, bool) { return "", false }
