package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

func TestFlowBindingMidflightCancellation(t *testing.T) {
	a, inv, f, opt := flowBindingFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.change = func(string, int, map[string]any) error { cancel(); return context.Canceled }
	if _, err := a.prepareSuiteFlow(ctx, inv, opt); !errors.Is(err, context.Canceled) {
		t.Fatal("preflight hid cancellation", err)
	}
	a, inv, f, opt = flowBindingFixture(t)
	s, err := a.prepareSuiteFlow(t.Context(), inv, opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	f.change = func(string, int, map[string]any) error { cancel(); return context.Canceled }
	if err := s.revalidate(ctx, a); !errors.Is(err, context.Canceled) {
		t.Fatal("revalidation hid cancellation", err)
	}
}

func TestFlowBindingSelectedTargetInference(t *testing.T) {
	a, inv, f, opt := flowBindingFixture(t)
	name := filepath.Join(opt.Workspace, "kmx.yaml")
	writeFile(t, name, readFlowFile(t, name)+"  overall: {context: kind-test, namespace: orka-system, runtime: orka, inference: 'provider:other'}\n")
	opt.Destination = "overall"
	if _, err := a.prepareSuiteFlow(t.Context(), inv, opt); err == nil || f.calls != 0 {
		t.Fatal("selected inference was silently discarded", err, f.calls)
	}
}

func TestFlowBindingDecodedCredentialAtDecoder(t *testing.T) {
	_, _, _, opt := flowBindingFixture(t)
	value := "prefix\n" + strings.Repeat("a1b2", 8) + "\nsuffix"
	if secretshapes.Match(value) == nil {
		t.Fatal("synthetic decoded fixture must match")
	}
	raw := strings.Replace(readFlowFile(t, opt.Bindings), "placementRef: dev", `placementRef: "prefix\n`+strings.Repeat("a1b2", 8)+`\nsuffix"`, 1)
	if secretshapes.Match(raw) != nil {
		t.Fatal("fixture does not mask serialized boundary")
	}
	writeFile(t, opt.Bindings, raw)
	if _, err := readSuiteFlowBinding(t.Context(), opt.Bindings); !errors.Is(err, errSuiteFlow) {
		t.Fatal("decoded credential reached parsed binding", err)
	}
}
