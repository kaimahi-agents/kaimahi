package main

import (
	"errors"
	"os"
	"path/filepath"
)

func openGitHubSummary() (*os.File, error) {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("GitHub Actions summary is unavailable")
	}
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0600)
		if createErr != nil {
			return nil, errors.New("GitHub Actions summary is unavailable")
		}
		opened, statErr := file.Stat()
		if statErr != nil || !opened.Mode().IsRegular() {
			_ = file.Close()
			return nil, errors.New("GitHub Actions summary is unavailable")
		}
		return file, nil
	}
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("GitHub Actions summary is unavailable")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return nil, errors.New("GitHub Actions summary is unavailable")
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, errors.New("GitHub Actions summary is unavailable")
	}
	return file, nil
}
