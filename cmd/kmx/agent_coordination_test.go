package main

import (
	"reflect"
	"testing"
)

func TestAgentCreateAcceptsRepeatableAllowedAgentFlag(t *testing.T) {
	cmd := newAgentCreateCommand(&commandState{})
	if err := cmd.Flags().Parse([]string{"--coordination", "--allowed-agent", "helper", "--allowed-agent", "reviewer"}); err != nil {
		t.Fatal(err)
	}
	names, err := cmd.Flags().GetStringArray("allowed-agent")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"helper", "reviewer"}) {
		t.Fatalf("allowed agent flags = %q", names)
	}
}
