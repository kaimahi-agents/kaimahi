package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentInstancesOnlyAppearAfterRun(t *testing.T) {
	a := mdFixture(t)
	var out bytes.Buffer
	if err := AgentInstanceStatus(&out, "", "table"); err != nil || !strings.Contains(out.String(), "No agent instances") {
		t.Fatalf("%s %v", &out, err)
	}
	s := NewMarkdownSession(io.Discard, io.Discard)
	s.Provider = "copilot"
	s.executor = &mdFakeExecutor{answer: "Hello"}
	if err := s.StartAgent(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadAgentInstances("")
	if err != nil || len(rows) != 1 || rows[0].State != "running" || rows[0].Version != a.Revision {
		t.Fatalf("%+v %v", rows, err)
	}
	if _, _, err := beginAgentInstance(a, ""); err == nil {
		t.Fatal("concurrent instance replaced live owner")
	}
	s.Close()
	rows, err = ReadAgentInstances("")
	if err != nil || rows[0].State != "stopped" {
		t.Fatalf("%+v %v", rows, err)
	}
	if err := os.Remove(a.Path); err != nil {
		t.Fatal(err)
	}
	rows, err = ReadAgentInstances("")
	if err != nil || len(rows) != 1 {
		t.Fatal("status depends on original source file")
	}
}

func TestRemoveInstancePreservesSourceCacheAndGeneration(t *testing.T) {
	a := mdFixture(t)
	lease, _, err := beginAgentInstance(a, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = RemoveAgentInstance(t.Context(), ShortAgentID(lease.record.ID), false); err == nil {
		t.Fatal("running instance removed")
	}
	if err = lease.finish(nil); err != nil {
		t.Fatal(err)
	}
	root, _ := agentStateRoot()
	cached := filepath.Join(root, "versions", strings.TrimPrefix(a.Revision, "sha256:"), "agent.md")
	if err = saveMarkdownResult(a, MarkdownTestResult{SchemaVersion: 1, ID: "before-removal", RunKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if _, err = RemoveAgentInstance(t.Context(), a.Name, false); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadAgentInstances("")
	if err != nil || len(rows) != 0 {
		t.Fatalf("removed still listed: %+v %v", rows, err)
	}
	if _, err = FindAgentInstance(lease.record.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("removed instance resolves")
	}
	for _, path := range []string{a.Path, a.TestsPath, cached} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("deleted retained content: %s %v", path, err)
		}
	}
	if history, err := LoadMarkdownHistory(a); err != nil || len(history) != 1 {
		t.Fatal("test history removed")
	}
	recreated, _, err := beginAgentInstance(a, "")
	if err != nil {
		t.Fatal(err)
	}
	defer recreated.finish(nil)
	if recreated.record.Generation != lease.record.Generation+1 {
		t.Fatal("removed generation reused")
	}
}

func TestForceRemoveStopsOnlySelectedVersion(t *testing.T) {
	a := mdFixture(t)
	lease, _, err := beginAgentInstance(a, "")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "kmx-rm-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "rpc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	lease.record.Socket = socket
	if err = lease.save(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			done <- e
			return
		}
		defer conn.Close()
		var req agentWorkerRequest
		if e = json.NewDecoder(conn).Decode(&req); e != nil {
			done <- e
			return
		}
		if req.Operation != "stop" || req.AgentID != lease.record.ID || req.Generation != lease.record.Generation {
			done <- errors.New("unbound stop request")
			return
		}
		e = lease.finish(nil)
		_ = json.NewEncoder(conn).Encode(agentWorkerReply{Ready: e == nil})
		done <- e
	}()
	if _, err = RemoveAgentInstance(t.Context(), a.Name, true); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	rows, err := ReadAgentInstances("")
	if err != nil || len(rows) != 0 {
		t.Fatal("force remove did not remove record")
	}
}

func TestAgentAgeFromUTCStart(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		elapsed time.Duration
		want    string
	}{{0, "0s"}, {45 * time.Second, "45s"}, {12 * time.Minute, "12m"}, {90 * time.Minute, "1h30m"}, {49 * time.Hour, "2d1h"}, {-time.Second, "clock skew"}} {
		if got := agentAge(now.Add(-tc.elapsed), now); got != tc.want {
			t.Fatalf("%s: %s", tc.want, got)
		}
	}
	if agentAge(time.Time{}, now) != "unknown" {
		t.Fatal("zero timestamp gained age")
	}
}

func TestAgentIDResolutionPrecedenceAndCollisions(t *testing.T) {
	a := AgentInstance{ID: "md-abcdef123456" + strings.Repeat("a", 52), Name: "frog", Source: "/one/frog.agent.md", State: "running"}
	b := AgentInstance{ID: "md-abcdef123456" + strings.Repeat("b", 52), Name: "frog", Source: "/two/frog.agent.md", State: "stopped"}
	c := AgentInstance{ID: "md-123456" + strings.Repeat("c", 58), Name: "abcdef123456" + strings.Repeat("a", 52), Source: "/three/other.agent.md"}
	rows := []AgentInstance{a, b, c}
	for _, selector := range []string{a.ID, strings.TrimPrefix(a.ID, "md-"), "abcdef123456a", "md-abcdef123456a", a.Source} {
		r, err := resolveAgentInstance(rows, selector)
		if err != nil || r.ID != a.ID {
			t.Fatalf("%s: %+v %v", selector, r, err)
		}
	}
	for _, selector := range []string{"frog", "abcdef", "md-abcdef"} {
		_, err := resolveAgentInstance(rows, selector)
		if err == nil || !strings.Contains(err.Error(), a.Source) || !strings.Contains(err.Error(), b.Source) {
			t.Fatalf("ambiguity %s: %v", selector, err)
		}
	}
	if len(agentDisplayID(a.ID, rows)) != 13 {
		t.Fatal("colliding displayed IDs not lengthened")
	}
	if _, err := resolveAgentInstance(rows, "abc"); err == nil || !strings.Contains(err.Error(), "at least 6") {
		t.Fatal(err)
	}
	if _, err := resolveAgentInstance(rows, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestInspectInstanceUsesCacheAfterSourceDeletion(t *testing.T) {
	a := mdFixture(t)
	lease, _, err := beginAgentInstance(a, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.finish(nil); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(a.Path); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = InspectAgentInstance(t.Context(), &out, ShortAgentID(lease.record.ID), "json"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), a.Instructions) || !strings.Contains(out.String(), lease.record.ID) {
		t.Fatal(out.String())
	}
	out.Reset()
	if err = ListAgentInstances(&out, "", "json", false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), lease.record.ID) {
		t.Fatal("stopped instance in running-only list")
	}
	out.Reset()
	if err = ListAgentInstances(&out, "", "json", true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), lease.record.ID) {
		t.Fatal("all list omitted stopped instance")
	}
}

func TestAgentVersionsAreImmutableAndRerunsUpdate(t *testing.T) {
	a := mdFixture(t)
	lease, frozen, err := beginAgentInstance(a, "")
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Instructions != a.Instructions {
		t.Fatal("wrong snapshot")
	}
	if err = lease.finish(nil); err != nil {
		t.Fatal(err)
	}
	root, _ := agentStateRoot()
	oldPath := filepath.Join(root, "versions", strings.TrimPrefix(a.Revision, "sha256:"), "agent.md")
	info, _ := os.Stat(oldPath)
	lease, _, err = beginAgentInstance(a, "")
	if err != nil {
		t.Fatal(err)
	}
	if lease.record.Generation != 2 {
		t.Fatal("same source did not retrigger")
	}
	_ = lease.finish(nil)
	after, _ := os.Stat(oldPath)
	if !after.ModTime().Equal(info.ModTime()) {
		t.Fatal("rewrote cached version")
	}
	if err = os.WriteFile(a.Path, []byte(strings.Replace(string(a.Source), "Greet the user.", "Say goodbye.", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := ReadMarkdownAgent(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	lease, frozen, err = beginAgentInstance(b, "")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.finish(nil)
	if lease.record.Generation != 3 || lease.record.PreviousVersion != a.Revision || lease.record.Version == a.Revision || frozen.Instructions != "Say goodbye." {
		t.Fatalf("%+v", lease.record)
	}
	old, err := os.ReadFile(oldPath)
	if err != nil || !bytes.Equal(old, a.Source) {
		t.Fatal("old version lost")
	}
}

func TestAgentInstanceAbandonedLeaseIsStopped(t *testing.T) {
	a := mdFixture(t)
	lease, _, err := beginAgentInstance(a, "")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate process death: the kernel releases the lock, but running remains on disk.
	lease.file.Close()
	lease.file = nil
	rows, err := ReadAgentInstances("")
	if err != nil || rows[0].State != "stopped" || rows[0].Finished != nil || !strings.Contains(rows[0].Reason, "owner exited") {
		t.Fatalf("%+v %v", rows, err)
	}
	var persisted AgentInstance
	if err = mdStateRead(lease.path, &persisted); err != nil || persisted.State != "running" {
		t.Fatal("read-only status rewrote instance")
	}
}
