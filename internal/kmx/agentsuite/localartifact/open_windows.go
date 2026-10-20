//go:build windows

package localartifact

import "syscall"

const readFlags = syscall.FILE_FLAG_OPEN_REPARSE_POINT
