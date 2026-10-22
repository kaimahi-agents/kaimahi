package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

// A package init can access D-Bus before command-level dependency injection.
// Exercise the actual binary against owned fixtures, never a host daemon.
func TestLinuxCLIStartupNoKeychainIO(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "kmx")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/kmx")
	build.Dir = repositoryRoot(t)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build kmx: %v\n%s", err, output)
	}
	launcher := "#!/bin/sh\nprintf called > \"$KMX_TEST_DBUS_MARKER\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(home, "dbus-launch"), []byte(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	for _, configured := range []bool{true, false} {
		mode := "no-session"
		if configured {
			mode = "configured"
		}
		for _, command := range []struct {
			name string
			args []string
		}{
			{"help", []string{"--help"}},
			{"completion", []string{"completion", "bash"}},
			{"local-list", []string{"model", "list", "-o", "json"}},
		} {
			t.Run(mode+"-"+command.name, func(t *testing.T) {
				marker := filepath.Join(home, mode+"-"+command.name+"-launch")
				env := []string{"PATH=" + home, "HOME=" + home, "XDG_CONFIG_HOME=" + home,
					"XDG_RUNTIME_DIR=" + home, "DISPLAY=:99", "KMX_TEST_DBUS_MARKER=" + marker}
				connected := make(chan struct{}, 1)
				var listener net.Listener
				var done chan struct{}
				if configured {
					path := filepath.Join(home, "bus")
					var err error
					listener, err = net.Listen("unix", path)
					if err != nil {
						t.Fatal(err)
					}
					defer listener.Close()
					done = make(chan struct{})
					go func() {
						defer close(done)
						for {
							conn, err := listener.Accept()
							if err != nil {
								return
							}
							select {
							case connected <- struct{}{}:
							default:
							}
							_ = conn.Close() // fail an unwanted handshake immediately
						}
					}()
					env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+path)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, command.args...)
				cmd.Dir, cmd.Env = home, env
				var output, diagnostic bytes.Buffer
				cmd.Stdout, cmd.Stderr = &output, &diagnostic
				err := cmd.Run()
				if listener != nil {
					_ = listener.Close()
					<-done
				}
				select {
				case <-connected:
					t.Error("inert CLI command connected to the credential session bus")
				default:
				}
				if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
					t.Errorf("inert CLI command invoked dbus-launch: stat=%v", statErr)
				}
				if command.name == "local-list" {
					// Discovery still probes the host's read-only Ollama
					// endpoint. A complete Failure report legitimately exits 1.
					var reports []modelaccess.Report
					if decodeErr := json.Unmarshal(output.Bytes(), &reports); decodeErr != nil || len(reports) != 2 {
						t.Fatalf("incomplete local discovery JSON: %v\n%s", decodeErr, output.String())
					}
					failure := false
					for _, report := range reports {
						if report.Provider == "" || report.Models == nil {
							t.Fatal("incomplete local provider report")
						}
						switch report.State {
						case modelaccess.Failure:
							failure = true
						case modelaccess.Absent, modelaccess.Empty, modelaccess.Installed:
						default:
							t.Fatalf("invalid local provider state: %q", report.State)
						}
					}
					if err != nil {
						exit, ok := err.(*exec.ExitError)
						if !failure || !ok || exit.ExitCode() != 1 {
							t.Fatalf("unexpected local list exit: %v\n%s", err, diagnostic.String())
						}
					} else if failure {
						t.Fatal("local discovery Failure report did not exit 1")
					}
				} else if err != nil {
					t.Fatalf("kmx %v: %v\n%s", command.args, err, diagnostic.String())
				}
			})
		}
	}
}
