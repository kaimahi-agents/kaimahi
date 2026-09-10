package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Catches CLI success on refusal, partial stdout, ignored positional arguments,
// and a converter that emits no deployable resources.
func TestRun(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("apiVersion: v1\nkind: Secret\nmetadata: {name: forbidden, namespace: demo}\ndata: {token: do-not-print}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		args       []string
		status     int
		diagnostic string
	}{
		{"success", []string{"../../testdata/literal.yaml", "../../testdata/schemas.json"}, 0, ""},
		{"refusal", []string{bad, "../../testdata/schemas.json"}, 1, "Secret"},
		{"missing file", []string{"absent.yaml", "../../testdata/schemas.json"}, 1, "read input"},
		{"missing schema", []string{"../../testdata/literal.yaml", "absent.json"}, 1, "read schemas"},
		{"no args", nil, 1, "usage:"},
		{"extra arg", []string{"a", "b", "c"}, 1, "usage:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			status := run(tc.args, &stdout, &stderr)
			if status != tc.status {
				t.Fatalf("status=%d want=%d stderr=%s", status, tc.status, stderr.String())
			}
			if tc.status == 0 {
				if stderr.Len() != 0 || !strings.Contains(stdout.String(), "kind: Provider") || !strings.Contains(stdout.String(), "kind: Agent") {
					t.Fatalf("stdout=%s stderr=%s", stdout.String(), stderr.String())
				}
			} else if stdout.Len() != 0 || !strings.Contains(stderr.String(), tc.diagnostic) {
				t.Fatalf("stdout=%s stderr=%s", stdout.String(), stderr.String())
			}
			if strings.Contains(stderr.String(), "do-not-print") {
				t.Fatal("secret leaked")
			}
		})
	}
}
