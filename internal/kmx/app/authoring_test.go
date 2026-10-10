package app

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// Generated secrets travel through the pipe into kubectl and nowhere else.
// The shell had to write them to 0600 files first, because `kubectl create
// secret --from-file` reads a path; this is the property that replaces that
// file, so it is worth asserting rather than commenting.
func TestSecretManifestCarriesValuesInTheDocumentOnly(t *testing.T) {
	body := string(secretManifest("kaimahi-governed-token", "orka-system",
		map[string]string{"api-key": "kmh_" + strings.Repeat("a", 64)},
		map[string]string{"kaimahi.dev/credential": "hello-world"}))

	if strings.Contains(body, "kmh_") {
		t.Errorf("the token appears in cleartext in the manifest:\n%s", body)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("kmh_" + strings.Repeat("a", 64)))
	if !strings.Contains(body, "api-key: "+encoded) {
		t.Errorf("the token is not carried as data:\n%s", body)
	}
	for _, want := range []string{
		"kind: Secret",
		"name: kaimahi-governed-token",
		"namespace: orka-system",
		`kaimahi.dev/credential: "hello-world"`,
		"type: Opaque",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("manifest lacks %q:\n%s", want, body)
		}
	}
}

// A random value is a random value: 32 bytes, hex, and never the same twice.
// Credentials and generated identities both rely on this shared helper.
func TestGeneratedSecretsAreRandomAndFullLength(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		v, err := randomHex(32)
		if err != nil {
			t.Fatal(err)
		}
		if len(v) != 64 {
			t.Fatalf("generated %d hex characters, want 64", len(v))
		}
		if _, err := hex.DecodeString(v); err != nil {
			t.Fatalf("generated value is not hex: %v", err)
		}
		if seen[v] {
			t.Fatal("the same value was generated twice")
		}
		seen[v] = true
	}
}

func TestApplySecretInPipesDocumentWithExplicitContextAndPreservesFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			script := `test "$*" = '--context kind-kaimahi-p1 -n agents apply -f -' || exit 2
IFS= read -r body
[ "$body" = 'secret payload only on stdin' ] || exit 3
`
			if failure {
				script += "exit 7"
			}
			a := appWithKubectl(t, script)
			a.Run.Echo = true
			err := a.applySecretIn("agents", []byte("secret payload only on stdin\n"), "provider-key")
			if failure {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 {
					t.Fatalf("apply failure lost subprocess cause: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := "kubectl --context kind-kaimahi-p1 -n agents apply -f - # (Secret provider-key)\n"
			if got := a.Err.(*bytes.Buffer).String(); got != want {
				t.Fatalf("apply diagnostic included payload or duplicate echo: %q", got)
			}
			if !a.Run.Echo {
				t.Fatal("applying Secret changed shared Runner echo behavior")
			}
		})
	}
}
