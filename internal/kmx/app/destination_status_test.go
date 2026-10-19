package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const statusTestAPI = "/apis/core.orka.ai/v1alpha1"
const statusTestNS = "workloads"

func statusTestList(items ...map[string]any) string {
	if items == nil {
		items = []map[string]any{}
	}
	raw, _ := json.Marshal(map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "AgentList", "metadata": map[string]any{"resourceVersion": "10", "continue": ""}, "items": items})
	return string(raw)
}

func statusTestObject(kind, name string) map[string]any {
	return map[string]any{
		"apiVersion": "core.orka.ai/v1alpha1", "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": statusTestNS, "uid": name + "-uid", "resourceVersion": "10", "generation": 1},
		"spec":     map[string]any{},
		"status":   map[string]any{"ready": true, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1}}},
	}
}

func statusTestFixture(t *testing.T) (*App, map[string]string, *bytes.Buffer, *bytes.Buffer, *[]string) {
	t.Helper()
	a := captureTestApp()
	a.Cfg.KubeContext = "production"
	out, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	a.Out, a.Err = out, stderr
	paths := []string{}
	responses := map[string]string{
		"/apis":                              `{"apiVersion":"v1","kind":"APIGroupList","groups":[{"name":"core.orka.ai","versions":[{"groupVersion":"core.orka.ai/v1alpha1","version":"v1alpha1"}]}]}`,
		statusTestAPI:                        `{"apiVersion":"v1","kind":"APIResourceList","groupVersion":"core.orka.ai/v1alpha1","resources":[{"name":"agents","kind":"Agent","namespaced":true,"verbs":["get","list"]}]}`,
		"/api/v1/namespaces/kube-system":     `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"kube-system","uid":"cluster-uid"}}`,
		"/api/v1/namespaces/" + statusTestNS: `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"workloads","uid":"namespace-uid"}}`,
		statusTestAPI + "/namespaces/" + statusTestNS + "/agents?limit=128": statusTestList(),
	}
	a.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
		// Optional shared state is an exact GET, never a ConfigMap inventory.
		if slices.Equal(cmd.Args[1:], []string{"--context", "production", "--request-timeout=10s", "-n", statusTestNS, "get", "configmap", releaseStateName("release-notes", "greeter", "orka"), "--ignore-not-found", "-o", "json"}) {
			if raw, _ := io.ReadAll(cmd.Stdin); len(raw) != 0 {
				t.Fatal("GET received input")
			}
			return nil // Older placements have no shared release record.
		}
		want := []string{"--context", "production", "--request-timeout=10s", "get", "--raw"}
		if len(cmd.Args) != 7 || !slices.Equal(cmd.Args[1:6], want) {
			t.Fatalf("not an explicitly targeted GET-only request: %v", cmd.Args)
		}
		if raw, _ := io.ReadAll(cmd.Stdin); len(raw) != 0 {
			t.Fatal("GET received input")
		}
		path := cmd.Args[6]
		paths = append(paths, path)
		raw, ok := responses[path]
		if !ok {
			t.Fatalf("unexpected API path %q", path)
		}
		if strings.HasPrefix(raw, "ERROR:") {
			fmt.Fprint(cmd.Stderr, strings.TrimPrefix(raw, "ERROR:"))
			return errors.New("fixture denied request")
		}
		_, err := io.WriteString(cmd.Stdout, raw)
		return err
	}
	return a, responses, out, stderr, &paths
}

func statusTestRun(t *testing.T, a *App, out *bytes.Buffer) (destinationStatusReport, error) {
	t.Helper()
	err := a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, Output: "json"})
	var report destinationStatusReport
	decoder := json.NewDecoder(out)
	if decodeErr := decoder.Decode(&report); decodeErr != nil {
		t.Fatalf("no report: %v, operation=%v", decodeErr, err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		t.Fatal("stdout contains more than one JSON document")
	}
	return report, err
}

func statusTestAddAgent(t *testing.T, responses map[string]string, agent map[string]any) {
	t.Helper()
	name := agent["metadata"].(map[string]any)["name"].(string)
	agent["spec"].(map[string]any)["providerRef"] = map[string]any{"name": name, "namespace": statusTestNS}
	responses[statusTestAPI+"/namespaces/"+statusTestNS+"/agents?limit=128"] = statusTestList(agent)
	raw, _ := json.Marshal(agent)
	responses[statusTestAPI+"/namespaces/"+statusTestNS+"/agents/"+name] = string(raw)
	provider := statusTestObject("Provider", name)
	ann, _ := agent["metadata"].(map[string]any)["annotations"].(map[string]any)
	if ann[orkaSuiteMarkers[3]] == "kmx.native-orka/v2" {
		statusTestOwned(provider)
		if run, present := ann[suiteCandidateRunMarker]; present {
			provider["metadata"].(map[string]any)["annotations"].(map[string]any)[suiteCandidateRunMarker] = run
		}
	}
	raw, _ = json.Marshal(provider)
	responses[statusTestAPI+"/namespaces/"+statusTestNS+"/providers/"+name] = string(raw)
}

func TestDestinationStatusVerifiedEmptyIsGETOnlyAndCreatesNoState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state-must-not-exist")
	t.Setenv("KMX_HOME", home)
	a, _, out, stderr, paths := statusTestFixture(t)
	report, err := statusTestRun(t, a, out)
	if err != nil || !report.Complete || len(report.Agents) != 0 || len(report.Candidates) != 0 || report.Version != 1 {
		t.Fatalf("not verified empty: %+v %v", report, err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("inspection touched KMX_HOME: %v", err)
	}
	if len(*paths) == 0 || stderr.Len() != 0 {
		t.Fatalf("paths=%v stderr=%s", *paths, stderr)
	}
}

func TestDestinationStatusDiscoveryAndListFailuresAreUnknownNotAbsent(t *testing.T) {
	for _, path := range []string{"/apis", statusTestAPI, statusTestAPI + "/namespaces/" + statusTestNS + "/agents?limit=128", "/api/v1/namespaces/" + statusTestNS} {
		t.Run(path, func(t *testing.T) {
			a, responses, out, _, _ := statusTestFixture(t)
			responses[path] = "ERROR:Forbidden: confidential-response-must-not-escape"
			report, err := statusTestRun(t, a, out)
			if err == nil || report.Complete || len(report.Errors) == 0 || strings.Contains(fmt.Sprint(report, err), "confidential-response") {
				t.Fatalf("failure hidden or leaked: %+v %v", report, err)
			}
		})
	}
}

func TestDestinationStatusLaterPageFailurePreservesReadableRows(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	agent := statusTestObject("Agent", "foreign")
	statusTestAddAgent(t, responses, agent)
	first := statusTestList(agent)
	first = strings.Replace(first, `"continue":""`, `"continue":"next +/"`, 1)
	responses[statusTestAPI+"/namespaces/"+statusTestNS+"/agents?limit=128"] = first
	responses[statusTestAPI+"/namespaces/"+statusTestNS+"/agents?limit=128&continue=next+%2B%2F"] = "ERROR:Forbidden"
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Complete || len(report.Agents) != 1 || report.Agents[0].Name != "foreign" || report.Agents[0].Provenance.State != "not kmx-owned" {
		t.Fatalf("lost partial inventory or foreign distinction: %+v %v", report, err)
	}
}

func TestDestinationStatusMalformedDiscoveryCannotProveAbsence(t *testing.T) {
	for _, raw := range []string{`{}`, `{"kind":"APIGroupList","apiVersion":"v1","groups":null}`, `{"kind":"APIGroupList","apiVersion":"v1","groups":[],"groups":[]}`, `{"kind":"APIGroupList","apiVersion":"v1","groups":[]} {}`} {
		a, responses, out, _, _ := statusTestFixture(t)
		responses["/apis"] = raw
		report, err := statusTestRun(t, a, out)
		if err == nil || report.Complete {
			t.Fatalf("malformed discovery accepted: %s %+v", raw, report)
		}
	}
}
