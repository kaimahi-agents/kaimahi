//go:build !linux && !darwin

package oras

import "errors"

func publishLayoutNoReplace(stage, destination string) error { return errors.ErrUnsupported }
