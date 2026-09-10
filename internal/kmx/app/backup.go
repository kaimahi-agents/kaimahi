package app

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
)

// Backup and restore of the plane's database.
//
// The property that matters most is the transport:
// pg_dump and psql run INSIDE the Postgres pod, over its unix socket, and
// the bytes travel through `kubectl exec`. The database password never
// leaves the pod (the socket authenticates the postgres OS user), nothing is
// written to disk in the cluster, and no local Postgres client is needed.
// That is carried across exactly; it is not replaced by a client connection.
//
// What a backup file holds: credential NAMES and token HASHES (never a
// token), caps, the ledger, the tool/inbound/approval audit trails (with
// whatever ids those trails record), grants and open spend holds. Keep it as
// you would the database — which is why kmx writes it 0600.

// dumpTrailer is pg_dump's last line, and the only well-formed positive
// either half of this pair accepts. A dump that stopped half way must not
// look like a backup, and a truncated file must not be restorable.
const dumpTrailer = "-- PostgreSQL database dump complete"

// Backup streams pg_dump out of the Postgres pod into a local file.
//
// A read: unguarded, exactly like `make backup` and `kmx ledger`.
func (a *App) Backup(file string) error {
	if file == "" {
		if err := os.MkdirAll("backups", 0o700); err != nil {
			return err
		}
		file = filepath.Join("backups", "kaimahi-"+time.Now().UTC().Format("20060102T150405Z")+".sql")
	}

	// Keep the overwrite contract, but say so before replacing an old backup.
	if _, err := os.Lstat(file); err == nil {
		a.notef("WARNING: plane-backup: %s already exists and will be replaced only after a complete dump is received.", file)
	} else if !os.IsNotExist(err) {
		return err
	}
	// Exclusive, unique creation gives us 0600 even if an old .partial is
	// world-readable or a symlink. Use the destination directory for rename.
	out, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".partial-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	removeTmp := true
	defer func() {
		out.Close()
		if removeTmp {
			os.Remove(tmp)
		}
	}()

	buffered := bufio.NewWriter(out)
	if err := a.Run.Pipe(nil, buffered, "kubectl", a.kubectl("-n", admin.Namespace,
		"exec", "deploy/kaimahi-postgres", "--",
		"pg_dump", "-U", "kaimahi", "-d", "kaimahi",
		"--clean", "--if-exists", "--no-owner", "--no-privileges")...); err != nil {
		return err
	}
	if err := buffered.Flush(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}

	complete, tables, size, err := inspectDump(tmp)
	if err != nil {
		return err
	}
	if !complete {
		return fmt.Errorf("plane-backup: dump did not complete (no trailer); nothing written to %s", file)
	}
	if err := os.Rename(tmp, file); err != nil {
		return err
	}
	removeTmp = false
	fmt.Fprintf(a.Out, "plane-backup: wrote %s (%d bytes, %d tables)\n", file, size, tables)
	return nil
}

// inspectDump reads a dump once and answers the three questions asked of it:
// did pg_dump finish, how many tables does it carry, and how big is it. The
// implementation answers them in one pass.
func inspectDump(path string) (complete bool, tables int, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, 0, 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	// A single COPY line can be long; a dump's data lines can be much
	// longer. 4MB per line, rather than bufio's 64KB default, which would
	// abort the scan on a wide audit row and report "no trailer" for a
	// perfectly good dump.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, dumpTrailer) {
			complete = true
		}
		if strings.HasPrefix(line, "COPY public.") {
			tables++
		}
	}
	if err := scanner.Err(); err != nil {
		return false, 0, 0, fmt.Errorf("cannot read %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		return false, 0, 0, err
	}
	return complete, tables, info.Size(), nil
}

// Restore loads a backup into the running plane's database, REPLACING its
// contents: the dump drops and recreates every table.
//
// Guarded, because it rewrites the ledger.
//
// Traffic is QUIESCED for the restore: the proxies are scaled to zero (their
// in-flight calls drain first), the tables are replaced, and the proxies are
// scaled back. A proxy admitting calls during a --clean restore could write
// ledger rows the restore then discards, or decide a budget against a
// half-loaded ledger. A restore is a short outage, never a concurrent write.
func (a *App) Restore(file string) (result error) {
	if file == "" {
		return fmt.Errorf("usage: kmx restore <backup.sql>")
	}
	info, err := os.Stat(file)
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("plane-restore: %s is missing or empty", file)
	}
	// Refuse a file that is not a complete pg_dump BEFORE anything is
	// scaled down. A truncated dump inside a --single-transaction restore
	// would roll back, but the plane would still have taken the outage for
	// nothing, and the operator would read a psql error instead of the
	// reason.
	complete, _, _, err := inspectDump(file)
	if err != nil {
		return err
	}
	if !complete {
		return fmt.Errorf("plane-restore: %s is not a complete pg_dump (no trailer); refusing", file)
	}

	if err := a.Guard("REPLACE the plane's database from "+file+" (every table is dropped and recreated)",
		a.operationCommand("restore", file)); err != nil {
		return err
	}

	replicas, err := a.proxyReplicas()
	if err != nil {
		return err
	}

	// Even a failed scale request may have reached the API server. Attempt
	// recovery on every failure, and retain both errors if recovery fails.
	restored := false
	defer func() {
		if !restored {
			if err := a.scaleProxy(replicas); err != nil {
				result = errors.Join(result, fmt.Errorf("plane-restore: failed to recover kaimahi-proxy to %d replicas: %w", replicas, err))
			}
		}
	}()
	a.notef("plane-restore: quiescing the plane (scaling kaimahi-proxy %d -> 0; in-flight calls drain)", replicas)
	if err := a.scaleProxy(0); err != nil {
		return err
	}
	if err := a.kubectlRun("-n", admin.Namespace, "wait", "--for=delete", "pod",
		"-l", "app=kaimahi-proxy", "--timeout=120s"); err != nil {
		return err
	}

	dump, err := os.Open(file)
	if err != nil {
		return err
	}
	defer dump.Close()
	// ON_ERROR_STOP makes a partial restore a failure rather than a
	// silently half-restored ledger; --single-transaction leaves the
	// database as it was if anything fails.
	if err := a.Run.Pipe(dump, nil, "kubectl", a.kubectl("-n", admin.Namespace,
		"exec", "-i", "deploy/kaimahi-postgres", "--",
		"psql", "-U", "kaimahi", "-d", "kaimahi", "-q",
		"-v", "ON_ERROR_STOP=1", "--single-transaction")...); err != nil {
		return err
	}

	// Well-formed positive: the ledger table answers a count afterwards.
	count, err := a.kubectlCapture("-n", admin.Namespace, "exec", "deploy/kaimahi-postgres", "--",
		"psql", "-U", "kaimahi", "-d", "kaimahi", "-tAc", "SELECT count(*) FROM ledger_entry")
	if err != nil {
		return fmt.Errorf("plane-restore: ledger unreadable after restore: %w", err)
	}
	rows, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil {
		return fmt.Errorf("plane-restore: ledger unreadable after restore: %q", strings.TrimSpace(count))
	}

	a.notef("plane-restore: restored %s; ledger_entry has %d rows; scaling kaimahi-proxy back to %d",
		file, rows, replicas)
	if err := a.scaleProxy(replicas); err != nil {
		return err
	}
	restored = true
	if replicas == 0 {
		fmt.Fprintf(a.Out, "plane-restore: restored %s; ledger_entry has %d rows; plane remains scaled to 0 replicas (not serving)\n", file, rows)
		return nil
	}
	if err := a.kubectlRun("-n", admin.Namespace, "rollout", "status", "deploy/kaimahi-proxy",
		"--timeout=300s"); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "plane-restore: restored %s; ledger_entry has %d rows; plane serving again\n", file, rows)
	return nil
}

// proxyReplicas reads the replica count to come back to. An unreadable count
// aborts before anything is scaled: restoring to a guessed number of
// replicas is how a two-replica plane comes back as one.
func (a *App) proxyReplicas() (int, error) {
	out, err := a.kubectlCapture("-n", admin.Namespace, "get", "deploy", "kaimahi-proxy",
		"-o", "jsonpath={.spec.replicas}")
	if err != nil {
		return 0, fmt.Errorf("plane-restore: cannot read the proxy's replica count: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("plane-restore: cannot read the proxy's replica count: %q", strings.TrimSpace(out))
	}
	return n, nil
}

func (a *App) scaleProxy(replicas int) error {
	return a.kubectlRun("-n", admin.Namespace, "scale", "deploy/kaimahi-proxy",
		"--replicas="+strconv.Itoa(replicas))
}
