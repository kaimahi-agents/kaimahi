//go:build !windows

package oras

import "oras.land/oras-go/v2/content/oci"

func newOCIStore(path string) (*oci.Store, error) {
	return oci.New(path)
}
