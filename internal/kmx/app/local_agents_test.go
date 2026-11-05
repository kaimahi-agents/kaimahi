package app

import (
	"os"
	"path/filepath"
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
	if err != nil || !created || record.State != "active" {
		t.Fatalf("record=%+v created=%v err=%v", record, created, err)
	}
	reused, created, err := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
	if err != nil || created || reused.EventID != record.EventID {
		t.Fatalf("reuse=%+v created=%v err=%v", reused, created, err)
	}
	rows, err := ReadLocalAgentInstances(false)
	if err != nil || len(rows) != 1 || rows[0].State != "active" {
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
}

func TestLocalRegistrationRejectsMalformedStop(t *testing.T) {
	pair := localRegistrationPair(t)
	record, _, err := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
	if err != nil {
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
