package adapter

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigurationRejectsUnsafeDestinationsAndSecretReferences(t *testing.T) {
	for _, route := range []string{
		`{"url":"http://user:password@server/mcp","name":"tool"}`,
		`{"url":"http://server/mcp?token=secret","name":"tool"}`,
		`{"url":"http://server/mcp?","name":"tool"}`,
		`{"url":"http://server/mcp#fragment","name":"tool"}`,
		`{"url":"file:///etc/passwd","name":"tool"}`,
		`{"url":"http:///mcp","name":"tool"}`,
		`{"url":"http://server/mcp","name":""}`,
		`{"url":"http://server/mcp","name":"tool","headers":{"Authorization":"literal-secret"}}`,
		`{"url":"http://server/mcp","name":"tool","headers":{"Authorization":{"name":"../escape","key":"token"}}}`,
		`{"url":"http://server/mcp","name":"tool","headers":{"Authorization":{"name":"secret","key":"../escape"}}}`,
		`{"url":"http://server/mcp","name":"tool","headers":{"Authorization":{"name":"secret","key":".."}}}`,
		`{"url":"http://server/mcp","name":"tool","headers":{"Authorization":{"name":"secret","key":"..data"}}}`,
		`{"url":"http://server/mcp","name":"tool","headers":{"Authorization":{"name":"secret","key":"token","value":"literal"}}}`,
		`{"url":"http://server/mcp","name":"tool","headers":{"Mcp-Session-Id":{"name":"secret","key":"token"}}}`,
		`{"url":"http://server/mcp","name":"tool","headers":{"Host":{"name":"secret","key":"token"}}}`,
		`{"url":"http://server/mcp","name":"tool","headers":{"Idempotency-Key":{"name":"secret","key":"token"}}}`,
		`{"url":"http://server/mcp","name":"tool","headers":{"Bad Header":{"name":"secret","key":"token"}}}`,
		`{"url":"http://server/mcp","name":"tool","headers":{"Authorization":{"name":"secret","key":"token"},"authorization":{"name":"secret","key":"token"}}}`,
		`{"url":"http://server/mcp","url":"http://other/mcp","name":"tool"}`,
	} {
		t.Run(route, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "routes.json")
			if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"tools":{"shim-abc123":%s}}`, route)), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := New(path, t.TempDir()); err == nil {
				t.Fatal("accepted invalid route")
			}
		})
	}
	for _, data := range []string{`null`, `{}`, `{"tools":{}} {}`, `{"tools":{},"extra":true}`, `{"tools":{"../escape":{"url":"http://server/mcp","name":"tool"}}}`} {
		path := filepath.Join(t.TempDir(), "routes.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(path, t.TempDir()); err == nil {
			t.Errorf("accepted config %s", data)
		}
	}
}

func TestMissingInvalidAndEscapingSecretsFailBeforeNetwork(t *testing.T) {
	for _, mode := range []string{"missing", "newline", "empty", "too-large", "escape"} {
		t.Run(mode, func(t *testing.T) {
			s, dir := fixtureAdapter(t, "http://127.0.0.1:1/mcp", `{"Authorization":{"name":"auth","key":"token"}}`)
			if err := os.Mkdir(filepath.Join(dir, "auth"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "auth", "token")
			switch mode {
			case "newline":
				if err := os.WriteFile(path, []byte("sensitive\r\nInjected: yes"), 0600); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "too-large":
				if err := os.WriteFile(path, make([]byte, 65537), 0600); err != nil {
					t.Fatal(err)
				}
			case "escape":
				outside := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(outside, []byte("sensitive"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			}
			status, body := request(t, s, "POST", toolPath, `{}`)
			if status != 500 || body != "secret resolution failed\n" {
				t.Fatalf("status = %d, body = %q", status, body)
			}
		})
	}
}
