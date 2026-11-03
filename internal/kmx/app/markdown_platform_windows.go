package app

import (
	"errors"
	"os"
	"os/exec"
)

// Native Windows builds retain canonical parser/MCP support. Markdown instance
// IO and subprocess execution require the qualified Unix host primitives.
var errMarkdownPlatform = errors.New("Markdown agent instances require Linux or macOS; use WSL on Windows")

func mdOpenRead(string) (*os.File, error)           { return nil, errMarkdownPlatform }
func mdOwned(os.FileInfo, bool) bool                { return false }
func openAgentLease(string, bool) (*os.File, error) { return nil, errMarkdownPlatform }
func mdLock(*os.File) error                         { return errMarkdownPlatform }
func mdUnlock(*os.File) error                       { return errMarkdownPlatform }
func mdLockBusy(error) bool                         { return false }
func mdDetachProcess(*exec.Cmd) error               { return errMarkdownPlatform }
func configureCLIProcess(cmd *exec.Cmd)             { cmd.Err = errMarkdownPlatform }
