package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(srv *httptest.Server) *Client { return &Client{base: srv.URL, http: srv.Client()} }

func TestRetiredToolRequestsFailBeforeTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("tool request reached transport")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := testClient(srv)
	if _, err := c.Request("agent", "tool", "delete"); err == nil {
		t.Fatal("tool request accepted")
	}
}

func TestBudgetRequestsStillFileWithoutArguments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/admin/requests" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["credential"] != "agent" || body["kind"] != "budget" || body["subject"] != "tokens" || len(body) != 3 {
			t.Errorf("body = %#v", body)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"deduped":true}`))
	}))
	defer srv.Close()
	c := testClient(srv)
	if deduped, err := c.Request("agent", "budget", "tokens"); err != nil || !deduped {
		t.Fatalf("budget request = %t, %v", deduped, err)
	}
}

func TestFlowAndWatchReadOnlyModelAndApprovalTrails(t *testing.T) {
	for _, watch := range []bool{false, true} {
		t.Run(map[bool]string{false: "flow", true: "watch"}[watch], func(t *testing.T) {
			reads := map[string]int{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads[r.URL.Path]++
				switch r.URL.Path {
				case "/admin/ledger":
					w.Write([]byte(`{"entries":[{"created_at":"2026-09-04T10:00:00Z","credential":"agent","model":"model-one","status":200,"cost_cents":1}]}`))
				case "/admin/approval-audit":
					w.Write([]byte(`{"entries":[{"created_at":"2026-09-04T10:00:01Z","credential":"agent","kind":"budget","subject":"tokens","action":"approved"}]}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c := testClient(srv)
			var out bytes.Buffer
			var err error
			if watch {
				err = c.Watch(&out, make(chan struct{}), WatchOptions{Replay: 2, For: time.Millisecond})
			} else {
				err = c.Flow(&out, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(reads) != 2 || reads["/admin/ledger"] == 0 || reads["/admin/approval-audit"] == 0 {
				t.Fatalf("trails = %v", reads)
			}
			if !strings.Contains(out.String(), "model-one") || !strings.Contains(out.String(), "budget:tokens") {
				t.Fatalf("missing trail: %s", &out)
			}
		})
	}
}
