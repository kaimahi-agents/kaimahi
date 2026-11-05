package app

import (
	"context"
	"errors"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"path/filepath"
	"testing"
)

func TestOrkaModelVisibleAnchorResult(t *testing.T) {
	for _, failure := range []string{"anchor", "checkpoint"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
			ctx := context.Background()
			r, err := openModelRecord(ctx, modelTestTarget, true)
			if err != nil {
				t.Fatal(err)
			}
			defer r.close()
			publish := r.publish
			writes := 0
			r.publish = func(ctx context.Context, dir *fileaccess.Directory, name string, data []byte) (fileaccess.PublicationResult, error) {
				writes++
				if failure == "checkpoint" && name == "000000.json" {
					return fileaccess.PublicationResult{VisibilityKnown: true}, errors.New("checkpoint refused")
				}
				visible, err := publish(ctx, dir, name, data)
				if err == nil && failure == "anchor" && name == "connection.json" {
					return visible, errors.New("anchor visibility known, durability unknown")
				}
				return visible, err
			}
			state := modelTestCheckpoint()
			if err = r.checkpoint(ctx, state); err == nil || !r.anchored {
				t.Fatal("publication failure boundary not reached", err)
			}
			result := modelConnectionResult(r, "unknown")
			if result.Connection == nil || result.Connection.ID != state.Connection.ID || result.Connection.CredentialGeneration != state.Connection.CredentialGeneration || result.Connection.State != "unknown" || result.RemoteState != "unknown" {
				t.Fatalf("visible anchor identity lost: %+v", result)
			}
			before := writes
			if err = r.checkpoint(ctx, state); err == nil || writes != before {
				t.Fatal("result projection granted publication retry")
			}
		})
	}
}
