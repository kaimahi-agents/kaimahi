package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestAttachedAgentUsesVersionAndDoesNotOwnLifecycle(t *testing.T) {
	a := mdFixture(t)
	lease, _, err := beginAgentInstance(a, "")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.finish(nil)
	dir, err := os.MkdirTemp("", "kmx-worker-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "rpc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	lease.record.Socket = socket
	if err = lease.save(); err != nil {
		t.Fatal(err)
	}
	requests := make(chan agentWorkerRequest, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			var request agentWorkerRequest
			e = json.NewDecoder(conn).Decode(&request)
			if e == nil {
				requests <- request
				_ = json.NewEncoder(conn).Encode(agentWorkerReply{Ready: true, Answer: "Hello"})
			}
			conn.Close()
		}
	}()
	defer func() { listener.Close(); <-done }()
	if err = os.Remove(a.Path); err != nil {
		t.Fatal(err)
	}
	s, cached, err := NewAttachedAgentSession(t.Context(), lease.record.ID, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cached.Revision != a.Revision {
		t.Fatal("wrong cached definition")
	}
	answer, err := s.AnswerAgent(context.Background(), cached, "hello")
	if err != nil || answer.Text != "Hello" {
		t.Fatalf("answer=%+v err=%v", answer, err)
	}
	s.Close()
	for i := 0; i < 2; i++ {
		request := <-requests
		if request.Version != a.Revision || request.Generation != 1 {
			t.Fatalf("unbound request: %+v", request)
		}
	}
	rows, err := ReadAgentInstances("")
	if err != nil || rows[0].State != "running" {
		t.Fatal("detach stopped worker")
	}
}
