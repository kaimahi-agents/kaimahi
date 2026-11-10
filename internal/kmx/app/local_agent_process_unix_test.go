//go:build !windows

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/residenttools"
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
	wantRoot := "/tmp/kmx-runtime-"
	if runtime.GOOS == "darwin" {
		wantRoot = "/private/tmp/kmx-runtime-"
	}
	if len(socket) > 103 || !strings.HasPrefix(socket, wantRoot) {
		t.Fatalf("socket=%q", socket)
	}
}

func TestLocalAgentWorkerEnvironmentPreservesSelectedGHConfigOnly(t *testing.T) {
	t.Setenv("GH_CONFIG_DIR", "/private/gh-profile")
	t.Setenv("GH_TOKEN", "secret")
	t.Setenv("GITHUB_TOKEN", "secret")
	env := localAgentWorkerEnvironment(LocalAgentInstance{ID: "md-123", EventID: strings.Repeat("a", 24)})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GH_CONFIG_DIR=/private/gh-profile") || strings.Contains(joined, "GH_TOKEN=") || strings.Contains(joined, "GITHUB_TOKEN=") {
		t.Fatalf("worker environment=%q", env)
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

func TestResidentCommandToolRunsThroughBroker(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", home)
	t.Setenv("RESIDENT_TOOL_SECRET", "must-not-reach-broker-command")
	dir := t.TempDir()
	source := filepath.Join(dir, "resident.agent.md")
	command := filepath.Join(dir, "command")
	raw := []byte("---\nname: resident\ndescription: resident tool test\nmodel: fake\ntools: [check]\n---\nCall the tool `check`. Reply with exactly `tool complete`.\n")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	commandSource := "#!/bin/sh\nexec " + shellQuote(os.Args[0]) + " -test.run=^TestResidentCommandToolHelper$ -- resident-tool-helper\n"
	if err := os.WriteFile(command, []byte(commandSource), 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("tools: {check: Enabled}\ncommandTools:\n  check:\n    command: %q\n    args: []\n", command)
	if err := os.WriteFile(filepath.Join(dir, "kmx.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	pair, err := agentfile.LoadPair(source, "")
	if err != nil {
		t.Fatal(err)
	}
	commands, err := localtools.FreezeCommandPlan(t.Context(), source, pair.Agent.Tools)
	if err != nil {
		t.Fatal(err)
	}
	planValue, err := residenttools.NewBoundedProcessPlan(commands)
	if err != nil {
		t.Fatal(err)
	}
	planValue.Binding.Limits.MaxCallsPerSession = 2
	planValue.Binding.Limits.MaxCallsPerInstance = 2
	planValue.Binding.Limits.MaxCallsPerTurn = 1
	plan := &planValue
	record, created, err := ActivateLocalAgentWithOptions(t.Context(), "", pair, "fake", "", "fake", LocalAgentActivationOptions{Mode: LocalAgentActivationNormal, ToolPlan: plan})
	if err != nil || !created || record.AgentBackend != residenttools.ResidentProcess || record.ToolBackend != residenttools.BoundedProcess || record.ToolConfigRevision != commands.ConfigRevision || record.ToolPlanRevision != plan.Revision() || record.ToolContractDigest != plan.ContractDigest() || record.ToolBindingDigest != plan.BindingDigest() {
		t.Fatalf("record=%+v created=%v err=%v", record, created, err)
	}
	defer StopLocalAgent(t.Context(), record.ID)
	session, err := OpenLocalAgentSession(t.Context(), record.ID, "tools")
	if err != nil {
		t.Fatal(err)
	}
	answer, err := InvokeLocalAgent(t.Context(), record.ID, session.ID, "run the fixed check")
	if err != nil || answer != "tool complete" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	answer, err = InvokeLocalAgent(t.Context(), record.ID, session.ID, "run it again")
	if err != nil || answer != "tool complete" {
		t.Fatalf("follow-up answer=%q err=%v", answer, err)
	}
	if _, err := InvokeLocalAgent(t.Context(), record.ID, session.ID, "limit this call"); err == nil {
		t.Fatal("broker exceeded the bound call budget")
	}
	if err := os.WriteFile(command, []byte(commandSource+"# changed\n"), 0700); err != nil {
		t.Fatal(err)
	}
	changedCommands, err := localtools.FreezeCommandPlan(t.Context(), source, pair.Agent.Tools)
	if err != nil || changedCommands.Revision == commands.Revision {
		t.Fatalf("changed command plan=%+v err=%v", changedCommands, err)
	}
	changedValue, err := residenttools.NewBoundedProcessPlan(changedCommands)
	if err != nil {
		t.Fatal(err)
	}
	changed := &changedValue
	_, _, err = ActivateLocalAgentWithOptions(t.Context(), "", pair, "fake", "", "fake", LocalAgentActivationOptions{Mode: LocalAgentActivationNormal, ToolPlan: changed})
	var drift *LocalAgentDriftError
	if !errors.As(err, &drift) {
		t.Fatalf("tool configuration drift was not refused: %v", err)
	}
}

func TestResidentCommandToolCancellationKeepsBrokerUsable(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", home)
	dir := t.TempDir()
	source := filepath.Join(dir, "resident.agent.md")
	command := filepath.Join(dir, "command")
	if err := os.WriteFile(source, []byte("---\nname: resident\nmodel: fake\ntools: [check]\n---\nCall the tool `check`. Reply with exactly `tool complete`.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "started")
	commandSource := "#!/bin/sh\nexec " + shellQuote(os.Args[0]) + " -test.run=^TestResidentCommandToolHelper$ -- resident-tool-slow " + shellQuote(marker) + "\n"
	if err := os.WriteFile(command, []byte(commandSource), 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("tools: {check: Enabled}\ncommandTools:\n  check:\n    command: %q\n    args: []\n", command)
	if err := os.WriteFile(filepath.Join(dir, "kmx.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	pair, err := agentfile.LoadPair(source, "")
	if err != nil {
		t.Fatal(err)
	}
	commands, err := localtools.FreezeCommandPlan(t.Context(), source, pair.Agent.Tools)
	if err != nil {
		t.Fatal(err)
	}
	planValue, err := residenttools.NewBoundedProcessPlan(commands)
	if err != nil {
		t.Fatal(err)
	}
	plan := &planValue
	record, _, err := ActivateLocalAgentWithOptions(t.Context(), "", pair, "fake", "", "fake", LocalAgentActivationOptions{Mode: LocalAgentActivationNormal, ToolPlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	defer StopLocalAgent(t.Context(), record.ID)
	session, err := OpenLocalAgentSession(t.Context(), record.ID, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, invokeErr := InvokeLocalAgent(ctx, record.ID, session.ID, "start slow command")
		result <- invokeErr
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, statErr := os.Stat(marker); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("broker command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call err=%v", err)
	}
	answer, err := InvokeLocalAgent(t.Context(), record.ID, session.ID, "retry after observed cancellation")
	if err != nil || answer != "tool complete" {
		t.Fatalf("broker unusable after cancellation: answer=%q err=%v", answer, err)
	}
}

func TestResidentCommandToolHelper(t *testing.T) {
	for i, value := range os.Args {
		if value == "resident-tool-helper" {
			if os.Getenv("RESIDENT_TOOL_SECRET") != "" {
				t.Fatal("broker command inherited ambient environment")
			}
			fmt.Fprint(os.Stdout, "broker command completed")
			return
		}
		if value == "resident-tool-slow" && i+1 < len(os.Args) {
			marker := os.Args[i+1]
			if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
				if err := os.WriteFile(marker, []byte("started"), 0600); err != nil {
					t.Fatal(err)
				}
				time.Sleep(30 * time.Second)
			}
			fmt.Fprint(os.Stdout, "broker command completed")
			return
		}
	}
	t.Skip("helper process only")
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
