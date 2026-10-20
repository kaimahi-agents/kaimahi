//go:build windows

package main

// This deliberately unresolved import stops Windows builds before Unix-only
// dependencies compile. Keep the path valid so other platforms can ignore it.
// Native support: https://github.com/kaimahi-agents/kaimahi/issues/578.
import "Windows-is-not-supported-yet/use-WSL/see-github.com/kaimahi-agents/kaimahi/issues/578"
