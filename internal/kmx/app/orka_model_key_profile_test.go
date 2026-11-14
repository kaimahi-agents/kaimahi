package app

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrkaModelKeyProfiles(t *testing.T) {
	for _, vendor := range []string{"openai", "anthropic"} {
		for _, arch := range []string{"amd64", "arm64"} {
			docs, err := modelDocuments("workspace", "gpt-4.1", modelConnectOptions().OperationID, arch, vendor)
			if err != nil {
				t.Fatal(err)
			}
			if len(docs) != 4 {
				t.Fatal("changed root count")
			}
			for _, doc := range docs {
				raw, _ := json.Marshal(doc)
				var normalized map[string]any
				json.Unmarshal(raw, &normalized)
				if _, err := modelDesired(normalized); err != nil {
					t.Fatal(err)
				}
			}
			spec := modelMap(docs[3]["spec"])
			wantType := "openai"
			if vendor == "anthropic" {
				wantType = "anthropic"
			}
			if spec["type"] != wantType || spec["baseURL"] != "http://workspace-vekil.orka-system.svc:1337/v1" {
				t.Fatal("incorrect native Provider", spec)
			}
			pod := modelMap(modelMap(modelMap(docs[1]["spec"])["template"])["spec"])
			container := modelMap(pod["containers"].([]any)[0])
			script := container["args"].([]any)[0].(string)
			if !strings.Contains(script, "/run/bridge/credentials/model-api-key") || strings.Contains(script, "copilot-github-token") || strings.Contains(script, "export COPILOT_GITHUB_TOKEN") {
				t.Fatal("wrong credential route")
			}
			p := modelVendorConfig(vendor, "gpt-4.1")["providers"].([]any)[0].(map[string]any)
			if p["api_key_env"] != "MODEL_API_KEY" || p["default"] != true || p["model_discovery"] != "openai" {
				t.Fatal("incorrect discovery", p)
			}
			if vendor == "openai" {
				if p["base_url"] != "https://api.openai.com/v1" || p["type"] != "openai-compatible" || p["models"] == nil {
					t.Fatal("missing OpenAI config", p)
				}
				other := modelVendorConfig(vendor, "other")["providers"].([]any)[0].(map[string]any)
				if other["models"] != nil {
					t.Fatal("unqualified Responses overlay")
				}
			} else if p["base_url"] != "https://api.anthropic.com" || p["type"] != "anthropic-compatible" || p["models_path"] != "/v1/models" || p["auth_header"] != "x-api-key" || p["extra_headers"].(map[string]string)["anthropic-version"] != "2023-06-01" {
				t.Fatal("incorrect Anthropic config", p)
			}
			// Execute the generated launcher, replacing only filesystem/tool endpoints.
			dir := t.TempDir()
			config := filepath.Join(dir, "providers.json")
			capture := filepath.Join(dir, "observed")
			tool := filepath.Join(dir, "busybox")
			fake := filepath.Join(dir, "vekil")
			if err := os.WriteFile(tool, []byte("#!/bin/sh\ncase \"$1\" in stat) echo '0:65532:640:1:regular file:12';; cat) printf fixture-only;; *) exit 7;; esac\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fake, []byte("#!/bin/sh\n[ \"$MODEL_API_KEY\" = fixture-only ] || exit 1\nprintf '%s' \"$*\" > \"$CAPTURE\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			script = strings.NewReplacer("/run/vekil/bin/busybox", tool, "/run/vekil/providers.json", config, "exec \"/vekil\"", "exec \""+fake+"\"").Replace(script)
			cmd := exec.Command("/bin/sh", "-c", script)
			cmd.Env = append(os.Environ(), "CAPTURE="+capture)
			if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
				t.Fatalf("launcher failed/leaked: %v", err)
			}
			raw, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "fixture-only") {
				t.Fatal("credential in config")
			}
			var cfg map[string]any
			if json.Unmarshal(raw, &cfg) != nil || cfg["providers"] == nil {
				t.Fatal("invalid provider JSON")
			}
			info, err := os.Stat(config)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("nonprivate config")
			}
			args, err := os.ReadFile(capture)
			if err != nil || !strings.Contains(string(args), "--providers-config "+config) || strings.Contains(string(args), "fixture-only") {
				t.Fatal("incorrect or credential-valued argv")
			}
		}
	}
}
