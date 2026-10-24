package copilot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestResponsesProtocolIsResolvedAtSend(t *testing.T) {
	for _, both := range []bool{false, true} {
		t.Run(fmt.Sprint(both), func(t *testing.T) {
			paths := `["/chat/completions"]`
			posts := 0
			p, store := fixture(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/models" {
					fmt.Fprintf(w, `{"data":[{"id":"gpt-test","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"supported_endpoints":%s}]}`, paths)
					return
				}
				posts++
				if both {
					if req.URL.Path != "/chat/completions" {
						t.Error("existing Chat protocol changed")
					}
					io.WriteString(w, `{"choices":[]}`)
				} else {
					if req.URL.Path != "/responses" {
						t.Error("stale protocol selected")
					}
					if req.Header.Get("Accept-Encoding") != "gzip" && req.Header.Get("Accept-Encoding") != "" {
						t.Error("caller compression hint survived")
					}
					io.WriteString(w, `{"model":"gpt-test","status":"completed","output":[]}`)
				}
			})
			store.token = canary
			client, err := p.Client(context.Background(), selected())
			if err != nil {
				t.Fatal(err)
			}
			paths = `["/responses"]`
			if both {
				paths = `["/responses","/chat/completions"]`
			}
			req := chatRequest(t, `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":false}`)
			if !both {
				req.Header.Set("Accept-Encoding", "deflate")
			}
			response, err := client.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || posts != 1 {
				t.Fatalf("send: %v posts=%d", err, posts)
			}
			if !both && (!strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") || response.ContentLength != int64(len(body))) {
				t.Fatal("converted length/type stale")
			}
		})
	}
}
