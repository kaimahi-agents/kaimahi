package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

var liveKindContextPattern = regexp.MustCompile(`^kind-[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func liveKindContext(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > 253 || !liveKindContextPattern.MatchString(value) {
		return "", fmt.Errorf("KMX_LIVE_KIND_CONTEXT must name a kind- context")
	}
	return value, nil
}

// The opt-in test is not a cluster creator: kind naming alone does not
// authorize it to touch a destination. Inspect only pinned kubeconfig metadata.
func requireLiveLocalKind(ctx context.Context, a *App) error {
	raw, err := a.orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot inspect opt-in live test context: %w", err)
	}
	kube, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return fmt.Errorf("cannot parse opt-in live test context: %w", err)
	}
	if _, err := liftContextCluster(kube, a.Cfg.KubeContext); err != nil {
		return fmt.Errorf("opt-in live test requires an existing context: %w", err)
	}
	posture, err := guard.Classify(kube, a.Cfg.KubeContext)
	if err != nil {
		return fmt.Errorf("cannot classify opt-in live test context: %w", err)
	}
	ip := net.ParseIP(posture.Host)
	if !posture.Local || !(posture.Host == "localhost" || ip != nil && ip.IsLoopback()) {
		return fmt.Errorf("opt-in live test requires an existing local kind context with loopback API server")
	}
	return nil
}

func liveInstallQuickstartK8sTool(output io.Writer) error {
	contextName, err := liveKindContext(os.Getenv("KMX_LIVE_KIND_CONTEXT"))
	if err != nil {
		return err
	}
	if contextName == "" {
		return fmt.Errorf("KMX_LIVE_KIND_CONTEXT must be set for live installation")
	}
	app := &App{Cfg: &config.Config{KubeContext: contextName},
		Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard}, Out: output, Err: io.Discard}
	if err := requireLiveLocalKind(context.Background(), app); err != nil {
		return err
	}
	return app.installQuickstartK8sTool()
}

func assertQuickstartToolEvents(envelope map[string]json.RawMessage) error {
	var events []struct {
		Type     string `json:"type"`
		ToolName string `json:"toolName"`
	}
	if json.Unmarshal(envelope["events"], &events) != nil || events == nil {
		return fmt.Errorf("Task events response has no valid events array")
	}
	started, completed, failed := 0, 0, 0
	for _, event := range events {
		if event.ToolName != quickstartK8sTool {
			continue
		}
		switch event.Type {
		case "ToolCallStarted":
			started++
		case "ToolCallCompleted":
			completed++
		case "ToolCallFailed":
			failed++
		}
	}
	if started == 0 || completed == 0 || failed != 0 {
		return fmt.Errorf("Task Tool event projection: started=%d completed=%d failed=%d (want started and completed, no failure)", started, completed, failed)
	}
	return nil
}

func TestQuickstartToolEventsRequireAuthoritativeName(t *testing.T) {
	for _, tt := range []struct {
		name, events string
		valid        bool
	}{
		{"started and completed", `[{"type":"ToolCallStarted","toolName":"k8s-get-resources","payload":{"secret":"do-not-print"}},{"type":"ToolCallCompleted","toolName":"k8s-get-resources"}]`, true},
		{"missing top-level name", `[{"type":"ToolCallStarted","payload":{"toolName":"k8s-get-resources"}},{"type":"ToolCallCompleted","toolName":"k8s-get-resources"}]`, false},
		{"failed execution", `[{"type":"ToolCallStarted","toolName":"k8s-get-resources"},{"type":"ToolCallFailed","toolName":"k8s-get-resources"}]`, false},
		{"unrelated tool", `[{"type":"ToolCallStarted","toolName":"other"},{"type":"ToolCallCompleted","toolName":"other"}]`, false},
		{"malformed envelope", `null`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := assertQuickstartToolEvents(map[string]json.RawMessage{"events": json.RawMessage(tt.events)})
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%t, error=%v", tt.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), "do-not-print") {
				t.Fatal("event contents reached diagnostics")
			}
		})
	}
}

func TestLiveToolInstallContextGuard(t *testing.T) {
	for _, tt := range []struct {
		name, context         string
		wantSkip, wantRefusal bool
	}{
		{name: "unset", wantSkip: true},
		{name: "remote", context: "kmx-teams-w112", wantRefusal: true},
		{name: "empty kind name", context: "kind-", wantRefusal: true},
		{name: "malformed", context: "kind-demo/other", wantRefusal: true},
		{name: "valid", context: "kind-tool-ci"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := liveKindContext(tt.context)
			if tt.wantRefusal {
				if err == nil || got != "" {
					t.Fatalf("context=%q error=%v: expected refusal", got, err)
				}
				return
			}
			if err != nil || (got == "") != tt.wantSkip {
				t.Fatalf("context=%q error=%v: unexpected guard result", got, err)
			}
		})
	}
}

func TestLiveToolRequiresExistingLoopbackContext(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		allowed      bool
	}{
		{"local", `{"contexts":[{"name":"kind-ci","context":{"cluster":"local"}}],"clusters":[{"name":"local","cluster":{"server":"https://127.0.0.1:6443"}}]}`, true},
		{"remote kind name", `{"contexts":[{"name":"kind-ci","context":{"cluster":"aks"}}],"clusters":[{"name":"aks","cluster":{"server":"https://aks.example.invalid"}}]}`, false},
		{"missing context", `{"contexts":[],"clusters":[]}`, false},
		{"unspecified address", `{"contexts":[{"name":"kind-ci","context":{"cluster":"local"}}],"clusters":[{"name":"local","cluster":{"server":"https://0.0.0.0:6443"}}]}`, false},
		{"invalid config", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "other-kubectl")
			args := filepath.Join(dir, "args")
			fakeTool(t, dir, "kubectl", fmt.Sprintf(`printf '%%s\n' "$*" > %[1]q
if [ "$*" != '--context kind-ci --request-timeout=10s config view -o json' ]; then : > %[2]q; exit 1; fi
printf '%%s' %[3]q`, args, marker, tc.config))
			t.Setenv("PATH", dir)
			a := &App{Cfg: &config.Config{KubeContext: "kind-ci"}, Run: &run.Runner{}}
			err := requireLiveLocalKind(t.Context(), a)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%t, error=%v", tc.allowed, err)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatal("cluster command reached before validation")
			}
			got, readErr := os.ReadFile(args)
			if readErr != nil || strings.TrimSpace(string(got)) != "--context kind-ci --request-timeout=10s config view -o json" {
				t.Fatalf("config view not pinned: %q %v", got, readErr)
			}
		})
	}
}

func TestLiveToolInstallRefusesUnverifiedTarget(t *testing.T) {
	for _, tc := range []struct{ name, config string }{
		{"remote", `{"contexts":[{"name":"kind-ci","context":{"cluster":"aks"}}],"clusters":[{"name":"aks","cluster":{"server":"https://aks.example.invalid"}}]}`},
		{"missing", `{"contexts":[],"clusters":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "cluster-command")
			fakeTool(t, dir, "kubectl", fmt.Sprintf(`if [ "$*" = '--context kind-ci --request-timeout=10s config view -o json' ]; then printf '%%s' %[1]q; else : > %[2]q; exit 1; fi`, tc.config, marker))
			t.Setenv("PATH", dir)
			t.Setenv("KMX_LIVE_KIND_CONTEXT", "kind-ci")
			if err := liveInstallQuickstartK8sTool(io.Discard); err == nil {
				t.Fatal("unverified target accepted")
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatal("installer reached cluster before refusal")
			}
		})
	}
}

func TestLiveToolInstallRefusesInvalidContextBeforeKubectl(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "kubectl-invoked")
	fakeTool(t, dir, "kubectl", ": > "+shellArg(marker))
	t.Setenv("PATH", dir)
	t.Setenv("KMX_LIVE_KIND_CONTEXT", "kind-")
	var output bytes.Buffer
	err := liveInstallQuickstartK8sTool(&output)
	if err == nil || !strings.Contains(err.Error(), "kind-") {
		t.Fatalf("expected kind-context refusal, got %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("kubectl invoked for invalid context")
	}
}

// The Task API is read through the existing bounded, pinned result session.
// Only the expected Tool-call outcome, not event bodies, reaches test output.
func TestLiveInspectQuickstartToolEvents(t *testing.T) {
	contextName, err := liveKindContext(os.Getenv("KMX_LIVE_KIND_CONTEXT"))
	if err != nil {
		t.Fatal(err)
	}
	if contextName == "" {
		t.Skip("set KMX_LIVE_KIND_CONTEXT to the dedicated kind context to opt in")
	}
	name := os.Getenv("KMX_LIVE_TASK_NAME")
	if scaffold.ValidateObjectName(name) != nil {
		t.Fatal("KMX_LIVE_TASK_NAME must name the dedicated Task")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	app := &App{Cfg: &config.Config{KubeContext: contextName},
		Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard}, Out: io.Discard, Err: io.Discard}
	if err := requireLiveLocalKind(ctx, app); err != nil {
		t.Fatal(err)
	}
	session, err := app.openOrkaResultSession(ctx, CreateOptions{Namespace: OrkaNamespace,
		ResultServiceAccount: orkaResultAccount, ResultPort: "19187"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	status, envelope, err := session.getTaskResource(ctx, OrkaNamespace, name, "events", url.Values{"limit": {"100"}})
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Fatalf("Task events HTTP status: %d", status)
	}
	if err := assertQuickstartToolEvents(envelope); err != nil {
		t.Fatal(err)
	}
	t.Log("Task event projection: ToolCallStarted and ToolCallCompleted for k8s-get-resources; no ToolCallFailed")
}

func TestLiveProvisionOrkaResultReader(t *testing.T) {
	contextName, err := liveKindContext(os.Getenv("KMX_LIVE_KIND_CONTEXT"))
	if err != nil {
		t.Fatal(err)
	}
	if contextName == "" {
		t.Skip("set KMX_LIVE_KIND_CONTEXT to the dedicated kind context to opt in")
	}
	app := &App{Cfg: &config.Config{KubeContext: contextName},
		Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard}, Out: io.Discard, Err: io.Discard}
	if err := requireLiveLocalKind(t.Context(), app); err != nil {
		t.Fatal(err)
	}
	if err := app.orkaResultReader(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveInstallQuickstartK8sTool(t *testing.T) {
	if os.Getenv("KMX_LIVE_KIND_CONTEXT") == "" {
		t.Skip("set KMX_LIVE_KIND_CONTEXT to the dedicated kind context to opt in")
	}
	if err := liveInstallQuickstartK8sTool(&bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}
