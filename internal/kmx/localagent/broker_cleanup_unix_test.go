//go:build !windows

package localagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/residenttools"
)

func TestBrokerEscapedPipeFixture(t *testing.T) {
	at := slices.Index(os.Args, "--")
	if at < 0 {
		return
	}
	mode, marker := os.Args[at+1], os.Args[at+2]
	if mode == "child" {
		if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(3)
		}
		time.Sleep(10 * time.Second) // finite fallback if the test is interrupted
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestBrokerEscapedPipeFixture$", "--", "child", marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		os.Exit(4)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	os.Exit(5)
}

func TestBrokerCleanupFailureRefusesFurtherCallsAndStop(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "escaped.pid")
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "exec " + shellQuote(program) + " -test.run=^TestBrokerEscapedPipeFixture$ -- parent " + shellQuote(marker)
	plan := brokerBoundaryPlan(t, dir, script, "")
	channel, err := residenttools.Start(t.Context(), "", plan)
	if err != nil {
		t.Fatal(err)
	}
	client, host, err := residenttools.Attach(channel)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	t.Cleanup(func() {
		for _, path := range []string{marker, marker + ".first"} {
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			pid, err := strconv.Atoi(string(raw))
			if err == nil && pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	for attempt := 0; attempt < 2; attempt++ {
		id := fmt.Sprintf("call%d", attempt)
		_, err := host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: id, Tool: "check", Args: map[string]any{}, Mediation: api.MediationControllerMediated, IdempotencyKey: strings.Repeat("a", 24) + ":" + id})
		if err == nil {
			t.Fatal("incomplete command cleanup accepted")
		}
		if attempt == 0 {
			// If the second call reached the executable, it would replace this marker.
			if err := os.Rename(marker, marker+".first"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("command ran after cleanup uncertainty")
	}
	if err := os.Rename(marker+".first", marker); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err == nil {
		t.Fatal("Stop acknowledged incomplete command cleanup")
	}
	if err := client.Close(); err == nil {
		t.Fatal("repeated Stop lost cleanup uncertainty")
	}
}
