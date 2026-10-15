package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

type evalFixture struct {
	app                              *App
	opt                              struct{ ResultPort string }
	dir                              string
	name, digest                     string
	out                              *bytes.Buffer
	nativeReads                      *nativeReadFixture
	answers                          map[string]string
	resultStatus                     int
	resultDisconnect, resultHoldBody bool
	resultHeldBodies                 atomic.Int32
	resultAddress                    string
	resultRequest                    func(*http.Request)
}

// Seed a raw live Agent and serve attributable Task results keyed by prompt.
func newEvalFixture(t *testing.T, cases map[string]string) *evalFixture {
	t.Helper()
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", kubectlFixture(t, "TestEvalKubectlHelper"))
	t.Setenv("PATH", dir)
	t.Setenv("KMX_EVAL_DIR", dir)
	t.Setenv("KMX_HOME", filepath.Join(dir, "user-state"))
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	out := &bytes.Buffer{}
	f := &evalFixture{
		app: &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Out: out, Err: &bytes.Buffer{}, Run: &run.Runner{Stdout: out, Stderr: &bytes.Buffer{}}, waitTiming: fastWaitTiming()},
		opt: struct{ ResultPort string }{},
		dir: dir, name: "sample", digest: strings.Repeat("a", 64), out: out,
		answers: map[string]string{}, resultStatus: http.StatusOK,
	}
	f.seedAgent(t, f.digest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		task := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/"), "/result")
		raw, err := os.ReadFile(filepath.Join(dir, "task-"+task+".json"))
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
			return
		}
		if f.resultRequest != nil {
			f.resultRequest(r)
		}
		if f.resultDisconnect {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		if f.resultHoldBody {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			f.resultHeldBodies.Add(1)
			<-r.Context().Done()
			return
		}
		if f.resultStatus != http.StatusOK {
			w.WriteHeader(f.resultStatus)
			fmt.Fprint(w, `{"error":{"code":500,"message":"broken"}}`)
			return
		}
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		prompt := doc["spec"].(map[string]any)["prompt"].(string)
		_ = json.NewEncoder(w).Encode(map[string]string{"result": f.answers[prompt]})
	}))
	t.Cleanup(server.Close)
	f.resultAddress = server.Listener.Addr().String()
	_, f.opt.ResultPort, _ = net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	return f
}

func (f *evalFixture) seedAgent(t *testing.T, digest string) {
	t.Helper()
	agent := map[string]any{
		"kind": "Agent",
		"metadata": map[string]any{"name": f.name, "namespace": "orka-system", "uid": "agent-uid", "generation": 1,
			"annotations": map[string]any{orkaBundleMarker: f.name, orkaPortableMarker: digest, orkaRenderedMarker: strings.Repeat("b", 64)}},
		"status": map[string]any{"ready": true, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1}}},
	}
	raw, _ := json.Marshal(agent)
	if err := os.WriteFile(filepath.Join(f.dir, "agent.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func (f *evalFixture) taskCreates(t *testing.T) int {
	t.Helper()
	count := 0
	for _, call := range orkaCalls(t, f.dir) {
		if call.Document != nil && call.Document["kind"] == "Task" && slices.Contains(call.Args, "create") {
			count++
		}
	}
	return count
}

func TestOrkaAPIServiceDiscoveryRefusesAbsentAndAmbiguousMatches(t *testing.T) {
	const chart = `{"metadata":{"name":"orka","labels":{"app.kubernetes.io/name":"orka"}},"spec":{"selector":{"app.kubernetes.io/name":"orka","app.kubernetes.io/component":"controller"},"ports":[{"name":"api","port":8080}]}}`
	const metrics = `{"metadata":{"name":"orka-metrics","labels":{"app.kubernetes.io/name":"orka","control-plane":"controller-manager"}},"spec":{"selector":{"app.kubernetes.io/name":"orka","control-plane":"controller-manager"},"ports":[{"name":"https","port":8443}]}}`
	const legacy = `{"metadata":{"name":"orka-api","labels":{"app.kubernetes.io/name":"orka","control-plane":"controller-manager"}},"spec":{"selector":{"app.kubernetes.io/name":"orka","control-plane":"controller-manager"},"ports":[{"name":"http","port":8080}]}}`
	for _, tc := range []struct{ name, services, want string }{
		{"legacy with metrics", `{"items":[` + metrics + `,` + legacy + `]}`, "orka-api"},
		{"chart", `{"items":[` + chart + `]}`, "orka"},
		{"absent", `{"items":[` + metrics + `]}`, "no Orka API Service"},
		{"ambiguous", `{"items":[` + chart + `,` + legacy + `]}`, "multiple Orka API Services"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvalFixture(t, nil)
			t.Setenv("KMX_EVAL_SERVICES", tc.services)
			got, err := f.app.orkaAPIService(t.Context(), "orka-system")
			if tc.name == "absent" || tc.name == "ambiguous" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got service=%q error=%v; want %s", got, err, tc.want)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("got service=%q error=%v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestWizardTaskFindsStockHelmReleaseAPIService(t *testing.T) {
	f := newEvalFixture(t, nil)
	f.answers["Say hello."] = "hello from Orka"
	t.Setenv("KMX_EVAL_STOCK_RELEASE", "1")
	opt := quickstartResultOptions("orka-system", "Say hello.")
	if opt.OrkaAPIService != "" {
		t.Fatal("wizard pinned a release-specific Service")
	}
	opt.ResultPort = f.opt.ResultPort // the fixture's HTTP server uses a free port
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	session, err := f.app.openOrkaResultSession(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	answer, err := f.app.runQuickstartOrkaTaskProfile(ctx, f.name, "orka-system", opt.Task, nil, nil, session)
	if err != nil || answer != "hello from Orka" {
		t.Fatalf("wizard task on stock release: answer=%q err=%v", answer, err)
	}
	var forwarded bool
	for _, c := range orkaCalls(t, f.dir) {
		forwarded = forwarded || slices.Contains(c.Args, "port-forward") && slices.Contains(c.Args, "svc/orka")
	}
	if !forwarded {
		t.Fatal("wizard result did not forward through stock chart API Service")
	}
}
