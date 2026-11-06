//go:build !windows

package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

func residentAgentPair(t *testing.T) *agentfile.Pair {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", home)
	source := filepath.Join(t.TempDir(), "resident.agent.md")
	raw := []byte("---\nname: resident\ndescription: resident test\nmodel: fake\ntools: []\n---\nReply with exactly `hello ` followed by the input.\n")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	pair, err := agentfile.LoadPair(source, "")
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func TestLocalAgentSocketFallsBackFromLongTemporaryPath(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", filepath.Join("/tmp", strings.Repeat("long-runtime-", 10)))
	record := LocalAgentInstance{InstanceID: "li-" + strings.Repeat("a", 24)}
	socket, err := localAgentSocket(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(socket) > 103 || !strings.HasPrefix(socket, "/tmp/kmx-runtime-") {
		t.Fatalf("socket=%q", socket)
	}
}

func TestResidentPreparedTurnRequiresCommitAcknowledgment(t *testing.T) {
	pair := residentAgentPair(t)
	record, _, err := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer StopLocalAgent(t.Context(), record.ID)
	session, err := OpenLocalAgentSession(t.Context(), record.ID, "ack")
	if err != nil {
		t.Fatal(err)
	}
	socket, _ := localAgentSocket(record)
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 24)
	raw, _ := json.Marshal(localAgentIPCRequest{Op: "invoke", EventID: record.EventID, SessionID: session.ID, Input: "Ada", InvocationID: id})
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	var prepared localAgentIPCResponse
	if err := readLocalAgentFrame(bufio.NewReader(conn), &prepared); err != nil || prepared.Phase != "prepared" {
		t.Fatalf("prepared=%+v err=%v", prepared, err)
	}
	_ = conn.Close()
	deadline := time.Now().Add(time.Second)
	for {
		sessions, err := ReadLocalAgentSessions(t.Context(), record.ID)
		if err == nil && len(sessions) == 1 && sessions[0].Messages == 0 && !sessions[0].Active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unacknowledged turn committed or stayed locked: %+v %v", sessions, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	answer, err := InvokeLocalAgent(t.Context(), record.ID, session.ID, "Grace")
	if err != nil || answer != "hello Grace" {
		t.Fatalf("follow-up=%q err=%v", answer, err)
	}
}

func TestLocalAgentResidentSessionInvokeStop(t *testing.T) {
	pair := residentAgentPair(t)
	record, created, err := ActivateLocalAgent(t.Context(), "", pair, "fake", "", "fake")
	if err != nil || !created || record.State != "running" {
		t.Fatalf("activate: %+v created=%v err=%v", record, created, err)
	}
	session, err := OpenLocalAgentSession(t.Context(), record.ID, "first")
	if err != nil || !strings.HasPrefix(session.ID, "session-") || session.Label != "first" {
		t.Fatalf("open: %+v err=%v", session, err)
	}
	answer, err := InvokeLocalAgent(t.Context(), record.ID, session.ID, "Ada")
	if err != nil || answer != "hello Ada" {
		t.Fatalf("first invoke: %q err=%v", answer, err)
	}
	answer, err = InvokeLocalAgent(t.Context(), record.ID, session.ID, "Grace")
	if err != nil || answer != "hello Grace" {
		t.Fatalf("second invoke: %q err=%v", answer, err)
	}
	sessions, err := ReadLocalAgentSessions(t.Context(), record.ID)
	if err != nil || len(sessions) != 1 || sessions[0].Messages != 4 || sessions[0].Active || sessions[0].LastError != "" {
		t.Fatalf("sessions: %+v err=%v", sessions, err)
	}
	rows, err := ReadLocalAgentInstances(false)
	if err != nil || len(rows) != 1 || rows[0].State != "running" || !rows[0].Ready || rows[0].Sessions != 1 || rows[0].LastActivity == nil {
		t.Fatalf("rows: %+v err=%v", rows, err)
	}
	if err := StopLocalAgent(t.Context(), record.ID); err != nil {
		t.Fatal(err)
	}
	if err := PingLocalAgent(t.Context(), record); err == nil {
		t.Fatal("stopped worker still answered")
	}
}

func TestLocalAgentResidentCrashObserved(t *testing.T) {
	pair := residentAgentPair(t)
	pidFile := filepath.Join(t.TempDir(), "worker.pid")
	wrapper := filepath.Join(t.TempDir(), "worker")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$$\" > %s\nexec %s\n", shellQuote(pidFile), shellQuote(os.Args[0]))
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	record, _, err := ActivateLocalAgent(t.Context(), wrapper, pair, "fake", "", "fake")
	if err != nil {
		t.Fatal(err)
	}
	rawPID, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(rawPID))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, listErr := ReadLocalAgentInstances(false)
		if listErr == nil && len(rows) == 1 && rows[0].State == "crashed" && !rows[0].Ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("crash not observed: %+v err=%v", rows, listErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := PingLocalAgent(t.Context(), record); err == nil {
		t.Fatal("crashed worker answered ping")
	}
	if err := StopLocalAgent(t.Context(), record.ID); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAgentResidentRestartsAfterCrash(t *testing.T) {
	pair := residentAgentPair(t)
	pidFile := filepath.Join(t.TempDir(), "worker.pid")
	wrapper := filepath.Join(t.TempDir(), "worker")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$$\" > %s\nexec %s\n", shellQuote(pidFile), shellQuote(os.Args[0]))
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	first, _, err := ActivateLocalAgent(t.Context(), wrapper, pair, "fake", "", "fake")
	if err != nil {
		t.Fatal(err)
	}
	rawPID, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(rawPID))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, _ := ReadLocalAgentInstances(false)
		if len(rows) == 1 && rows[0].State == "crashed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("crash not observed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	second, created, err := ActivateLocalAgent(t.Context(), wrapper, pair, "fake", "", "fake")
	if err != nil || !created || second.EventID == first.EventID || second.State != "running" {
		t.Fatalf("restart: first=%+v second=%+v created=%v err=%v", first, second, created, err)
	}
	if err := StopLocalAgent(t.Context(), second.ID); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
