//go:build !windows

package residenttools

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestBrokerProtocolBoundsAndRequestIdentity(t *testing.T) {
	for value, want := range map[string]bool{
		strings.Repeat("a", 24): true,
		strings.Repeat("A", 24): false,
		strings.Repeat("a", 23): false,
		strings.Repeat("g", 24): false,
	} {
		if got := validRequestID(value); got != want {
			t.Fatalf("validRequestID(%q)=%v want %v", value, got, want)
		}
	}
	var encoded bytes.Buffer
	if err := writeFrame(&encoded, request{Op: "describe"}); err != nil {
		t.Fatal(err)
	}
	var decoded request
	if err := readFrame(bufio.NewReaderSize(&encoded, brokerLimit+1), &decoded); err != nil || decoded.Op != "describe" {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	oversized := bytes.NewBufferString(strings.Repeat("x", brokerLimit) + "\n")
	if err := readFrame(bufio.NewReaderSize(oversized, brokerLimit+1), &decoded); err == nil {
		t.Fatal("oversized broker frame accepted")
	}
}
