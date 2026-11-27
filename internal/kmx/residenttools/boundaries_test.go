package residenttools

import (
	"bufio"
	"bytes"
	"net"
	"testing"
)

func TestRuntimeAdmissionRefusesUnimplementedAuthority(t *testing.T) {
	for _, change := range []func(*Plan){
		func(p *Plan) { p.Binding.CredentialRefs = []CredentialRef{{Name: "identity", Type: "env"}} },
		func(p *Plan) {
			p.Binding.AgentRuntime.Backend = AppleContainer
			p.Binding.AgentRuntime.Filesystem = FilesystemPolicy{Mode: "declared-mounts", WorkingDirectory: "/agent"}
			p.Binding.AgentRuntime.Network = NetworkPolicy{Mode: "isolated", Incoming: "none", Outgoing: "none"}
		},
		func(p *Plan) {
			p.Backend = RootlessOCI
			p.Binding.ToolRuntime.Backend = RootlessOCI
			p.Binding.ToolRuntime.Filesystem = FilesystemPolicy{Mode: "declared-mounts", WorkingDirectory: "/tools"}
			p.Binding.ToolRuntime.Network = NetworkPolicy{Mode: "isolated", Incoming: "none", Outgoing: "none"}
		},
	} {
		p := contractFixture(t)
		change(&p)
		if p.Revision() == "" {
			t.Fatal("planning declaration unexpectedly rejected")
		}
		if _, err := p.Freeze([]string{"check"}); err == nil {
			t.Fatal("unsupported policy admitted")
		}
	}
}

func TestPlanBindsSchemaAndCopiesMutableAuthority(t *testing.T) {
	p := contractFixture(t)
	copy, err := p.Freeze([]string{"check"})
	if err != nil {
		t.Fatal(err)
	}
	p.Contract.Tools[0].InputSchema["description"] = "changed"
	if copy.Revision() == "" {
		t.Fatal("caller mutated frozen plan")
	}
	if p.Revision() != "" {
		t.Fatal("contract diverged from command definitions")
	}
	p = contractFixture(t)
	p.Commands.Commands[0].Args = []string{"changed"}
	if p.Revision() != "" {
		t.Fatal("private binding drift ignored")
	}
	if _, err := copy.Freeze([]string{"other"}); err == nil {
		t.Fatal("wrong agent tools accepted")
	}
}

func TestDuplicateAndContradictoryBrokerFramesRefuse(t *testing.T) {
	var req request
	if readFrame(bufio.NewReader(bytes.NewBufferString("{\"op\":\"describe\",\"op\":\"execute\"}\n")), &req) == nil {
		t.Fatal("duplicate operation accepted")
	}
	client, server := net.Pipe()
	c := &Client{conn: client, read: bufio.NewReader(client)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		var req request
		_ = readFrame(bufio.NewReader(server), &req)
		_ = writeFrame(server, response{OK: true, Error: "cleanup uncertain"})
	}()
	if err := c.Close(); err == nil {
		t.Fatal("contradictory cleanup accepted")
	}
	if err := c.Close(); err == nil {
		t.Fatal("cleanup uncertainty erased on second close")
	}
	<-done
}
