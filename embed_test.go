package kaimahi

import (
	"io/fs"
	"testing"
)

func TestEmbeddedAssetsCanBeRead(t *testing.T) {
	for name, assets := range map[string]fs.FS{
		"Manifests": Manifests,
		"Managed":   Managed,
	} {
		t.Run(name, func(t *testing.T) {
			files := 0
			err := fs.WalkDir(assets, ".", func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.Type().IsRegular() {
					files++
					_, err = fs.ReadFile(assets, path)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if files == 0 {
				t.Fatal("embedded filesystem contains no files")
			}
		})
	}
}
