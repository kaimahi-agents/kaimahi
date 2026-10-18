package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestReleaseLiftAdmissionAndCreateRaces(t *testing.T) {
	for _, tc := range []string{"admission spec", "admission ownership", "admission drift", "create collision", "create response", "cancel after create", "provider replaced before agent"} {
		t.Run(tc, func(t *testing.T) {
			a, f, opt, dir := releaseApplyFixture(t, "both")
			p, err := a.PrepareReleaseLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			capture := a.captureCommand
			triggered := false
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if !slices.Contains(cmd.Args, "create") || !slices.Contains(cmd.Args, "-f") {
					return capture(ctx, cmd)
				}
				raw, err := io.ReadAll(cmd.Stdin)
				if err != nil {
					return err
				}
				cmd.Stdin = bytes.NewReader(raw)
				var doc map[string]any
				if json.Unmarshal(raw, &doc) != nil {
					t.Fatal("invalid candidate")
				}
				dry := slices.Contains(cmd.Args, "--dry-run=server")
				kind := doc["kind"].(string)
				if kind != "Provider" && kind != "Agent" {
					return capture(ctx, cmd)
				}
				if tc == "create collision" && !dry && kind == "Provider" {
					triggered = true
					seedReconcile(t, dir, doc)
				}
				stdout := cmd.Stdout
				var response bytes.Buffer
				cmd.Stdout = &response
				err = capture(ctx, cmd)
				cmd.Stdout = stdout
				if err != nil {
					return err
				}
				var observed map[string]any
				if json.Unmarshal(response.Bytes(), &observed) != nil {
					t.Fatal("invalid server response")
				}
				switch {
				case tc == "admission spec" && dry && kind == "Agent":
					observed["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "mutated"}
					triggered = true
				case tc == "admission ownership" && dry && kind == "Agent":
					delete(nativeMetadata(observed)["annotations"].(map[string]any), suiteReservedKeys[0])
					triggered = true
				case tc == "admission drift" && dry && kind == "Agent":
					nativeMetadata(f.objects["namespace/orka-system"])["uid"] = "changed"
					triggered = true
				case tc == "create response" && !dry && kind == "Provider":
					observed["spec"].(map[string]any)["defaultModel"] = "different"
					triggered = true
				case tc == "cancel after create" && !dry && kind == "Provider":
					cancel()
					triggered = true
				case tc == "provider replaced before agent" && !dry && kind == "Provider":
					replacement, cloneErr := cloneOrkaDoc(observed)
					if cloneErr != nil {
						t.Fatal(cloneErr)
					}
					nativeMetadata(replacement)["uid"] = "foreign"
					seedReconcile(t, dir, replacement)
					triggered = true
				}
				return json.NewEncoder(stdout).Encode(observed)
			}
			result, err := a.ApplyReleaseLift(ctx, p, p.Summary().PlanDigest)
			if !triggered || err == nil {
				t.Fatalf("race not refused: %+v %v triggered=%t", result, err, triggered)
			}
			want := 0
			if strings.HasPrefix(tc, "create") || tc == "cancel after create" || tc == "provider replaced before agent" {
				want = 1
			}
			if len(persistentOrkaCalls(t, dir)) != want {
				t.Fatal("extra action or retry")
			}
			if want == 0 && result.State != "not-applied" {
				t.Fatal("admission refusal described as mutation")
			}
			if want == 1 && (result.State != "partial" || result.Resources[1].Outcome != "not-attempted") {
				t.Fatalf("bad partial: %+v", result)
			}
			if tc == "create collision" && (result.Resources[0].Outcome != "unknown" || result.Resources[0].UID != "") {
				t.Fatal("conditional collision invented an acknowledged placement")
			}
			if tc == "create response" && result.Resources[0].SpecDigest != "" {
				t.Fatal("unverified create response credited the reviewed spec")
			}
			if tc == "cancel after create" && (!errors.Is(err, context.Canceled) || result.Resources[0].Outcome != "created" || result.Resources[0].Ready || result.Resources[0].UID == "") {
				t.Fatalf("acknowledged but unready creation lost: %+v %v", result, err)
			}
		})
	}
}

func TestReleaseLiftRevalidationAndStatusChurn(t *testing.T) {
	a, _, opt, dir := releaseApplyFixture(t, "")
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"Provider", "Agent"} {
		path := dir + "/" + strings.ToLower(kind) + "s.core.orka.ai.json"
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if json.Unmarshal(raw, &doc) != nil {
			t.Fatal("bad fixture")
		}
		nativeMetadata(doc)["resourceVersion"] = "42"
		seedReconcile(t, dir, doc)
	}
	result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if len(persistentOrkaCalls(t, dir)) != 0 {
		t.Fatal("status churn wrote")
	}
	result.Summary.Target.Context = "forged"
	result.Resources[0].UID = "forged"
	if err := a.VerifyReleaseLiftPlacement(t.Context(), result); err != nil {
		t.Fatal("public copies altered private observation")
	}
	raw, err := os.ReadFile(dir + "/agents.core.orka.ai.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatal("bad fixture")
	}
	nativeMetadata(doc)["uid"] = "replacement"
	seedReconcile(t, dir, doc)
	if err := a.VerifyReleaseLiftPlacement(t.Context(), result); err == nil {
		t.Fatal("invocation verification accepted replacement")
	}
}
