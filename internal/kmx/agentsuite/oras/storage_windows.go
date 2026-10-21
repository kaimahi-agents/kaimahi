//go:build windows

package oras

import (
	"errors"
	"fmt"

	"oras.land/oras-go/v2/content/oci"
)

func newOCIStore(path string) (*oci.Store, error) {
	return nil, fmt.Errorf("open OCI store: native Windows not supported yet: %w", errors.ErrUnsupported)
}
