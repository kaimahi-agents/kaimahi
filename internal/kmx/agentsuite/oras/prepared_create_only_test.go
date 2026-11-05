package oras

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedPushCreateOnlyRaces(t *testing.T) {
	for _, scenario := range []string{"existing", "late OCI", "late empty"} {
		t.Run(scenario, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source")
			copyDirectory(t, filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), source)
			layout := filepath.Join(t.TempDir(), "layout")
			p, err := Push(t.Context(), source, layout, "original")
			if err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "destination")
			var before []byte
			var identity os.FileInfo
			appear := func() error {
				if scenario == "late empty" {
					if err := os.Mkdir(dest, 0700); err != nil {
						return err
					}
				} else {
					copyDirectory(t, layout, dest)
					before, err = os.ReadFile(filepath.Join(dest, "index.json"))
					if err != nil {
						return err
					}
				}
				identity, err = os.Stat(dest)
				return err
			}
			hook := appear
			if scenario == "existing" {
				if err := appear(); err != nil {
					t.Fatal(err)
				}
				hook = nil
			}
			_, err = pushLayoutWithMode(t.Context(), layout, dest, "compiled", p.Descriptor, true, hook)
			if !errors.Is(err, fs.ErrExist) {
				t.Fatalf("must refuse collision without changing peer: %v", err)
			}
			after, err := os.Stat(dest)
			if err != nil || !os.SameFile(identity, after) {
				t.Fatal("peer directory replaced", err)
			}
			if scenario == "late empty" {
				entries, err := os.ReadDir(dest)
				if err != nil || len(entries) != 0 {
					t.Fatal("peer empty directory changed", err)
				}
			} else {
				after, err := os.ReadFile(filepath.Join(dest, "index.json"))
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("peer OCI index overwritten", err)
				}
			}
		})
	}
}

func TestPreparedPushMergeModeUnchanged(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	copyDirectory(t, filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), source)
	layout := filepath.Join(t.TempDir(), "layout")
	p, err := Push(t.Context(), source, layout, "original")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "dest")
	if _, err := PushLayout(t.Context(), layout, dest, "first", p.Descriptor); err != nil {
		t.Fatal(err)
	}
	result, err := PushLayout(t.Context(), layout, dest, "second", p.Descriptor)
	if err != nil || !result.Updated {
		t.Fatal("legacy merge refused", err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "index.json"))
	if err != nil || !bytes.Contains(data, []byte("first")) || !bytes.Contains(data, []byte("second")) {
		t.Fatal("unrelated reference lost", err)
	}
}
