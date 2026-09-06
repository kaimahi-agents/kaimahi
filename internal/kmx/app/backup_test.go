package app

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// A fake kubectl that stands in for the Postgres pod. Every argument list is
// recorded, so the assertions below are about what kmx WOULD have run — the
// scale-down, the wait, the exec — not only about what it returned.
const fakePostgresKubectl = `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_ARGS"
case "$*" in
  *"config view"*)
    cat <<'JSON'
{"clusters":[{"name":"kind-kaimahi-p1","cluster":{"server":"https://127.0.0.1:6443"}}],
 "contexts":[{"name":"kind-kaimahi-p1","context":{"cluster":"kind-kaimahi-p1"}}]}
JSON
    exit 0 ;;
  *pg_dump*) printf '%s' "$KMX_TEST_DUMP"; exit "${KMX_TEST_DUMP_STATUS:-0}" ;;
  *"SELECT count(*) FROM ledger_entry"*) printf '%s\n' "${KMX_TEST_ROWS:-7}"; exit 0 ;;
  *psql*) cat > /dev/null; exit "${KMX_TEST_PSQL_STATUS:-0}" ;;
  *"get deploy kaimahi-proxy"*) printf '%s' "${KMX_TEST_REPLICAS:-2}"; exit 0 ;;
  *"get pods -l app=kaimahi-proxy"*) printf '%s' "$KMX_TEST_PODS"; exit 0 ;;
esac
exit 0
`

type pgFixture struct {
	app     *App
	out     *bytes.Buffer
	errOut  *bytes.Buffer
	argsLog string
	dir     string
}

func newPgFixture(t *testing.T) *pgFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake kubectl is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakePostgresKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &pgFixture{
		out: &bytes.Buffer{}, errOut: &bytes.Buffer{},
		argsLog: filepath.Join(dir, "args"), dir: dir,
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TEST_ARGS", f.argsLog)
	// The guard must not read a real kubeconfig or ask a real question.
	t.Setenv("KAIMAHI_CONFIRM", "")
	r := run.Default()
	r.Stdout, r.Stderr = f.out, f.errOut
	f.app = &App{
		Cfg: &config.Config{KindCluster: "kaimahi-p1", KubeContext: "kind-kaimahi-p1",
			ContextSource: config.SourceKubeCtx},
		Run: r, Out: f.out, Err: f.errOut,
	}
	return f
}

func (f *pgFixture) args() string {
	b, _ := os.ReadFile(f.argsLog)
	return string(b)
}

const completeDump = `--
-- PostgreSQL database dump
--
DROP TABLE IF EXISTS public.ledger_entry;
COPY public.credential (id, name) FROM stdin;
1	hello-world
\.
COPY public.ledger_entry (id, credential) FROM stdin;
1	hello-world
\.
--
-- PostgreSQL database dump complete
--
`

// A dump that stopped half way must not look like a backup. This is the
// whole reason the shell wrote a `.partial` and renamed it: a truncated file
// that is nevertheless present is a backup somebody will one day restore.
func TestBackupRefusesATruncatedDump(t *testing.T) {
	f := newPgFixture(t)
	t.Setenv("KMX_TEST_DUMP", strings.TrimSuffix(completeDump, "--\n-- PostgreSQL database dump complete\n--\n"))
	file := filepath.Join(f.dir, "backup.sql")

	if err := f.app.Backup(file); err == nil {
		t.Fatal("a dump with no trailer was accepted")
	} else if !strings.Contains(err.Error(), "did not complete") {
		t.Errorf("unexpected refusal: %v", err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("the truncated dump was left behind as a backup")
	}
	if _, err := os.Stat(file + ".partial"); err == nil {
		t.Error("the partial file was left behind")
	}
}

func TestBackupWritesACompleteDump(t *testing.T) {
	f := newPgFixture(t)
	t.Setenv("KMX_TEST_DUMP", completeDump)
	file := filepath.Join(f.dir, "backup.sql")

	if err := f.app.Backup(file); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	// A backup carries the ledger and the audit trails. It is 0600 from the
	// moment it exists, not after a chmod — the shell's `umask 077`.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the backup is mode %o, want 600", perm)
	}
	if !strings.Contains(f.out.String(), "2 tables") {
		t.Errorf("the summary did not count the tables: %q", f.out.String())
	}

	// The transport is the property that matters: pg_dump runs INSIDE the
	// pod over its unix socket, so no password leaves the pod and no local
	// client is needed.
	args := f.args()
	if !strings.Contains(args, "exec deploy/kaimahi-postgres -- pg_dump -U kaimahi -d kaimahi") {
		t.Errorf("the dump did not go through kubectl exec: %q", args)
	}
	if !strings.Contains(args, "--clean --if-exists") {
		t.Error("the dump is not a complete replacement (--clean --if-exists)")
	}
	// A read: no guard banner, exactly like `make backup`.
	if strings.Contains(f.errOut.String(), "About to") {
		t.Errorf("backup ran the guard: %q", f.errOut.String())
	}
}

// A file that is not a complete pg_dump is refused BEFORE the plane is
// quiesced. The restore would roll back either way — but the plane would
// have taken the outage for nothing, and the operator would read a psql
// error instead of the reason.
func TestRestoreRefusesAnIncompleteDumpWithoutQuiescing(t *testing.T) {
	f := newPgFixture(t)
	file := filepath.Join(f.dir, "truncated.sql")
	if err := os.WriteFile(file, []byte("DROP TABLE public.ledger_entry;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.app.Restore(file); err == nil {
		t.Fatal("an incomplete dump was restored")
	} else if !strings.Contains(err.Error(), "not a complete pg_dump") {
		t.Errorf("unexpected refusal: %v", err)
	}
	if strings.Contains(f.args(), "scale") {
		t.Error("the plane was quiesced for a file that was never going to load")
	}
	if err := f.app.Restore(filepath.Join(f.dir, "absent.sql")); err == nil {
		t.Error("a missing file was restored")
	}
}

func TestRestoreQuiescesAndBringsThePlaneBack(t *testing.T) {
	f := newPgFixture(t)
	t.Setenv("KMX_TEST_REPLICAS", "2")
	t.Setenv("KMX_TEST_ROWS", "41")
	file := filepath.Join(f.dir, "backup.sql")
	if err := os.WriteFile(file, []byte(completeDump), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.app.Restore(file); err != nil {
		t.Fatal(err)
	}
	args := f.args()
	for _, want := range []string{
		"scale deploy/kaimahi-proxy --replicas=0",
		"wait --for=delete pod -l app=kaimahi-proxy",
		"exec -i deploy/kaimahi-postgres -- psql",
		"ON_ERROR_STOP=1 --single-transaction",
		"scale deploy/kaimahi-proxy --replicas=2",
		"rollout status deploy/kaimahi-proxy",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("restore never ran %q:\n%s", want, args)
		}
	}
	// The order is the property: a proxy admitting calls during a --clean
	// restore could write ledger rows the restore then discards.
	down := strings.Index(args, "--replicas=0")
	load := strings.Index(args, "exec -i deploy/kaimahi-postgres")
	up := strings.LastIndex(args, "--replicas=2")
	if !(down < load && load < up) {
		t.Errorf("the restore did not run inside the quiesced window:\n%s", args)
	}
	if !strings.Contains(f.out.String(), "ledger_entry has 41 rows") {
		t.Errorf("the well-formed positive was not reported: %q", f.out.String())
	}
	// Guarded: this rewrites the ledger.
	if !strings.Contains(f.errOut.String(), "REPLACE the plane's database") {
		t.Errorf("restore did not run the guard: %q", f.errOut.String())
	}
}

// Whatever happens, the proxies come back. A failed restore that left the
// plane at zero replicas would turn a recoverable error into an outage.
func TestRestoreBringsTheProxiesBackAfterAFailedLoad(t *testing.T) {
	f := newPgFixture(t)
	t.Setenv("KMX_TEST_REPLICAS", "2")
	t.Setenv("KMX_TEST_PSQL_STATUS", "1")
	file := filepath.Join(f.dir, "backup.sql")
	if err := os.WriteFile(file, []byte(completeDump), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.app.Restore(file); err == nil {
		t.Fatal("a failed psql was reported as a restore")
	}
	if !strings.Contains(f.args(), "scale deploy/kaimahi-proxy --replicas=2") {
		t.Errorf("the proxies were left at zero replicas:\n%s", f.args())
	}
}

// An unreadable replica count aborts before anything is scaled: restoring to
// a guessed number of replicas is how a two-replica plane comes back as one.
func TestRestoreRefusesAnUnreadableReplicaCount(t *testing.T) {
	f := newPgFixture(t)
	// What the API server actually returns when the Deployment is not
	// there: no number at all.
	t.Setenv("KMX_TEST_REPLICAS", "<none>")
	file := filepath.Join(f.dir, "backup.sql")
	if err := os.WriteFile(file, []byte(completeDump), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.app.Restore(file); err == nil {
		t.Fatal("an unreadable replica count was accepted")
	}
	if strings.Contains(f.args(), "scale") {
		t.Errorf("the plane was scaled without knowing what to come back to:\n%s", f.args())
	}
}

// `status.phase=Running` is not "can take traffic": a pod draining after a
// rolling restart stays Running and keeps its IP, and a port-forward to it
// fails. That distinction cost the clone-free CI job a fix of its own (#67).
func TestProxyPodsSkipTerminatingAndNotReady(t *testing.T) {
	f := newPgFixture(t)
	t.Setenv("KMX_TEST_PODS", `{"items": [
	  {"metadata": {"name": "draining", "deletionTimestamp": "2026-09-03T09:00:00Z"},
	   "status": {"conditions": [{"type": "Ready", "status": "True"}]}},
	  {"metadata": {"name": "starting"},
	   "status": {"conditions": [{"type": "Ready", "status": "False"}]}},
	  {"metadata": {"name": "serving-a"},
	   "status": {"conditions": [{"type": "Ready", "status": "True"}]}},
	  {"metadata": {"name": "serving-b"},
	   "status": {"conditions": [{"type": "Ready", "status": "True"}]}}
	]}`)
	pods, err := f.app.proxyPods()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pods, ",") != "serving-a,serving-b" {
		t.Errorf("proxyPods = %v", pods)
	}

	// No pod that can take traffic is a failure, not an empty scrape.
	t.Setenv("KMX_TEST_PODS", `{"items": []}`)
	if err := f.app.Metrics(""); err == nil || !strings.Contains(err.Error(), "no running kaimahi-proxy pod") {
		t.Errorf("Metrics with no ready pod: %v", err)
	}
}
