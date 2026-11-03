package app

import (
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"strings"
	"testing"
)

func TestNativeContentAllowsCheckedBinarySource(t *testing.T) {
	t.Setenv("COPILOT_GITHUB_TOKEN", "fixture-private-credential")
	p := copilot.New(nil)
	access, _ := p.CopilotdClient(t.Context(), "ws://127.0.0.1:1", nil)
	if access == nil {
		t.Fatal("missing opaque credential checker")
	}
	defer access.Close(t.Context())
	a := nativeDisplayAccess{access}
	if err := a.CheckContent("\x00\xffbinary"); err != nil {
		t.Fatalf("binary source refused: %v", err)
	}
	if err := a.CheckContent("\x00fixture-private-credential\xff"); err == nil {
		t.Fatal("credential in binary accepted")
	}
	if err := a.CheckContent(strings.Repeat("a", (1<<20)-1) + "\xc3"); err != nil {
		t.Fatalf("partial UTF-8 stream refused: %v", err)
	}
	if err := a.CheckContent(strings.Repeat("a", 1<<20)); err != nil {
		t.Fatal(err)
	}
}
