package app

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrkaModelAdmissionDefaults(t *testing.T) {
	docs, _ := modelDocuments("copilot", "gpt-4.1", "11111111-1111-4111-8111-111111111111", "amd64")
	raw, _ := json.Marshal(docs[1]["spec"])
	var want, got map[string]any
	json.Unmarshal(raw, &want)
	json.Unmarshal(raw, &got)
	modelMap(modelMap(got["template"])["metadata"])["creationTimestamp"] = nil
	if !modelSpecMatches(want, got, "", "Deployment") {
		t.Fatal("rejected standard template creationTimestamp null")
	}
	modelMap(modelMap(got["template"])["spec"])["hostNetwork"] = true
	if modelSpecMatches(want, got, "", "Deployment") {
		t.Fatal("accepted injected host network")
	}
}

func TestOrkaModelProfile(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		docs, err := modelDocuments("copilot", "gpt-4.1", "11111111-1111-4111-8111-111111111111", arch)
		if err != nil {
			t.Fatal(err)
		}
		if len(docs) != 4 {
			t.Fatal("wrong connection root count")
		}
		for _, doc := range docs {
			b, _ := json.Marshal(doc)
			var normalized map[string]any
			json.Unmarshal(b, &normalized)
			if _, err := modelDesired(normalized); err != nil {
				t.Fatal(err)
			}
		}
		secret := docs[0]
		if secret["data"].(map[string]any)["api-key"] != "bm90LXVzZWQtYnktdGhpcy1lbmRwb2ludA==" {
			t.Fatal("not fixed placeholder")
		}
		pod := modelMap(modelMap(modelMap(docs[1]["spec"])["template"])["spec"])
		if pod["serviceAccountName"] != "default" || pod["automountServiceAccountToken"] != false {
			t.Fatal("gateway account changed")
		}
		probe := modelMap(modelMap(pod["containers"].([]any)[0])["readinessProbe"])
		if modelMap(probe["tcpSocket"])["port"] != 1337 || probe["httpGet"] != nil {
			t.Fatal("listener readiness must not retry authentication")
		}
		root, _ := json.Marshal(pod)
		for _, s := range []string{"\"medium\":\"Memory\"", "\"readOnly\":true", "\"runAsUser\":65532", "\"runAsGroup\":65532", "\"readOnlyRootFilesystem\":true", "/run/vekil/bin/busybox", "/run/bridge/credentials"} {
			if !strings.Contains(string(root), s) {
				t.Errorf("missing boundary %s", s)
			}
		}
		if strings.Contains(string(root), "latest") || strings.Contains(string(root), "ghu_canary") {
			t.Fatal("unpinned or credential-valued deployment")
		}
	}
	if _, err := modelDocuments("copilot", "gpt-4.1", "11111111-1111-4111-8111-111111111111", "windows"); err == nil {
		t.Fatal("unsupported architecture")
	}
}

func TestOrkaModelLauncher(t *testing.T) {
	// The filesystem/tool/exec boundary is synthetic; no provider binary runs.
	for _, data := range []string{"", " ", "\n\t", "ghu_fixture\n", "ghu_one\nghu_two", "ghu_\x00fixture", "ghu_fixture\r", "ghu_\x7ffixture", "ghu_é", strings.Repeat("x", 16385), "ghu_fixture"} {
		t.Run(fmt.Sprintf("bytes-%d-%x", len(data), []byte(data)[:min(4, len(data))]), func(t *testing.T) {
			dir := t.TempDir()
			token := filepath.Join(dir, "credential")
			if err := os.WriteFile(token, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			tool := filepath.Join(dir, "tool")
			capture := filepath.Join(dir, "observed")
			fake := filepath.Join(dir, "vekil")
			toolScript := `#!/bin/sh
case "$1" in
stat) printf '0:65532:640:1:regular file:%s\n' "$(wc -c < "$TOKEN_TEST_FILE" | tr -d ' ')";;
cat) shift; exec /bin/cat "$@";;
*) exit 7;; esac
`
			if err := os.WriteFile(tool, []byte(toolScript), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s' \"$COPILOT_GITHUB_TOKEN\" > \"$TOKEN_TEST_CAPTURE\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", "-c", modelLauncherScript(token, tool, fake))
			cmd.Env = append(os.Environ(), "TOKEN_TEST_FILE="+token, "TOKEN_TEST_CAPTURE="+capture)
			output, err := cmd.CombinedOutput()
			if len(output) != 0 {
				t.Fatal("launcher leaked output")
			}
			got, readErr := os.ReadFile(capture)
			if data == "ghu_fixture" {
				if err != nil || readErr != nil || string(got) != data {
					t.Fatalf("valid credential refused: %v", err)
				}
			} else {
				if err == nil || !os.IsNotExist(readErr) {
					t.Fatal("started Vekil with missing/whitespace/multiline/oversize input")
				}
			}
		})
	}
}
