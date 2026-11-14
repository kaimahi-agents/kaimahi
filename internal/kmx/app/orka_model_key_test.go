package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the real record/lifecycle with synthetic Kubernetes and no key bytes.
func TestOrkaModelKeyLifecycle(t *testing.T) {
	for _, vendor := range []string{"openai", "anthropic"} {
		t.Run(vendor, func(t *testing.T) {
			a, objects, calls := modelLifecycleFixture(t)
			modelDeleteFixture(a, objects)
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if strings.Contains(strings.Join(cmd.Args, " "), "/v1/models") {
					fmt.Fprint(cmd.Stdout, `{"data":[{"id":"gpt-4.1"}]}`)
					return nil
				}
				return capture(ctx, cmd)
			}
			opt := modelConnectOptions()
			opt.Route, opt.Vendor = "key", vendor
			result, err := a.ConnectOrkaModel(opt)
			if err != nil {
				t.Fatalf("key connect: %v", err)
			}
			raw, _ := json.Marshal(result.Connection)
			var connection map[string]any
			json.Unmarshal(raw, &connection)
			if connection["vendor"] != vendor || connection["route"] != "key" || len(objects) != 4 {
				t.Fatalf("lost selection: %s", raw)
			}
			before := len(*calls)
			again, err := a.ConnectOrkaModel(opt)
			if err != nil || again.Connection.ID != result.Connection.ID {
				t.Fatalf("reuse: %+v %v", again, err)
			}
			for _, call := range (*calls)[before:] {
				if strings.Contains(call, " create ") || strings.Contains(call, " delete ") {
					t.Fatal("reuse mutated")
				}
			}
			opt.Vendor = map[string]string{"openai": "anthropic", "anthropic": "openai"}[vendor]
			if _, err := a.ConnectOrkaModel(opt); err == nil {
				t.Fatal("changed vendor reused connection")
			}
			// Removing the external credential must not block disconnect.
			stat := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "stat")
			original, err := os.ReadFile(stat)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stat, append([]byte("#!/bin/sh\ncase \"$*\" in *model-api-key*|*copilot-github-token*) exit 1;; esac\n"), original...), 0700); err != nil {
				t.Fatal(err)
			}
			result, err = a.DisconnectOrkaModel(modelDisconnectOptions(result.Connection.ID))
			if err != nil || result.Connection.State != "gateway_stopped" || len(objects) != 0 {
				t.Fatalf("disconnect: %+v %v", result, err)
			}
		})
	}
}
