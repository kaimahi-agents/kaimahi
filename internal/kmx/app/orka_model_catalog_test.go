package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// A refusal must keep the owned identity independently of untrusted catalog data.
func TestOrkaModelCatalogChoices(t *testing.T) {
	for _, vendor := range []string{"", "openai", "anthropic"} {
		for _, tc := range []struct{ name, body, code, choices string }{
			{"missing", `{"data":[{"id":"other","model_picker_enabled":false,"supported_endpoints":["/v1/messages"],"private":"private-upstream-canary"},{"id":"unknown"},{"id":"embedding","supported_endpoints":["/embeddings"]}]}`, "model_unavailable", `[{"id":"other","pickerEnabled":false,"chatSupported":true},{"id":"unknown"},{"id":"embedding","chatSupported":false}]`},
			{"empty", `{"data":[]}`, "model_unavailable", `[]`},
			{"picker disabled", `{"data":[{"id":"gpt-4.1","model_picker_enabled":false}]}`, "model_unavailable", `[{"id":"gpt-4.1","pickerEnabled":false}]`},
			{"nonchat", `{"data":[{"id":"gpt-4.1","supported_endpoints":["/embeddings"]}]}`, "model_unavailable", `[{"id":"gpt-4.1","chatSupported":false}]`},
			{"duplicate unselected", `{"data":[{"id":"gpt-4.1"},{"id":"other"},{"id":"other"}]}`, "dependency_unavailable", ""},
			{"malformed tail", `{"data":[{"id":"gpt-4.1"},{"id":"bad id"}]}`, "dependency_unavailable", ""},
			{"secret id", `{"data":[{"id":"` + "ghp_" + strings.Repeat("a", 36) + `"}]}`, "dependency_unavailable", ""},
			{"bad flag", `{"data":[{"id":"gpt-4.1","model_picker_enabled":"yes"}]}`, "dependency_unavailable", ""},
			{"bad endpoints", `{"data":[{"id":"gpt-4.1","supported_endpoints":[false]}]}`, "dependency_unavailable", ""},
			{"null row", `{"data":[null]}`, "dependency_unavailable", ""},
			{"null data", `{"data":null}`, "dependency_unavailable", ""},
			{"failed list", "", "dependency_unavailable", ""},
		} {
			t.Run(vendor+"/"+tc.name, func(t *testing.T) {
				a, _, _ := modelLifecycleFixture(t)
				capture := a.captureCommand
				a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					if strings.Contains(strings.Join(cmd.Args, " "), "/v1/models") {
						want := "/v1/models"
						if vendor == "anthropic" {
							want += "?limit=1000"
						}
						if !strings.HasSuffix(strings.Join(cmd.Args, " "), want) {
							t.Fatal("incorrect catalog query")
						}
						if tc.body == "" {
							return errors.New("private-upstream-canary")
						}
						_, err := fmt.Fprint(cmd.Stdout, tc.body)
						return err
					}
					return capture(ctx, cmd)
				}
				opt := modelConnectOptions()
				if vendor != "" {
					opt.Route, opt.Vendor = "key", vendor
				}
				result, err := a.ConnectOrkaModel(opt)
				var safe *OrkaModelError
				if !errors.As(err, &safe) || safe.Code() != tc.code {
					t.Fatalf("got %v, want %s", err, tc.code)
				}
				c := result.Connection
				if c == nil || !modelUUID(c.ID) || c.Route != opt.Route || c.Vendor != vendor || c.Provider != opt.Provider || c.Model != opt.Model || c.CredentialGeneration != opt.CredentialGeneration || c.State != "unknown" || result.RemoteState != "unknown" {
					t.Fatalf("lost cleanup identity: %+v", result)
				}
				raw, _ := json.Marshal(result)
				var projected map[string]json.RawMessage
				json.Unmarshal(raw, &projected)
				if string(projected["modelChoices"]) != tc.choices {
					t.Fatalf("choices=%s, want %s", projected["modelChoices"], tc.choices)
				}
				if strings.Contains(string(raw)+err.Error(), "private") || strings.Contains(string(raw), "ghp_") {
					t.Fatal("raw upstream data leaked")
				}
			})
		}
	}
}

func TestOrkaModelCatalogBoundsAndCapabilities(t *testing.T) {
	for _, n := range []int{1025, 4096, 4097} {
		rows := make([]string, n)
		for i := range rows {
			rows[i] = fmt.Sprintf(`{"id":"model-%d"}`, i)
		}
		choices, err := modelCatalogChoices([]byte(`{"data":[` + strings.Join(rows, ",") + `]}`))
		if (err == nil) != (n <= 4096) || err == nil && len(choices) != n {
			t.Fatalf("row ceiling %d: %v", n, err)
		}
	}
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"data":[{"id":"m","model_picker_enabled":null,"supported_endpoints":null}]}`, true},
		{`{"data":[{"id":"m","supported_endpoints":["/responses","/v1/responses","/chat/completions","/v1/chat/completions","/v1/messages"]}]}`, true},
		{`{"data":[{"id":"m","supported_endpoints":[]}]}`, true},
		{`{"data":[{"id":"m","supported_endpoints":["` + strings.Repeat("x", 4097) + `"]}]}`, false},
		{`{"data":[{"id":"m","supported_endpoints":[` + strings.Repeat(`"x",`, 64) + `"x"]}]}`, false},
	} {
		if _, err := modelCatalogChoices([]byte(tc.body)); (err == nil) != tc.valid {
			t.Fatalf("capabilities valid=%t: %v", tc.valid, err)
		}
	}
}

func TestOrkaModelLargeCatalog(t *testing.T) {
	// Upstream metadata can exceed the private checkpoint's 64 KiB limit.
	metadata := strings.Repeat("x", 70<<10)
	for _, tc := range []struct {
		name, body, want string
	}{
		{"advertised", `{"data":[{"id":"gpt-4.1","metadata":"` + metadata + `"}]}`, ""},
		{"missing", `{"data":[{"id":"other","metadata":"` + metadata + `"}]}`, "model_unavailable"},
		{"repeated", `{"data":[{"id":"gpt-4.1","metadata":"` + metadata + `"},{"id":"gpt-4.1"}]}`, "dependency_unavailable"},
		{"invalid identity", `{"data":[{"id":"gpt-4.1","metadata":"` + metadata + `"},{"id":"bad id"}]}`, "dependency_unavailable"},
		{"duplicate key", `{"data":[{"id":"gpt-4.1","metadata":"` + metadata + `","metadata":"other"}]}`, "dependency_unavailable"},
		{"trailing data", `{"data":[{"id":"gpt-4.1","metadata":"` + metadata + `"}]} {}`, "dependency_unavailable"},
		{"over response bound", `{"data":[{"id":"gpt-4.1","metadata":"` + strings.Repeat("x", 1<<20) + `"}]}`, "dependency_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := modelGatewayFixture(t)
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if strings.HasSuffix(strings.Join(cmd.Args, " "), "/v1/models") {
					_, err := fmt.Fprint(cmd.Stdout, tc.body)
					return err
				}
				return capture(ctx, cmd)
			}
			err := a.modelHealth(context.Background(), OrkaModelConnection{Provider: "copilot", Model: "gpt-4.1"})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("large advertised catalog refused: %v", err)
				}
			} else {
				var safe *OrkaModelError
				if !errors.As(err, &safe) || safe.Code() != tc.want {
					t.Fatalf("got %v, want %s", err, tc.want)
				}
			}
		})
	}
}
