package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chartWithCRDs(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"agents.core.orka.ai", "tasks.core.orka.ai"} {
		body := "---\napiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: " + name + "\n---\n"
		if err := tw.WriteHeader(&tar.Header{Name: "orka/crds/" + name + ".yaml", Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestOrkaChartCRDsServerSideApply(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "install", true: "dry-run"}[dryRun], func(t *testing.T) {
			chart := chartWithCRDs(t)
			f := newOrkaFixture(t, chart)
			f.app.orkaInstallerDigest = digestOf(chart)
			if err := f.app.OrkaInstall(OrkaOptions{Provider: "-", DryRun: dryRun}); err != nil {
				t.Fatal(err)
			}
			want := "kubectl --context kind-kaimahi-p1 apply --server-side --field-manager=kmx"
			if dryRun {
				want += " --dry-run=server"
			}
			want += " -f -"
			var applies []string
			for _, line := range strings.Split(f.calls(t), "\n") {
				if strings.Contains(line, " apply --server-side ") {
					applies = append(applies, line)
				}
			}
			if len(applies) != 2 {
				t.Fatalf("want one apply per CRD, got %q", applies)
			}
			for _, call := range applies {
				if call != want {
					t.Errorf("apply argv = %q; want %q", call, want)
				}
			}
			if strings.Contains(f.calls(t), "--force") || strings.Contains(f.calls(t), " replace ") {
				t.Fatal("forced or replaced CRDs")
			}
		})
	}
}

func TestOrkaChartCRDConflictRefusesBeforeLaterWork(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "install", true: "dry-run"}[dryRun], func(t *testing.T) {
			chart := chartWithCRDs(t)
			f := newOrkaFixture(t, chart)
			f.app.orkaInstallerDigest = digestOf(chart)
			// Simulate a CRD created by another manager after the absence preflight.
			// Only SSA refuses: reverting to client-side apply would overwrite it.
			script := strings.Replace(fakeOrkaKubectl, "case \"$*\" in", `case "$*" in
  *"apply --server-side --field-manager=kmx"*)
    cat > "$KMX_TEST_STDIN"
    printf 'Apply failed with 1 conflict: conflict with "other-owner": .spec.versions\n' >&2
    exit 1 ;;`, 1)
			if err := os.WriteFile(filepath.Join(f.dir, "kubectl"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			err := f.app.OrkaInstall(OrkaOptions{Provider: "-", DryRun: dryRun})
			if err == nil {
				t.Fatal("conflicting CRD was accepted")
			}
			for _, want := range []string{"agents.core.orka.ai", "other-owner", "cluster down", "cluster up"} {
				if !strings.Contains(err.Error()+f.errOut.String(), want) {
					t.Errorf("missing refusal diagnostic %q: %v\n%s", want, err, f.errOut)
				}
			}
			calls := f.calls(t)
			if strings.Count(calls, " apply ") != 1 || strings.Contains(calls, "wait --for=condition=Established") || strings.Contains(calls, "helm install ") || strings.Contains(calls, "--force") || strings.Contains(calls, " replace ") {
				t.Fatalf("continued or forced after conflict:\n%s", calls)
			}
			if strings.Contains(f.applied(t), "tasks.core.orka.ai") {
				t.Fatal("submitted later CRD after conflict")
			}
		})
	}
}

func TestOrkaInstallRefusesOldChartWithoutApplyingCRDs(t *testing.T) {
	chart := orkaChart(t)
	f := newOrkaFixture(t, chart)
	f.app.orkaInstallerDigest = digestOf(chart)
	t.Setenv("KMX_TEST_HELM_LIST", `[{"name":"orka","chart":"orka-0.2.0","app_version":"v0.2.0","status":"deployed"}]`)
	if err := f.app.OrkaInstall(OrkaOptions{Provider: "-"}); err == nil {
		t.Fatal("old chart accepted")
	}
	if calls := f.calls(t); strings.Contains(calls, " apply ") || strings.Contains(calls, "helm install ") {
		t.Fatalf("old chart mutated:\n%s", calls)
	}
}
