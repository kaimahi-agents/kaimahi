package app

import (
	"context"
	"encoding/json"
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

func TestLocalInventoryRetiresInterruptedPublicationStage(t *testing.T) {
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
	stage, err := fileaccess.CreatePrivateFileAt(t.Context(), dir, ".kmx-stage-"+strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	_ = dir.Close()
	if rows, err := ReadLocalAgentInstances(false); err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	dir, _, err = localAgentInstanceDirectory(t.Context(), record.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	for _, entry := range mustReadLocalAgentEntries(t, dir) {
		if strings.HasPrefix(entry.Name(), ".kmx-stage-") {
			t.Fatalf("stage remained: %s", entry.Name())
		}
	}
}

func mustReadLocalAgentEntries(t *testing.T, dir *fileaccess.Directory) []os.DirEntry {
	t.Helper()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestInterruptedUpgradeKeepsInstancesUniquelyRoutable(t *testing.T) {
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
	event, err := randomEventID()
	if err != nil {
		t.Fatal(err)
	}
	dir, path, err := localAgentInstanceDirectory(t.Context(), first.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	agent, tests, err := cachePair(t.Context(), dir, path, changed)
	if err != nil {
		t.Fatal(err)
	}
	provisional := "greeter-" + event[:8]
	replacement := LocalAgentInstance{SchemaVersion: localAgentSchema, EventID: event, InstanceID: "li-" + event, Sequence: first.Sequence + 1, ID: first.ID, Name: provisional, AuthoredName: "greeter", Source: changed.AgentPath, AgentRevision: changed.Agent.Revision, SnapshotRevision: changed.SnapshotRevision, TestsState: changed.Tests.State, TestsPath: changed.Tests.Selection.Path, CachedAgent: agent, CachedTests: tests, Provider: "fake", Model: "fake", Started: time.Now().UTC(), State: "active"}
	raw, _ := json.Marshal(replacement)
	if err := publishPrivateBytes(t.Context(), dir, "start-"+event+".json", raw); err != nil {
		t.Fatal(err)
	}
	_ = dir.Close()
	if err := startLocalAgentWorker(t.Context(), "", replacement); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = StopLocalAgent(t.Context(), replacement.InstanceID) }()
	rows, err := ReadLocalAgentInstances(false)
	if err != nil || len(rows) != 2 || rows[0].Name == rows[1].Name {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if _, err := ReadLocalAgentRecord("greeter"); err != nil {
		t.Fatalf("old primary name not routable: %v", err)
	}
	if _, err := ReadLocalAgentRecord(provisional); err != nil {
		t.Fatalf("replacement provisional name not routable: %v", err)
	}
}

func TestLegacyPromotionRecordKeepsSchemaTwoInventoryReadable(t *testing.T) {
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
	legacy := []byte(fmt.Sprintf(`{"schemaVersion":2,"eventId":%q,"instanceId":%q,"promoted":"2026-11-06T01:02:03Z"}`, record.EventID, record.InstanceID))
	name := "primary-" + record.EventID + ".json"
	existing, err := fileaccess.OpenPrivateFileAt(t.Context(), dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileaccess.RemovePrivateFileAt(t.Context(), dir, name, existing); err != nil {
		t.Fatal(err)
	}
	_ = existing.Close()
	if err := publishPrivateBytes(t.Context(), dir, name, legacy); err != nil {
		t.Fatal(err)
	}
	_ = dir.Close()
	rows, err := ReadLocalAgentInstances(false)
	if err != nil || len(rows) != 1 || rows[0].Name != record.Name || !rows[0].Primary {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestPromotedUpgradeRecoveryNameAvoidsParallelInstance(t *testing.T) {
	pair := localRegistrationPair(t)
	first, _, err := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = StopLocalAgent(t.Context(), first.InstanceID) }()
	recoveryName := "greeter-" + first.EventID[:8]
	parallel, _, err := ActivateLocalAgentWithOptions(t.Context(), "", pair, "fake", "", "fake", LocalAgentActivationOptions{Mode: LocalAgentActivationNew, Name: recoveryName})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = StopLocalAgent(t.Context(), parallel.InstanceID) }()
	event, err := randomEventID()
	if err != nil {
		t.Fatal(err)
	}
	dir, path, err := localAgentInstanceDirectory(t.Context(), first.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	agent, tests, err := cachePair(t.Context(), dir, path, pair)
	if err != nil {
		t.Fatal(err)
	}
	replacement := LocalAgentInstance{SchemaVersion: localAgentSchema, EventID: event, InstanceID: "li-" + event, Sequence: parallel.Sequence + 1, ID: first.ID, Name: "greeter-" + event[:8], AuthoredName: "greeter", Source: pair.AgentPath, AgentRevision: pair.Agent.Revision, SnapshotRevision: pair.SnapshotRevision, TestsState: pair.Tests.State, TestsPath: pair.Tests.Selection.Path, CachedAgent: agent, CachedTests: tests, Provider: "fake", Model: "fake", Started: time.Now().UTC(), State: "active"}
	raw, _ := json.Marshal(replacement)
	if err := publishPrivateBytes(t.Context(), dir, "start-"+event+".json", raw); err != nil {
		t.Fatal(err)
	}
	_ = dir.Close()
	if err := startLocalAgentWorker(t.Context(), "", replacement); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = StopLocalAgent(t.Context(), replacement.InstanceID) }()
	dir, _, err = localAgentInstanceDirectory(t.Context(), first.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLocalAgentPromotion(t.Context(), dir, replacement, "greeter"); err != nil {
		t.Fatal(err)
	}
	_ = dir.Close()
	rows, err := ReadLocalAgentInstances(false)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	names := map[string]bool{}
	for _, row := range rows {
		if names[row.Name] {
			t.Fatalf("duplicate projected name %q: %+v", row.Name, rows)
		}
		names[row.Name] = true
	}
}

func TestPromotionRefusesNameOwnedByAnotherSource(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", home)
	makePair := func(dir, name string) *agentfile.Pair {
		t.Helper()
		path := filepath.Join(dir, name+".agent.md")
		raw := []byte(fmt.Sprintf("---\nname: %s\ndescription: test\nmodel: fake\ntools: []\n---\nTest.\n", name))
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		pair, err := agentfile.LoadPair(path, "")
		if err != nil {
			t.Fatal(err)
		}
		return pair
	}
	owner := makePair(t.TempDir(), "taken")
	owned, _, err := ActivateLocalAgent(t.Context(), "", owner, "fake", "", "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = StopLocalAgent(t.Context(), owned.InstanceID) }()
	pair := makePair(t.TempDir(), "other")
	record, _, err := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = StopLocalAgent(t.Context(), record.InstanceID) }()
	dir, _, err := localAgentInstanceDirectory(t.Context(), record.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := writeLocalAgentPromotion(t.Context(), dir, record, "taken"); err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("promotion err=%v", err)
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
