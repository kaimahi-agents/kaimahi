package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// postTo is post() with the path spelled out, for the routes that differ
// only in their trailing slash.
func postTo(h http.Handler, token, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		path, strings.NewReader(string(body)))
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// initResult unwraps a relayed initialize response.
func initResult(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var rpc struct {
		JSONRPC string                     `json:"jsonrpc"`
		ID      json.RawMessage            `json:"id"`
		Result  map[string]json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rpc), "body: %s", rec.Body.String())
	assert.Equal(t, "2.0", rpc.JSONRPC)
	assert.Equal(t, "1", string(rpc.ID))
	return rpc.Result
}

// A full FastMCP-shaped advertisement: the framing this project met in the
// wild offers four capabilities the gateway does not relay.
const advertisedAll = `{"tools":{"listChanged":true},"prompts":{"listChanged":true},` +
	`"resources":{"subscribe":true,"listChanged":true},"logging":{},"completions":{}}`

func initializeBody(caps string) string {
	return `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26",` +
		`"capabilities":` + caps + `,"serverInfo":{"name":"fastmcp","version":"2.11"}}}`
}

// The defect this closes: the gateway relayed an advertisement it then
// refused, and a client that guards its calls on it died at startup.
func TestInitializeCapabilitiesProjectedToTools(t *testing.T) {
	body := initializeBody(advertisedAll)
	for name, respond := range map[string]http.HandlerFunc{
		"json": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Mcp-Session-Id", "s-1")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		},
		"sse": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Mcp-Session-Id", "s-1")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: " + body + "\n\n"))
		},
		// The space after "data:" is optional per the SSE format.
		"sse-no-space": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Mcp-Session-Id", "s-1")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata:" + body + "\n\n"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			up := httptest.NewServer(respond)
			defer up.Close()
			fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
			h := newGateway(t, fs, up)

			rec := post(h, goodToken, rpc(t, "initialize", map[string]any{"protocolVersion": "2025-03-26"}))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			result := initResult(t, rec)

			assert.JSONEq(t, `{"tools":{}}`, string(result["capabilities"]),
				"only tools is relayed, and nothing under it: listChanged promises a "+
					"server-initiated stream this gateway does not offer")
			assert.JSONEq(t, `{"name":"fastmcp","version":"2.11"}`, string(result["serverInfo"]),
				"the rest of the handshake is the upstream's")
			assert.JSONEq(t, `"2025-03-26"`, string(result["protocolVersion"]))
			assert.Equal(t, "s-1", rec.Header().Get("Mcp-Session-Id"), "session header must relay back")
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"),
				"a projected answer is JSON whatever framing it arrived in")
			assert.Empty(t, fs.audits, "successful lifecycle relays are not audited")
		})
	}
}

// An upstream that advertises no tools has nothing the gateway relays, and
// the client is told exactly that rather than being offered prompts.
func TestInitializeWithNoToolsCapabilityAdvertisesNothing(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(initializeBody(`{"prompts":{},"resources":{}}`)))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	rec := post(h, goodToken, rpc(t, "initialize", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{}`, string(initResult(t, rec)["capabilities"]))
}

// No capabilities member is not the same as an empty one, and the gateway
// does not invent a key the upstream did not send.
func TestInitializeWithoutCapabilitiesKeyIsUntouched(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"fake"}}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	rec := post(h, goodToken, rpc(t, "initialize", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	result := initResult(t, rec)
	_, present := result["capabilities"]
	assert.False(t, present, "no capabilities member was sent, so none is relayed")
	assert.JSONEq(t, `{"name":"fake"}`, string(result["serverInfo"]))
}

// The bound, and what happens at it: a refusal, not a handshake with fields
// quietly missing.
func TestOversizedInitializeIsRefusedNotTruncated(t *testing.T) {
	huge := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26",` +
		`"capabilities":` + advertisedAll + `,"instructions":"` +
		strings.Repeat("x", maxInitializeResp) + `"}}`
	require.Greater(t, len(huge), maxInitializeResp)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(huge))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	rec := post(h, goodToken, rpc(t, "initialize", nil))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), MsgInitializeTooLarge)
	assert.NotContains(t, rec.Body.String(), "protocolVersion",
		"a refusal, not the head of the response the client asked for")
	assert.Less(t, rec.Body.Len(), 1024, "the oversized body is not relayed at all")
}

// A handshake the gateway cannot read is one it cannot project, and an
// unprojected advertisement is the defect this lane closed.
func TestUnparseableInitializeFailsClosed(t *testing.T) {
	for name, body := range map[string]string{
		"not json":          "<html>a proxy error page</html>",
		"no jsonrpc member": `{"id":1,"result":{"capabilities":{"prompts":{}}}}`,
		"sse with no data":  "event: message\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(body, "event:") {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				_, _ = w.Write([]byte(body))
			}))
			defer up.Close()
			fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
			h := newGateway(t, fs, up)

			rec := post(h, goodToken, rpc(t, "initialize", nil))
			assert.Equal(t, http.StatusBadGateway, rec.Code)
			assert.Contains(t, rec.Body.String(), MsgInitializeUnprojectable)
		})
	}
}

// A capabilities member that is not an object cannot be projected, and an
// unprojected advertisement is the thing this closes.
func TestInitializeWithUnreadableCapabilitiesFailsClosed(t *testing.T) {
	for name, caps := range map[string]string{
		"a string": `"everything"`,
		"an array": `["tools","prompts"]`,
		"a number": `7`,
	} {
		t.Run(name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(initializeBody(caps)))
			}))
			defer up.Close()
			fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
			h := newGateway(t, fs, up)

			rec := post(h, goodToken, rpc(t, "initialize", nil))
			assert.Equal(t, http.StatusBadGateway, rec.Code)
			assert.Contains(t, rec.Body.String(), MsgInitializeUnprojectable)
		})
	}
	t.Run("null projects to nothing offered", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(initializeBody(`null`)))
		}))
		defer up.Close()
		fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
		h := newGateway(t, fs, up)

		rec := post(h, goodToken, rpc(t, "initialize", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{}`, string(initResult(t, rec)["capabilities"]))
	})
}

// An SSE body may carry more than one frame. The handshake is the frame
// answering THIS request, not whichever arrived last.
func TestInitializeSSEPicksTheFrameAnsweringTheRequest(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: message\ndata: " + initializeBody(advertisedAll) + "\n\n" +
				"event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"," +
				"\"params\":{\"level\":\"info\"}}\n\n"))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	rec := post(h, goodToken, rpc(t, "initialize", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"tools":{}}`, string(initResult(t, rec)["capabilities"]))
}

// JSON-RPC gives `"id": null` to an error about a message the server
// could not read. A handshake sent with a null id must not be answered by
// picking that frame up and relaying it as a successful handshake.
func TestInitializeSSEWithANullIDMatchesNoFrame(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: " +
			`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}` + "\n\n"))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	rec := post(h, goodToken,
		[]byte(`{"jsonrpc":"2.0","id":null,"method":"initialize","params":{}}`))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), MsgInitializeUnprojectable)
}

// A second frame claiming the same id is a protocol violation. It must not
// be able to overwrite the answer the first one already gave.
func TestInitializeSSETakesTheFirstAnsweringFrame(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: message\ndata: " + initializeBody(advertisedAll) + "\n\n" +
				"event: message\ndata: " +
				`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"prompts":{}},` +
				`"serverInfo":{"name":"second"}}}` + "\n\n"))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	rec := post(h, goodToken, rpc(t, "initialize", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	result := initResult(t, rec)
	assert.JSONEq(t, `{"tools":{}}`, string(result["capabilities"]))
	assert.JSONEq(t, `{"name":"fastmcp","version":"2.11"}`, string(result["serverInfo"]),
		"the second frame must not displace the first")
}

// The request path refuses a duplicated JSON key rather than collapsing
// it. The response path is where a projection reads, so it refuses too:
// Go merges a repeated `result` into one map, and a projection that read
// the first would hand the client fields from the second.
func TestInitializeWithDuplicateResultMembersIsRefused(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,` +
			`"result":{"capabilities":{"tools":{}}},` +
			`"result":{"serverInfo":{"name":"second"},"smuggled":{"prompts":true}}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	rec := post(h, goodToken, rpc(t, "initialize", nil))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), MsgInitializeUnprojectable)
	assert.NotContains(t, rec.Body.String(), "smuggled")
}

// The upstream's status is the client's signal, and it is relayed whatever
// it was — a 202 must not arrive as a 200.
func TestInitializeRelaysTheUpstreamStatus(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusAccepted, 299} {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(initializeBody(advertisedAll)))
		}))
		fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
		h := newGateway(t, fs, up)

		rec := post(h, goodToken, rpc(t, "initialize", nil))
		assert.Equal(t, status, rec.Code)
		assert.JSONEq(t, `{"tools":{}}`, string(initResult(t, rec)["capabilities"]))
		up.Close()
	}
}

// Neither a result nor an error is not a handshake, and must not be
// relayed as `"result": null` under a 200 for a client to dereference.
func TestInitializeWithNeitherResultNorErrorFailsClosed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	rec := post(h, goodToken, rpc(t, "initialize", nil))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), MsgInitializeUnprojectable)
}

func TestInitializeSSEWithNoAnsweringFrameFailsClosed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: " +
			`{"jsonrpc":"2.0","id":99,"result":{"capabilities":{"prompts":{}}}}` + "\n\n"))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	rec := post(h, goodToken, rpc(t, "initialize", nil))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), MsgInitializeUnprojectable)
}

// An upstream that refuses the handshake has nothing to project; its own
// answer is what the client needs to read.
func TestInitializeUpstreamErrorRelayed(t *testing.T) {
	t.Run("jsonrpc error", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,` +
				`"error":{"code":-32602,"message":"unsupported protocolVersion"}}`))
		}))
		defer up.Close()
		fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
		h := newGateway(t, fs, up)

		rec := post(h, goodToken, rpc(t, "initialize", nil))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "unsupported protocolVersion")
	})
	t.Run("http error with no handshake in it", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "upstream is starting", http.StatusServiceUnavailable)
		}))
		defer up.Close()
		fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
		h := newGateway(t, fs, up)

		rec := post(h, goodToken, rpc(t, "initialize", nil))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Contains(t, rec.Body.String(), "upstream is starting")
	})
	// A non-2xx status does not exempt an advertisement from the
	// projection. A server answering 503 with a complete handshake would
	// otherwise put the unprojected one back on the wire.
	t.Run("http error that still carries a handshake", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(initializeBody(advertisedAll)))
		}))
		defer up.Close()
		fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
		h := newGateway(t, fs, up)

		rec := post(h, goodToken, rpc(t, "initialize", nil))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code,
			"the upstream's status is what tells the client the handshake failed")
		assert.NotContains(t, rec.Body.String(), "prompts",
			"a non-2xx status is not an exemption from the projection")
		assert.JSONEq(t, `{"tools":{}}`, string(initResult(t, rec)["capabilities"]))
	})
}

// An initialize the gateway never sent upstream must not be projected into
// a success: the redirect refusal stands.
func TestInitializeRedirectStillRefused(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.invalid/mcp/", http.StatusTemporaryRedirect)
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	rec := post(h, goodToken, rpc(t, "initialize", nil))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), MsgUpstreamRedirected)
	assert.Empty(t, rec.Header().Get("Location"))
}

// A client that normalises its base URL by appending a slash reaches the
// same seam. What it may then call is unchanged.
func TestTrailingSlashOnTheSeamPath(t *testing.T) {
	var seen int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(initializeBody(advertisedAll)))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	rec := postTo(h, goodToken, "/upstream/kagent-tools/mcp/", rpc(t, "initialize", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"tools":{}}`, string(initResult(t, rec)["capabilities"]))
	assert.Equal(t, 1, seen)

	t.Run("still authenticated", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized,
			postTo(h, "", "/upstream/kagent-tools/mcp/", rpc(t, "initialize", nil)).Code)
	})
	t.Run("still one segment", func(t *testing.T) {
		for _, deeper := range []string{
			"/upstream/kagent-tools/mcp/prompts",
			"/upstream/kagent-tools/mcp/a/b",
		} {
			assert.Equal(t, http.StatusNotFound,
				postTo(h, goodToken, deeper, rpc(t, "initialize", nil)).Code,
				"the slash form is the same seam, not a prefix under it: %s", deeper)
		}
	})
	t.Run("still no server-initiated stream", func(t *testing.T) {
		// GET is answered by the mux, not by a handler: the gateway
		// offers no stream to open, on either form of the path.
		for _, path := range []string{"/upstream/kagent-tools/mcp", "/upstream/kagent-tools/mcp/"} {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
			req.Header.Set("Authorization", goodToken)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, path)
		}
	})
	t.Run("terminate too", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete,
			"/upstream/kagent-tools/mcp/", nil)
		req.Header.Set("Authorization", goodToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.NotEqual(t, http.StatusNotFound, rec.Code)
	})
}

// Everything the tools-only method set refuses is still refused, and the
// projected advertisement is what stops a client asking.
func TestProjectionDoesNotWidenTheMethodSet(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a refused method must not reach the tool server")
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := newGateway(t, fs, up)

	for _, method := range []string{"prompts/list", "prompts/get", "resources/list", "resources/read"} {
		rec := post(h, goodToken, rpc(t, method, nil))
		assert.Equal(t, http.StatusOK, rec.Code, method)
		assert.Contains(t, rec.Body.String(), "method not relayed by the Kaimahi gateway (tools only)", method)
	}
}
