package workspace_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/opencontainers/go-digest"
)

// Capture the pre-policy identities so an optional field cannot rewrite legacy packages.
func TestPolicyFreeCompatibility(t *testing.T) {
	for _, profile := range []string{"orka", "kagent", "agentsessions", "multi"} {
		t.Run(profile, func(t *testing.T) {
			root, cache := createWorkspace(t)
			switch profile {
			case "kagent":
				setMemberRuntime(t, root, "runtime:\n  id: kagent\n  version: v0.10.3\n  engine: go\n")
			case "agentsessions":
				setMemberRuntime(t, root, "runtime:\n  id: agentsessions\n  version: "+workspace.AgentSessionsVersion+"\n  engine: chat\n")
			case "multi":
				root, cache = multiWorkspace(t, 2, 1)
			}
			p, err := workspace.Prepare(t.Context(), root, cache)
			if err != nil {
				t.Fatal(err)
			}
			out := extract(t, p)
			files := map[string]string{}
			err = filepath.WalkDir(out, func(name string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				rel, err := filepath.Rel(out, name)
				if err != nil {
					return err
				}
				files[filepath.ToSlash(rel)] = digest.FromBytes(readFile(t, name)).String()
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			// fmt sorts map keys; this commits every original packaged file's bytes/path.
			tree := digest.FromString(fmt.Sprint(files)).String()
			want := map[string][3]string{
				"orka":          {"sha256:3beea63073dc6f2cbb050bb616493bc8d71882af79eed33d45e260e6f60fbf57", "sha256:885cac444a5cdf7933fb16765636835dd53eab276a7a0c30d4dc3b0d000be999", "sha256:1fa7738a53ac94f5311274730b2e1d53b77e2c81c84d9bbc67959518ac7b3595"},
				"kagent":        {"sha256:a472897054016d285606a230245eb6f93bd173e692e6f2b58ad801bc0e876efe", "sha256:429fd4caf1a27b21665e88616cb40a97b507d9b899946e236fd6e0654dd0a7da", "sha256:8503f4966f3db5c77ba2d6726dc72fc3e7ec501bbda7c8da01a1dc84882fe946"},
				"agentsessions": {"sha256:9fade07800b026d22893bcf09b44004f4ae99911cbb4af42e76881a50f96860d", "sha256:a17fe1db7b17c6e4559ed326bf8695143c4a49510173fc5c4a763012d99db32d", "sha256:72aaa5fdabb8a2982baa586049b0656ee485234e84fe5d01c3365beee5463c68"},
				"multi":         {"sha256:90f5531fef141f54907e7cbbe08a61be2d708937e6ec4fc4d821990a8b398342", "sha256:c79fecf5b43c3660a56882879bebe974f0224331c76520f57b265c4c2e61d5f6", "sha256:67b38c8bdb4bf54e5bc4c8e150774ecb4a17ec087e013718d121e2849b649df3"},
			}[profile]
			if got := [3]string{p.Fingerprint, p.Descriptor.Digest.String(), tree}; got != want {
				t.Fatalf("policy-free identity changed:\n got %v\nwant %v", got, want)
			}
		})
	}
}
