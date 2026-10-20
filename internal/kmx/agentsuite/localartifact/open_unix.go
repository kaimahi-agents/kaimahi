//go:build unix

package localartifact

import "syscall"

const readFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
