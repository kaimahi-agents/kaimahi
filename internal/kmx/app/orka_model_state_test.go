package app

import (
	"context"
	"errors"
	"os"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"path/filepath"
	"strings"
	"testing"
)

const modelTestTarget = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func modelTestCheckpoint() modelCheckpoint {
	return modelCheckpoint{Version: 1, ClusterUID: "cluster-uid", NamespaceUID: "namespace-uid", Connection: OrkaModelConnection{ID: "11111111-1111-4111-8111-111111111111", Route: "copilot", Provider: "copilot", Model: "gpt-4.1", CredentialGeneration: "22222222-2222-4222-8222-222222222222", State: "unknown"}}
}

// Mutation caught: creating state in read-only lookup, skipping the process lock,
// replacing immutable publications, or accepting a changed target/config.
func TestOrkaModelState(t *testing.T) {
	t.Run("lookup_does_not_create", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "absent")
		t.Setenv("KMX_HOME", home)
		r, err := openModelRecord(context.Background(), modelTestTarget, false)
		if err == nil {
			r.close()
			t.Fatal("lookup unexpectedly created/opened absent state")
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatalf("lookup changed state: %v", err)
		}
	})
	t.Run("private_roundtrip_and_lock", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "state")
		t.Setenv("KMX_HOME", home)
		ctx := context.Background()
		r, err := openModelRecord(ctx, modelTestTarget, true)
		if err != nil {
			t.Fatal(err)
		}
		if other, err := openModelRecord(ctx, modelTestTarget, true); err == nil {
			other.close()
			t.Fatal("second holder acquired target lock")
		}
		cp := modelTestCheckpoint()
		if err := r.checkpoint(ctx, cp); err != nil {
			t.Fatal(err)
		}
		cp.Connection.State = "model_ready"
		if err := r.checkpoint(ctx, cp); err != nil {
			t.Fatal(err)
		}
		if err := r.close(); err != nil {
			t.Fatal(err)
		}
		r, err = openModelRecord(ctx, modelTestTarget, false)
		if err != nil {
			t.Fatal(err)
		}
		defer r.close()
		if r.state.Connection.ID != "11111111-1111-4111-8111-111111111111" || r.state.Connection.State != "model_ready" {
			t.Fatalf("lost retained observation: %+v", r.state.Connection)
		}
		base := filepath.Join(home, "orka-models", modelTestTarget)
		for _, p := range []string{base, filepath.Join(base, "connection.json"), filepath.Join(base, "11111111-1111-4111-8111-111111111111", "000000.json"), filepath.Join(base, "11111111-1111-4111-8111-111111111111", "000001.json")} {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0077 != 0 {
				t.Errorf("state exposed: %s %v", p, info.Mode())
			}
		}
		cp.NamespaceUID = "foreign"
		if err := r.checkpoint(ctx, cp); err == nil {
			t.Fatal("namespace identity changed in retained family")
		}
	})
	t.Run("invalid_target_and_cancelled_create", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "absent")
		t.Setenv("KMX_HOME", home)
		for _, target := range []string{"../foreign", strings.Repeat("a", 63), strings.Repeat("Z", 64)} {
			if r, err := openModelRecord(context.Background(), target, true); err == nil {
				r.close()
				t.Fatal("unsafe target accepted")
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if r, err := openModelRecord(ctx, modelTestTarget, true); err == nil {
			r.close()
			t.Fatal("cancelled create accepted")
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatal("invalid/cancelled request created state")
		}
	})
	t.Run("unknown_checkpoint_field_not_republished", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "state")
		t.Setenv("KMX_HOME", home)
		ctx := context.Background()
		r, err := openModelRecord(ctx, modelTestTarget, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.checkpoint(ctx, modelTestCheckpoint()); err != nil {
			t.Fatal(err)
		}
		r.close()
		p := filepath.Join(home, "orka-models", modelTestTarget, "11111111-1111-4111-8111-111111111111", "000001.json")
		bad := []byte(`{"version":1,"SYNTHETIC_PRIVATE_INPUT":"token"}`)
		if err := os.WriteFile(p, bad, 0600); err != nil {
			t.Fatal(err)
		}
		if r, err := openModelRecord(ctx, modelTestTarget, false); err == nil {
			r.close()
			t.Fatal("unknown private field accepted")
		} else if strings.Contains(err.Error(), "SYNTHETIC_PRIVATE_INPUT") {
			t.Fatal("raw state leaked in error")
		}
		got, err := os.ReadFile(p)
		if err != nil || string(got) != string(bad) {
			t.Fatal("invalid private state repaired or removed")
		}
	})
	t.Run("complete_anchor_explicit_local_finish", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "state")
		t.Setenv("KMX_HOME", home)
		ctx := context.Background()
		r, err := openModelRecord(ctx, modelTestTarget, true)
		if err != nil {
			t.Fatal(err)
		}
		cp := modelTestCheckpoint()
		if err := r.checkpoint(ctx, cp); err != nil {
			t.Fatal(err)
		}
		cp.Complete = true
		if err := r.checkpoint(ctx, cp); err != nil {
			t.Fatal(err)
		}
		r.close()
		// Simulate process loss after complete checkpoint and before unlink. A new
		// explicit disconnect may finish only this local step, never a remote retry.
		r, err = openModelRecord(ctx, modelTestTarget, false)
		if err != nil {
			t.Fatal(err)
		}
		if !r.state.Complete {
			t.Fatal("complete family became active")
		}
		if err := r.finish(ctx); err != nil {
			t.Fatal(err)
		}
		r.close()
		if _, err := os.Stat(filepath.Join(home, "orka-models", modelTestTarget, "connection.json")); !os.IsNotExist(err) {
			t.Fatal("completed anchor still present")
		}
		if _, err := os.Stat(filepath.Join(home, "orka-models", modelTestTarget, cp.Connection.ID, "000001.json")); err != nil {
			t.Fatal("diagnostic family removed")
		}
	})
	t.Run("visible_publication_error_blocks_next_effect", func(t *testing.T) {
		t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
		ctx := context.Background()
		r, err := openModelRecord(ctx, modelTestTarget, true)
		if err != nil {
			t.Fatal(err)
		}
		defer r.close()
		writes := 0
		r.publish = func(ctx context.Context, dir *fileaccess.Directory, name string, data []byte) (fileaccess.PublicationResult, error) {
			writes++
			observed, err := fileaccess.PublishPrivate(ctx, dir, name, data)
			if err != nil {
				return observed, err
			}
			return observed, errors.New("synthetic post-visibility failure")
		}
		if err := r.checkpoint(ctx, modelTestCheckpoint()); err == nil {
			t.Fatal("unconfirmed publication accepted")
		}
		if !r.anchored {
			t.Fatal("lost positively observed anchor visibility")
		}
		if err := r.checkpoint(ctx, modelTestCheckpoint()); err == nil {
			t.Fatal("continued after ambiguous persistence")
		}
		if writes != 1 {
			t.Fatalf("publication replayed %d times", writes)
		}
		if err := r.finish(ctx); err == nil {
			t.Fatal("ambiguous publication authorized removal")
		}
	})
	t.Run("checkpoint_gap_and_symlink_refused", func(t *testing.T) {
		for _, linked := range []bool{false, true} {
			t.Run(map[bool]string{false: "gap", true: "link"}[linked], func(t *testing.T) {
				home := filepath.Join(t.TempDir(), "state")
				t.Setenv("KMX_HOME", home)
				ctx := context.Background()
				r, err := openModelRecord(ctx, modelTestTarget, true)
				if err != nil {
					t.Fatal(err)
				}
				if err := r.checkpoint(ctx, modelTestCheckpoint()); err != nil {
					t.Fatal(err)
				}
				r.close()
				family := filepath.Join(home, "orka-models", modelTestTarget, "11111111-1111-4111-8111-111111111111")
				if linked {
					if err := os.Symlink(filepath.Join(family, "000000.json"), filepath.Join(family, "000001.json")); err != nil {
						t.Skipf("symlink unavailable: %v", err)
					}
				} else if err := os.WriteFile(filepath.Join(family, "000002.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
				if r, err := openModelRecord(ctx, modelTestTarget, false); err == nil {
					r.close()
					t.Fatal("incomplete/linked family accepted")
				}
			})
		}
	})
	t.Run("unsafe_existing_container_not_repaired", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "state")
		t.Setenv("KMX_HOME", home)
		ctx := context.Background()
		r, err := openModelRecord(ctx, modelTestTarget, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.checkpoint(ctx, modelTestCheckpoint()); err != nil {
			t.Fatal(err)
		}
		r.close()
		base := filepath.Join(home, "orka-models")
		if err := os.Chmod(base, 0755); err != nil {
			t.Fatal(err)
		}
		if r, err := openModelRecord(ctx, modelTestTarget, false); err == nil {
			r.close()
			t.Fatal("unsafe existing container accepted")
		}
		info, err := os.Stat(base)
		if err != nil || info.Mode().Perm() != 0755 {
			t.Fatal("unsafe container repaired")
		}
	})
	t.Run("inconsistent_create_marker_and_scope_refused", func(t *testing.T) {
		for _, wrongScope := range []bool{false, true} {
			t.Run(map[bool]string{false: "marker", true: "purpose"}[wrongScope], func(t *testing.T) {
				t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
				ctx := context.Background()
				r, err := openModelRecord(ctx, modelTestTarget, true)
				if err != nil {
					t.Fatal(err)
				}
				defer r.close()
				cp := modelTestCheckpoint()
				root := modelOwnedResource{APIVersion: "core.orka.ai/v1alpha1", Kind: "Task", Name: "proof", Marker: cp.Connection.ID, Purpose: "proof"}
				opID := cp.Connection.CredentialGeneration
				if wrongScope {
					opID = root.Marker
					root.Purpose = "connection"
				}
				cp.Pending = &modelPendingAction{OperationID: opID, Verb: "create", Resource: root}
				if err := r.checkpoint(ctx, cp); err == nil {
					t.Fatal("inconsistent operation ownership published")
				}
			})
		}
	})
	t.Run("credential_shaped_state_not_published", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "state")
		t.Setenv("KMX_HOME", home)
		ctx := context.Background()
		r, err := openModelRecord(ctx, modelTestTarget, true)
		if err != nil {
			t.Fatal(err)
		}
		defer r.close()
		cp := modelTestCheckpoint()
		cp.Connection.Model = "ghp_" + strings.Repeat("b", 36)
		if err := r.checkpoint(ctx, cp); err == nil {
			t.Fatal("credential-shaped identifier persisted")
		} else if strings.Contains(err.Error(), cp.Connection.Model) {
			t.Fatal("credential reflected by error")
		}
		if _, err := os.Stat(filepath.Join(home, "orka-models", modelTestTarget, "connection.json")); !os.IsNotExist(err) {
			t.Fatal("credential request published anchor")
		}
	})
	t.Run("duplicate_checkpoint_keys_refused", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "state")
		t.Setenv("KMX_HOME", home)
		ctx := context.Background()
		r, err := openModelRecord(ctx, modelTestTarget, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.checkpoint(ctx, modelTestCheckpoint()); err != nil {
			t.Fatal(err)
		}
		r.close()
		p := filepath.Join(home, "orka-models", modelTestTarget, "11111111-1111-4111-8111-111111111111", "000000.json")
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		ambiguous := strings.Replace(string(data), `"version":1,`, `"version":1,"version":1,`, 1)
		if err := os.WriteFile(p, []byte(ambiguous), 0600); err != nil {
			t.Fatal(err)
		}
		if r, err := openModelRecord(ctx, modelTestTarget, false); err == nil {
			r.close()
			t.Fatal("ambiguous checkpoint accepted")
		}
	})
	t.Run("replaced_anchor_not_removed", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "state")
		t.Setenv("KMX_HOME", home)
		ctx := context.Background()
		r, err := openModelRecord(ctx, modelTestTarget, true)
		if err != nil {
			t.Fatal(err)
		}
		defer r.close()
		cp := modelTestCheckpoint()
		if err := r.checkpoint(ctx, cp); err != nil {
			t.Fatal(err)
		}
		cp.Complete = true
		if err := r.checkpoint(ctx, cp); err != nil {
			t.Fatal(err)
		}
		anchor := filepath.Join(home, "orka-models", modelTestTarget, "connection.json")
		data, err := os.ReadFile(anchor)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(anchor, anchor+".retained"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(anchor, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := r.finish(ctx); err == nil {
			t.Fatal("replacement anchor removed by name")
		}
		got, err := os.ReadFile(anchor)
		if err != nil || string(got) != string(data) {
			t.Fatal("foreign replacement changed")
		}
	})
	t.Run("pending_effect_prevents_complete", func(t *testing.T) {
		t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
		ctx := context.Background()
		r, err := openModelRecord(ctx, modelTestTarget, true)
		if err != nil {
			t.Fatal(err)
		}
		defer r.close()
		cp := modelTestCheckpoint()
		cp.Pending = &modelPendingAction{OperationID: cp.Connection.ID, Verb: "create", Resource: modelOwnedResource{APIVersion: "core.orka.ai/v1alpha1", Kind: "Task", Name: "proof", Marker: cp.Connection.ID, Purpose: "proof"}}
		if err := r.checkpoint(ctx, cp); err != nil {
			t.Fatal(err)
		}
		cp.Complete = true
		if err := r.checkpoint(ctx, cp); err == nil {
			t.Fatal("unacknowledged proof became complete")
		}
		if err := r.finish(ctx); err == nil {
			t.Fatal("unfinished proof dropped its anchor")
		}
	})
}
