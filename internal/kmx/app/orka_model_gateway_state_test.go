package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrkaModelGatewayCheckpoint(t *testing.T) {
	child := modelChild{"v1", "Pod", "gateway.pod", "pod-uid", "rs-uid"}
	for _, damage := range []string{"", "kind", "owner", "complete", "bound", "digest"} {
		t.Run(damage, func(t *testing.T) {
			cp := modelTestCheckpoint()
			cp.RuntimeDigest = strings.Repeat("a", 64)
			cp.GatewayChildren = []modelChild{child}
			switch damage {
			case "kind":
				cp.GatewayChildren[0].Kind = "Secret"
			case "owner":
				cp.GatewayChildren[0].OwnerUID = ""
			case "complete":
				cp.Complete = true
			case "bound":
				cp.GatewayChildren = make([]modelChild, 129)
			case "digest":
				cp.RuntimeDigest = "not-a-digest"
			}
			err := validateModelCheckpoint(cp)
			if (err == nil) != (damage == "") {
				t.Fatalf("validation %s: %v", damage, err)
			}
		})
	}
	t.Run("old absent fields", func(t *testing.T) {
		old := modelTestCheckpoint()
		raw, err := json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}
		var got modelCheckpoint
		if err = modelJSON(raw, &got); err != nil || validateModelCheckpoint(got) != nil || got.RuntimeDigest != "" || len(got.GatewayChildren) != 0 {
			t.Fatalf("old checkpoint: %+v %v", got, err)
		}
	})
	t.Run("runtime immutable", func(t *testing.T) {
		t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "home"))
		r, err := openModelRecord(context.Background(), modelTestTarget, true)
		if err != nil {
			t.Fatal(err)
		}
		defer r.close()
		cp := modelTestCheckpoint()
		cp.RuntimeDigest = strings.Repeat("a", 64)
		if err = r.checkpoint(context.Background(), cp); err != nil {
			t.Fatal(err)
		}
		cp.RuntimeDigest = strings.Repeat("b", 64)
		if err = r.checkpoint(context.Background(), cp); err == nil {
			t.Fatal("changed runtime digest")
		}
	})
}
