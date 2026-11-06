package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

func localRegistrationPair(t *testing.T) *agentfile.Pair {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", home)
	source := filepath.Join(t.TempDir(), "greeter.agent.md")
	if err := os.WriteFile(source, []byte("---\nname: greeter\ndescription: greet\nmodel: fake\ntools: []\n---\nGreet.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pair, err := agentfile.LoadPair(source, "")
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func TestLocalRegistrationActivateListStop(t *testing.T) {
	pair := localRegistrationPair(t)
	record, created, err := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
	if err != nil || !created || record.State != "running" {
		t.Fatalf("record=%+v created=%v err=%v", record, created, err)
	}
	reused, created, err := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
	if err != nil || created || reused.EventID != record.EventID {
		t.Fatalf("reuse=%+v created=%v err=%v", reused, created, err)
	}
	rows, err := ReadLocalAgentInstances(false)
	if err != nil || len(rows) != 1 || rows[0].State != "running" || !rows[0].Ready {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if err := StopLocalAgent(t.Context(), record.ID); err != nil {
		t.Fatal(err)
	}
	if rows, err = ReadLocalAgentInstances(false); err != nil || len(rows) != 0 {
		t.Fatalf("active after stop: %+v %v", rows, err)
	}
	rows, err = ReadLocalAgentInstances(true)
	if err != nil || len(rows) != 1 || rows[0].State != "stopped" || rows[0].Stopped == nil || !rows[0].Stopped.After(rows[0].Started) {
		t.Fatalf("stopped=%+v err=%v", rows, err)
	}
}

func TestLocalRegistrationConcurrentActivateCreatesOneEvent(t *testing.T) {
	pair := localRegistrationPair(t)
	type result struct {
		record  LocalAgentInstance
		created bool
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, c, e := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
			results <- result{r, c, e}
		}()
	}
	wg.Wait()
	close(results)
	created, event := 0, ""
	for got := range results {
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.created {
			created++
		}
		if event == "" {
			event = got.record.EventID
		} else if event != got.record.EventID {
			t.Fatal("concurrent activation returned different events")
		}
	}
	if created != 1 {
		t.Fatalf("created=%d", created)
	}
	if err := StopLocalAgent(t.Context(), LocalAgentID(pair.AgentPath)); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRegistrationConcurrentSourcesKeepRuntimeNamesUnique(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", home)
	pairs := make([]*agentfile.Pair, 2)
	for i := range pairs {
		source := filepath.Join(t.TempDir(), "greeter.agent.md")
		if err := os.WriteFile(source, []byte("---\nname: greeter\ndescription: greet\nmodel: fake\ntools: []\n---\nGreet.\n"), 0600); err != nil {
			t.Fatal(err)
		}
		pair, err := agentfile.LoadPair(source, "")
		if err != nil {
			t.Fatal(err)
		}
		pairs[i] = pair
	}
	type result struct {
		record LocalAgentInstance
		err    error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, pair := range pairs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record, _, err := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
			results <- result{record: record, err: err}
		}()
	}
	wg.Wait()
	close(results)
	succeeded, refused := 0, 0
	started := []string{}
	for got := range results {
		if got.err == nil {
			succeeded++
			started = append(started, got.record.InstanceID)
		} else if strings.Contains(got.err.Error(), "runtime name is already active") {
			refused++
		} else {
			t.Fatal(got.err)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("succeeded=%d refused=%d", succeeded, refused)
	}
	for _, id := range started {
		if err := StopLocalAgent(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLocalAgentInventoryCapacityRefusesBeforeCreatingSourceDirectory(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", home)
	instances, err := localAgentDirectories(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "local-agents", "instances")
	for i := 0; i < 1024; i++ {
		id := LocalAgentID(fmt.Sprintf("source-%d", i))
		if err := os.Mkdir(filepath.Join(root, id), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := instances.Close(); err != nil {
		t.Fatal(err)
	}
	newID := LocalAgentID("new-source")
	if dir, _, err := localAgentInstanceDirectory(t.Context(), newID, true); err == nil || !strings.Contains(err.Error(), "source limit") {
		if dir != nil {
			_ = dir.Close()
		}
		t.Fatalf("capacity err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, newID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new source directory exists: %v", err)
	}
}

func TestLocalAgentTransitionWaitHonorsContext(t *testing.T) {
	pair := localRegistrationPair(t)
	dir, _, err := localAgentInstanceDirectory(t.Context(), LocalAgentID(pair.AgentPath), true)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	lock, err := fileaccess.EnsurePrivateFileAt(t.Context(), dir, "transition.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := fileaccess.TryLock(t.Context(), lock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := withLocalAgentTransition(ctx, dir, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestValidLocalAgentRecordAcceptsAuthoredIDOutsideRuntimeNameRules(t *testing.T) {
	record := LocalAgentInstance{
		SchemaVersion: localAgentSchema, EventID: "1234567890abcdef12345678", InstanceID: "li-1234567890abcdef12345678", Sequence: 1,
		ID: "md-1234567890abcdef1234567890abcdef", Name: "runtime", AuthoredName: "authored-", Source: "/tmp/source.agent.md",
		AgentRevision: "sha256:" + strings.Repeat("1", 64), SnapshotRevision: "sha256:" + strings.Repeat("2", 64), TestsState: "none",
		Provider: "fake", Model: "fake", Started: time.Now().UTC(), State: "active",
	}
	record.ID = LocalAgentID(record.Source)
	dir := filepath.Join("/tmp", record.ID)
	record.CachedAgent = filepath.Join(dir, "snapshot-"+strings.Repeat("2", 64)+".agent.md")
	if !validLocalAgentRecord(record, dir) {
		t.Fatal("authored ID was incorrectly validated as a runtime name")
	}
}

func TestLocalRegistrationRejectsMalformedStop(t *testing.T) {
	pair := localRegistrationPair(t)
	record, _, err := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probeLocalAgent(t.Context(), record, "shutdown", localAgentIPCRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := waitLocalAgentStopped(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	dir, _, err := localAgentInstanceDirectory(t.Context(), record.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileaccess.PublishPrivate(t.Context(), dir, "stop-"+record.EventID+".json", []byte(`{"bad":true}`)); err != nil {
		t.Fatal(err)
	}
	_ = dir.Close()
	if _, err := ReadLocalAgentInstances(true); err == nil {
		t.Fatal("malformed stop accepted")
	}
}

func TestLocalRegistrationRejectsOrphanWarning(t *testing.T) {
	pair := localRegistrationPair(t)
	record, _, err := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = StopLocalAgent(t.Context(), record.InstanceID) }()
	dir, _, err := localAgentInstanceDirectory(t.Context(), record.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	orphan := "abcdefabcdefabcdefabcdef"
	raw := []byte(fmt.Sprintf(`{"schemaVersion":2,"eventId":%q,"warning":"cleanup incomplete","observed":"2026-11-06T01:02:03Z"}`, orphan))
	if _, err := fileaccess.PublishPrivate(t.Context(), dir, "warning-"+orphan+".json", raw); err != nil {
		t.Fatal(err)
	}
	_ = dir.Close()
	if _, err := ReadLocalAgentInstances(true); err == nil || !strings.Contains(err.Error(), "no matching start") {
		t.Fatalf("orphan warning err=%v", err)
	}
}

func TestLocalRegistrationDriftNewAndUpgrade(t *testing.T) {
	pair := localRegistrationPair(t)
	first, _, err := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = StopLocalAgent(t.Context(), first.InstanceID) }()
	if err := os.WriteFile(pair.AgentPath, []byte("---\nname: greeter\ndescription: greet\nmodel: fake\ntools: []\n---\nGreet differently.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := agentfile.LoadPair(pair.AgentPath, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ActivateLocalAgent(t.Context(), "", changed, "fake", "", "fake")
	var drift *LocalAgentDriftError
	if !errors.As(err, &drift) || drift.Active.InstanceID != first.InstanceID {
		t.Fatalf("drift=%v", err)
	}
	parallel, created, err := ActivateLocalAgentWithOptions(t.Context(), "", changed, "fake", "", "fake", LocalAgentActivationOptions{Mode: LocalAgentActivationNew})
	if err != nil || !created || parallel.InstanceID == first.InstanceID || parallel.Name == first.Name {
		t.Fatalf("parallel=%+v err=%v", parallel, err)
	}
	if _, err := ReadLocalAgentRecord(first.ID); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("stable selector err=%v", err)
	}
	if _, err := ReadLocalAgentRecord(parallel.Name); err != nil {
		t.Fatal(err)
	}
	upgraded, created, err := ActivateLocalAgentWithOptions(t.Context(), "", changed, "fake", "", "fake", LocalAgentActivationOptions{Mode: LocalAgentActivationUpgrade})
	if err != nil || !created || !upgraded.Primary {
		t.Fatalf("upgrade=%+v err=%v", upgraded, err)
	}
	if rows, err := ReadLocalAgentInstances(false); err != nil || len(rows) != 1 || rows[0].InstanceID != upgraded.InstanceID {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if _, err := ReadLocalAgentSessions(t.Context(), first.InstanceID); err == nil {
		t.Fatal("old sessions remained routable")
	}
	_ = StopLocalAgent(t.Context(), upgraded.InstanceID)
}

func TestLocalRegistrationFailedUpgradePreservesOld(t *testing.T) {
	pair := localRegistrationPair(t)
	first, _, err := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = StopLocalAgent(t.Context(), first.InstanceID) }()
	if err := os.WriteFile(pair.AgentPath, []byte("---\nname: greeter\ndescription: greet\nmodel: fake\ntools: []\n---\nChanged.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := agentfile.LoadPair(pair.AgentPath, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ActivateLocalAgentWithOptions(t.Context(), filepath.Join(t.TempDir(), "missing"), changed, "fake", "", "fake", LocalAgentActivationOptions{Mode: LocalAgentActivationUpgrade})
	if err == nil {
		t.Fatal("failed worker start accepted")
	}
	if err := PingLocalAgent(t.Context(), first); err != nil {
		t.Fatalf("old worker lost: %v", err)
	}
}
