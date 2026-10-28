package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func splitResultController(t *testing.T, args []string) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"items": []any{map[string]any{
		"metadata": map[string]any{
			"name": "orka-controller", "namespace": OrkaNamespace, "uid": "controller-uid",
			"labels": map[string]string{"app.kubernetes.io/name": "orka", "app.kubernetes.io/component": "controller"},
		},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "controller", "args": args}},
		}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const splitResultService = `{"items":[{"metadata":{"name":"orka","labels":{"app.kubernetes.io/name":"orka"}},"spec":{"selector":{"app.kubernetes.io/name":"orka","app.kubernetes.io/component":"controller"},"ports":[{"name":"api","port":8080}]}}]}`

func TestQuickstartResultUsesVerifiedControllerRoute(t *testing.T) {
	for _, watch := range []string{"tasks", "other"} {
		t.Run(watch, func(t *testing.T) {
			f := newEvalFixture(t, nil)
			t.Setenv("KMX_EVAL_SERVICES", `{"items":[]}`)
			t.Setenv("KMX_EVAL_MANAGEMENT_SERVICES", splitResultService)
			t.Setenv("KMX_EVAL_MANAGEMENT_DEPLOYMENTS", splitResultController(t, []string{"--watch-namespace=" + watch}))
			f.app.orkaSetup = &preparedOrkaSetup{spec: orkaInstallSpec{taskNamespace: "tasks"}}
			opt := f.app.quickstartCreateOptions()
			namespace, service, err := f.app.orkaResultAPI(t.Context(), opt.Namespace, opt.OrkaAPIService)
			if watch != "tasks" {
				if err == nil {
					t.Fatal("quickstart accepted an API route without a matching controller")
				}
				return
			}
			if err != nil || namespace != OrkaNamespace || service != "orka" {
				t.Fatalf("quickstart did not discover the proven management API: namespace=%q service=%q err=%v", namespace, service, err)
			}
		})
	}
}

func TestSuiteNativeResultPreflightUsesProvenManagementRoute(t *testing.T) {
	for _, name := range []string{"matching", "different watch", "missing management API"} {
		t.Run(name, func(t *testing.T) {
			f := newEvalFixture(t, nil)
			t.Setenv("KMX_EVAL_SERVICES", `{"items":[]}`)
			t.Setenv("KMX_EVAL_MANAGEMENT_SERVICES", splitResultService)
			watch := "tasks"
			if name == "different watch" {
				watch = "other"
			}
			if name == "missing management API" {
				t.Setenv("KMX_EVAL_MANAGEMENT_SERVICES", `{"items":[]}`)
			}
			t.Setenv("KMX_EVAL_MANAGEMENT_DEPLOYMENTS", splitResultController(t, []string{"--watch-namespace=" + watch}))
			err := f.app.preflightSuiteNativeResults(t.Context(), "tasks")
			if (err == nil) != (name == "matching") {
				t.Fatalf("split result preflight: %v", err)
			}
		})
	}
}

func TestOrkaSplitResultKeepsTenantPrincipalAndManagementForward(t *testing.T) {
	f := newEvalFixture(t, nil)
	t.Setenv("KMX_EVAL_SERVICES", `{"items":[]}`)
	t.Setenv("KMX_EVAL_MANAGEMENT_SERVICES", splitResultService)
	t.Setenv("KMX_EVAL_MANAGEMENT_DEPLOYMENTS", splitResultController(t, []string{"--api-port=8080", "--watch-namespace=tasks"}))
	requests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Query().Get("namespace")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
	}))
	defer server.Close()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	session, err := f.app.openOrkaResultSession(ctx, CreateOptions{
		Namespace: "tasks", ResultServiceAccount: "reader", ResultPort: port,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	if err := session.probe(ctx, "tasks", "absent-task"); err != nil {
		t.Fatal(err)
	}
	if namespace := <-requests; namespace != "tasks" {
		t.Fatalf("Task request used namespace %q", namespace)
	}
	accountRead, tokenMint, serviceRead, forwarded := false, false, false, false
	for _, call := range orkaCalls(t, f.dir) {
		i := slices.Index(call.Args, "-n")
		if i < 0 || i+1 >= len(call.Args) {
			continue
		}
		namespace := call.Args[i+1]
		switch {
		case slices.Contains(call.Args, "serviceaccount"):
			accountRead = true
			if namespace != "tasks" {
				t.Fatal("result ServiceAccount read outside Task namespace")
			}
		case slices.Contains(call.Args, "token"):
			tokenMint = true
			if namespace != "tasks" {
				t.Fatal("result token minted outside Task namespace")
			}
		case slices.Contains(call.Args, "service"):
			serviceRead = true
			if namespace != OrkaNamespace {
				t.Fatal("API Service read outside management namespace")
			}
		case slices.Contains(call.Args, "port-forward"):
			forwarded = true
			if namespace != OrkaNamespace || !slices.Contains(call.Args, "svc/orka") {
				t.Fatal("forward did not select the management API Service")
			}
		}
	}
	if !accountRead || !tokenMint || !serviceRead || !forwarded {
		t.Fatalf("missing operation: account=%t token=%t service=%t forward=%t", accountRead, tokenMint, serviceRead, forwarded)
	}
}

func TestOrkaSplitResultRefusesUnprovenControllerBeforeToken(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"different namespace", []string{"--watch-namespace=other"}},
		{"missing namespace", []string{"--api-port=8080"}},
		{"duplicate namespace", []string{"--watch-namespace=tasks", "--watch-namespace=tasks"}},
		{"split argument", []string{"--watch-namespace", "tasks"}},
		{"positional argument", []string{"start", "--watch-namespace=tasks"}},
		{"flag terminator", []string{"--", "--watch-namespace=tasks"}},
		{"single dash alias", []string{"--watch-namespace=tasks", "-watch-namespace=other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvalFixture(t, nil)
			t.Setenv("KMX_EVAL_SERVICES", `{"items":[]}`)
			t.Setenv("KMX_EVAL_MANAGEMENT_SERVICES", splitResultService)
			t.Setenv("KMX_EVAL_MANAGEMENT_DEPLOYMENTS", splitResultController(t, tc.args))
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			session, err := f.app.openOrkaResultSession(ctx, CreateOptions{Namespace: "tasks", ResultServiceAccount: "reader"})
			if session != nil {
				session.close()
				t.Fatal("opened result session without a matching controller")
			}
			if err == nil {
				t.Fatal("accepted unproven controller routing")
			}
			for _, call := range orkaCalls(t, f.dir) {
				if slices.Contains(call.Args, "token") || slices.Contains(call.Args, "port-forward") {
					t.Fatal("minted token or opened forward after controller refusal")
				}
			}
		})
	}
}

func TestOrkaResultDiscoveryDoesNotMaskLocalErrors(t *testing.T) {
	var services struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(splitResultService), &services); err != nil {
		t.Fatal(err)
	}
	ambiguous := fmt.Sprintf(`{"items":[%s,%s]}`, services.Items[0], services.Items[0])
	for _, tc := range []struct{ name, response string }{
		{"malformed response", `not-json`},
		{"ambiguous response", ambiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvalFixture(t, nil)
			t.Setenv("KMX_EVAL_SERVICES", tc.response)
			t.Setenv("KMX_EVAL_MANAGEMENT_SERVICES", splitResultService)
			t.Setenv("KMX_EVAL_MANAGEMENT_DEPLOYMENTS", splitResultController(t, []string{"--watch-namespace=tasks"}))
			if _, _, err := f.app.orkaResultAPI(t.Context(), "tasks", ""); err == nil {
				t.Fatal("masked invalid or ambiguous local discovery")
			}
			for _, call := range orkaCalls(t, f.dir) {
				if slices.Contains(call.Args, "deploy") || slices.Contains(call.Args, "token") || slices.Contains(call.Args, "port-forward") {
					t.Fatal("used management fallback after a local discovery error")
				}
			}

		})
	}
}

func TestOrkaLocalResultDiscoveryNeedsNoManagementAccess(t *testing.T) {
	f := newEvalFixture(t, nil)
	t.Setenv("KMX_EVAL_SERVICES", splitResultService)
	t.Setenv("KMX_EVAL_MANAGEMENT_DEPLOYMENTS", `not-json`)
	namespace, service, err := f.app.orkaResultAPI(t.Context(), "custom-colocated", "")
	if err != nil || namespace != "custom-colocated" || service != "orka" {
		t.Fatalf("namespace=%q service=%q error=%v", namespace, service, err)
	}
	for _, call := range orkaCalls(t, f.dir) {
		if slices.Contains(call.Args, "deploy") {
			t.Fatal("colocated discovery accessed management Deployments")
		}
	}
}

func TestOrkaSplitResultRejectsInvalidManagementIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*orkaDeployment)
	}{
		{"missing UID", func(d *orkaDeployment) { d.Metadata.UID = "" }},
		{"wrong namespace", func(d *orkaDeployment) { d.Metadata.Namespace = "other" }},
		{"terminating", func(d *orkaDeployment) {
			when := "2026-10-25T19:00:00Z"
			d.Metadata.DeletionTimestamp = &when
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvalFixture(t, nil)
			t.Setenv("KMX_EVAL_SERVICES", `{"items":[]}`)
			var list struct {
				Items []orkaDeployment `json:"items"`
			}
			if err := json.Unmarshal([]byte(splitResultController(t, []string{"--watch-namespace=tasks"})), &list); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&list.Items[0])
			raw, err := json.Marshal(list)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("KMX_EVAL_MANAGEMENT_DEPLOYMENTS", string(raw))
			if _, _, err := f.app.orkaResultAPI(t.Context(), "tasks", ""); err == nil {
				t.Fatal("selected invalid management identity")
			}
		})
	}
}
