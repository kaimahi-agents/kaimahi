package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
