//go:build !windows

package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

func TestRetainedStoreParentDurability(t *testing.T) {
	for _, stop := range []string{"", "retained-packages", "state"} {
		t.Run(stop, func(t *testing.T) {
			retainedHome(t)
			var names []string
			canary := errors.New("parent flush failed")
			s, err := openRetainedStoreWithSync(t.Context(), true, func(ctx context.Context, d *fileaccess.Directory) error {
				info, err := d.Stat()
				if err != nil {
					return err
				}
				name := info.Name()
				if name != "v1" && name != "retained-packages" {
					name = "state"
				}
				names = append(names, name)
				if name == stop {
					return canary
				}
				return fileaccess.SyncDirectory(ctx, d)
			})
			if s != nil {
				_ = s.Close()
			}
			if stop != "" {
				if !errors.Is(err, canary) || s != nil {
					t.Fatal("parent persistence failure did not veto store", names, err)
				}
				return
			}
			if err != nil || !slices.Equal(names, []string{"v1", "retained-packages", "state"}) {
				t.Fatal("hierarchy not flushed deepest-first", names, err)
			}
			names = nil
			s, err = openRetainedStoreWithSync(t.Context(), false, func(context.Context, *fileaccess.Directory) error {
				t.Error("read-only store flushed state")
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			_ = s.Close()
		})
	}
}
