package app

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func modelRequireOwnershipReason(t *testing.T, err error) {
	t.Helper()
	safe := modelFailure("execution_unknown", err)
	var typed *OrkaModelError
	if !errors.As(safe, &typed) || typed.Code() != "execution_unknown" || typed.DiagnosticReason() != "model_resource_ownership_changed" {
		t.Fatalf("ownership reason lost: %v (%v)", err, safe)
	}
	if typed.Error() != "The execution outcome is unknown." {
		t.Fatalf("public message changed: %s", typed.Error())
	}
}

func TestOrkaModelOwnershipReasonAtObservationBoundaries(t *testing.T) {
	for _, boundary := range []string{"read", "serving", "final-check"} {
		t.Run(boundary, func(t *testing.T) {
			a, _, roots := modelGatewayFixture(t)
			root, err := modelGatewayRoot(roots)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			// Establish the retained preflight before moving identity at the chosen boundary.
			for _, r := range roots {
				if _, exists, e := a.readModelObject(ctx, OrkaNamespace, r); e != nil || !exists {
					t.Fatalf("baseline read: %v", e)
				}
			}
			capture := a.captureCommand
			catalogSeen, changed := false, false
			fullAfterChange := 0
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				args := strings.Join(cmd.Args, " ")
				if changed && (strings.Contains(args, "--raw") || slices.Contains(cmd.Args, "--ignore-not-found=true") && slices.Contains(cmd.Args, "json")) {
					fullAfterChange++
				}
				if strings.HasSuffix(args, "/v1/models") {
					catalogSeen = true
				}
				if strings.Contains(args, "jsonpath={.metadata.uid}") && (boundary != "final-check" || catalogSeen) {
					changed = true
					fmt.Fprint(cmd.Stdout, "replaced-observation-uid")
					return nil
				}
				return capture(ctx, cmd)
			}
			switch boundary {
			case "read":
				_, _, err = a.readModelObject(ctx, OrkaNamespace, root)
			case "serving":
				_, err = a.modelServingGateway(ctx, root)
			case "final-check":
				_, err = a.modelCheckGateway(ctx, OrkaModelConnection{Provider: "copilot", Model: "gpt-4.1"}, roots)
			}
			if !changed || fullAfterChange != 0 || boundary == "final-check" && !catalogSeen {
				t.Fatalf("wrong boundary: changed%t fullAfter%d catalog%t", changed, fullAfterChange, catalogSeen)
			}
			modelRequireOwnershipReason(t, err)
		})
	}
}

func TestOrkaModelRetainedConnectKeepsOwnershipReason(t *testing.T) {
	a, _, _ := modelLifecycleFixture(t)
	if _, err := a.ConnectOrkaModel(modelConnectOptions()); err != nil {
		t.Fatal(err)
	}
	capture := a.captureCommand
	changed, mutations := false, 0
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		args := strings.Join(cmd.Args, " ")
		if strings.Contains(args, " create ") || strings.Contains(args, " delete ") || strings.Contains(args, " replace ") {
			mutations++
		}
		if strings.Contains(args, "jsonpath={.metadata.uid}") {
			changed = true
			fmt.Fprint(cmd.Stdout, "replaced-observation-uid")
			return nil
		}
		return capture(ctx, cmd)
	}
	result, err := a.ConnectOrkaModel(modelConnectOptions())
	if !changed || mutations != 0 || result.Connection == nil || result.Connection.State != "unknown" {
		t.Fatalf("unexpected reentry: %+v %v changed%t mutations%d", result, err, changed, mutations)
	}
	modelRequireOwnershipReason(t, err)
}
