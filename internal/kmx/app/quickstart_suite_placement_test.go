package app

import (
	"context"
	"os/exec"
	"reflect"
	"slices"
	"testing"
)

// Strict mode must refuse, never repair, an operator edit. General
// evaluation retains its separate, explicitly authorized update semantics.
func TestQuickstartSuitePlacementRefusesUpdates(t *testing.T) {
	for _, kind := range []string{"Provider", "Agent"} {
		for _, afterReview := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: " before", true: " after review"}[afterReview], func(t *testing.T) {
				a, _, p, dir := nativePlacementFixture(t)
				for _, k := range []string{"Provider", "Agent"} {
					seedReconcile(t, dir, nativeOwnedDoc(t, p, k))
				}
				drift := func() {
					doc := nativeOwnedDoc(t, p, kind)
					spec := doc["spec"].(map[string]any)
					if kind == "Agent" {
						spec["systemPrompt"] = map[string]any{"inline": "operator edit"}
					} else {
						spec["defaultModel"] = "operator-model"
					}
					seedReconcile(t, dir, doc)
				}
				reviewed := false
				if afterReview {
					capture := a.captureCommand
					a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
						err := capture(ctx, cmd)
						if err == nil && slices.Contains(cmd.Args, "config") && !reviewed {
							reviewed = true
							drift()
						}
						return err
					}
				} else {
					drift()
				}
				placement, err := a.placeSuiteNativeMode(t.Context(), p, true)
				if err == nil || placement != nil {
					t.Fatalf("operator edit accepted: placement=%v err=%v", placement, err)
				}
				if afterReview && !reviewed {
					t.Fatal("post-review drift was not introduced")
				}
				if writes := persistentOrkaCalls(t, dir); len(writes) != 0 {
					t.Fatalf("operator edit overwritten: %d writes", len(writes))
				}
			})
		}
	}
}

func TestQuickstartSuitePlacementCreatesAndReuses(t *testing.T) {
	a, _, p, dir := nativePlacementFixture(t)
	first, err := a.placeSuiteNativeMode(t.Context(), p, true)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	second, err := a.placeSuiteNativeMode(t.Context(), p, true)
	if err != nil || second == nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("exact rerun changed placement: %v", err)
	}
	if len(persistentOrkaCalls(t, dir)) != 2 {
		t.Fatal("exact rerun wrote again")
	}
}
