package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"google.golang.org/grpc"
)

func TestTargetsOfflineDoesNotLoadConfigOrTools(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	for _, output := range []string{"table", "json"} {
		t.Run(output, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			if err := execute([]string{"targets", "-o", output}, deps); err != nil {
				t.Fatal(err)
			}
			if *loads != 0 || diagnostics.Len() != 0 {
				t.Fatalf("offline view touched operational configuration: loads=%d stderr=%s", *loads, &diagnostics)
			}
			if output == "table" {
				lines := strings.Split(out.String(), "\n")
				for i, want := range []string{"agentsessions", "kagent", "orka"} {
					if len(lines) <= i+1 {
						t.Fatalf("table row missing: %s", &out)
					}
					fields := strings.Fields(lines[i+1])
					if len(fields) == 0 || fields[0] != want {
						t.Fatalf("table order must be alphabetical: %s", &out)
					}
				}
				return
			}
			var report struct {
				SchemaVersion int `json:"schemaVersion"`
				Targets       []struct {
					ID, DisplayName, Kind, SupportTier string
					Capabilities                       []struct {
						Operation string
						Supported bool
						Reason    string
					}
					Detection struct{ State string }
				} `json:"targets"`
			}
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.SchemaVersion != 1 || len(report.Targets) != 3 {
				t.Fatalf("report=%s", &out)
			}
			for i, want := range []string{"agentsessions", "kagent", "orka"} {
				row := report.Targets[i]
				wantTier := []string{"eval-only", "create-only", "supported"}[i]
				if row.ID != want || row.SupportTier != wantTier || row.Detection.State != "not-probed" {
					t.Fatalf("row=%+v", row)
				}
				for _, capability := range row.Capabilities {
					if !capability.Supported && capability.Reason == "" {
						t.Fatalf("unexplained refusal: %+v", capability)
					}
					if want == "kagent" && capability.Supported && capability.Operation != "render" && capability.Operation != "deploy" {
						t.Fatalf("expanded Kagent support: %+v", capability)
					}
					if want == "agentsessions" && capability.Supported && capability.Operation != "evaluate" && capability.Operation != "verify" {
						t.Fatalf("expanded Sessions support: %+v", capability)
					}
				}
				if want == "agentsessions" && (row.Kind != "integration" || row.DisplayName != "agentsessions") {
					t.Fatalf("Sessions advertised as lifecycle adapter: %+v", row)
				}
			}
		})
	}
}

func TestTargetsInvalidFlagsFailBeforeConfig(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-o", "yaml"}, "output"},
		{[]string{"--sessions", " "}, "non-empty"},
		{[]string{"--sessions-ca", "ca.pem"}, "requires --sessions"},
		{[]string{"--sessions", "https://example.test"}, "InvalidArgument"},
		{[]string{"extra"}, "usage:"},
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		err := execute(append([]string{"targets"}, tc.args...), deps)
		if err == nil || !strings.Contains(err.Error(), tc.want) || *loads != 0 || out.Len() != 0 {
			t.Fatalf("%v: loads=%d out=%s err=%v", tc.args, *loads, &out, err)
		}
	}
}

type targetsSessionsServer struct{ v1.UnimplementedSessionsServer }

func (targetsSessionsServer) ListSessions(context.Context, *v1.ListSessionsRequest) (*v1.ListSessionsResponse, error) {
	return &v1.ListSessionsResponse{}, nil
}

func TestTargetsSessionsProbeSurvivesUnreadableKubeConfiguration(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	v1.RegisterSessionsServer(server, targetsSessionsServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	for _, detect := range []bool{false, true} {
		var out, diagnostics bytes.Buffer
		deps, _ := testDependencies(&out, &diagnostics)
		loads := 0
		deps.loadConfig = func(string, string) (*config.Config, error) {
			loads++
			return nil, errors.New("PRIVATE-CONFIG-PAYLOAD")
		}
		args := []string{"targets", "--sessions", listener.Addr().String(), "-o", "json"}
		if detect {
			args = append(args, "--detect")
		}
		err := execute(args, deps)
		var unreadable *app.TargetsUnreadableError
		if errors.As(err, &unreadable) != detect {
			t.Fatalf("detect=%t err=%v", detect, err)
		}
		if detect && loads != 1 || !detect && loads != 0 {
			t.Fatalf("detect=%t loads=%d", detect, loads)
		}
		var report app.TargetsReport
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if report.Targets[0].Detection.State != "present" || report.Targets[0].Detection.Version != "" {
			t.Fatalf("lost independent Sessions read or invented version: %s", &out)
		}
		if detect && (report.Targets[2].Detection.Error == nil || report.Targets[2].Detection.Error.Code != "configuration_unreadable") {
			t.Fatalf("configuration error became absence: %s", &out)
		}
		if strings.Contains(out.String()+diagnostics.String(), "PRIVATE-CONFIG-PAYLOAD") {
			t.Fatal("configuration payload leaked")
		}
	}
}

func TestTargetsHelpDoesNotLoadConfiguration(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"targets", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 || !strings.Contains(out.String(), "--detect") || !strings.Contains(out.String(), "--sessions") {
		t.Fatalf("loads=%d help=%s", *loads, &out)
	}
}
