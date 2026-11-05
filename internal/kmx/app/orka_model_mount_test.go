package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestOrkaModelMountOptions(t *testing.T) {
	a := New(&config.Config{})
	for _, opt := range []UpOptions{{CredentialMount: "unknown"}, {CredentialMount: "copilot-v1", Step: "model"}} {
		if err := a.UpWithOptions(opt); err == nil || !strings.Contains(err.Error(), "credential mount") {
			t.Fatalf("unsafe options: %v", err)
		}
	}
}

func TestOrkaModelMount(t *testing.T) {
	t.Run("default bytes unchanged", func(t *testing.T) {
		b, err := modelKindConfig("")
		if err != nil {
			t.Fatal(err)
		}
		want := "apiVersion: kind.x-k8s.io/v1alpha4\nkind: Cluster\nkubeadmConfigPatches:\n- |\n  apiVersion: kubelet.config.k8s.io/v1beta1\n  kind: KubeletConfiguration\n  syncFrequency: 5s\n"
		if string(b) != want {
			t.Fatalf("default config changed: %q", b)
		}
	})
	t.Run("closed fixed profile", func(t *testing.T) {
		b, err := modelKindConfig("copilot-v1")
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{"extraMounts:", "hostPath: /run/bridge/credentials", "containerPath: /run/bridge/credentials", "readOnly: true", "propagation: None", "syncFrequency: 5s"} {
			if !strings.Contains(string(b), s) {
				t.Errorf("missing %s", s)
			}
		}
		for _, profile := range []string{"/tmp/credentials", "copilot-v1 ", "auto"} {
			if _, err := modelKindConfig(profile); err == nil {
				t.Errorf("accepted %q", profile)
			}
		}
	})
	t.Run("all node configurations fail closed", func(t *testing.T) {
		row := map[string]any{"Mounts": []any{map[string]any{"Type": "bind", "Source": modelCredentialDir, "Destination": modelCredentialDir, "RW": false, "Propagation": "rprivate"}}}
		check := func(row map[string]any) error { b, _ := json.Marshal([]any{row}); return modelNodeMount(b) }
		if err := check(row); err != nil {
			t.Fatal(err)
		}
		mount := row["Mounts"].([]any)[0].(map[string]any)
		for _, field := range []string{"Type", "Source", "Destination", "RW", "Propagation"} {
			original := mount[field]
			mount[field] = "wrong"
			if err := check(row); err == nil {
				t.Errorf("accepted changed %s", field)
			}
			mount[field] = original
		}
		delete(mount, "RW")
		if err := check(row); err == nil {
			t.Fatal("accepted missing RW observation")
		}
		row["Mounts"] = []any{}
		if err := check(row); err == nil {
			t.Fatal("accepted missing mount")
		}
		for _, raw := range []string{"null", "[]", "[{\"Mounts\":[]},{\"Mounts\":[]}]", "{}"} {
			if err := modelNodeMount([]byte(raw)); err == nil {
				t.Errorf("accepted %s", raw)
			}
		}
	})
}
