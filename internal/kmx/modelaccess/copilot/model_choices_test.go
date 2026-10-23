package copilot

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestUnavailableModelListsCurrentChoices(t *testing.T) {
	for _, beforeSend := range []bool{false, true} {
		t.Run(map[bool]string{false: "client", true: "before-send"}[beforeSend], func(t *testing.T) {
			changed, posts := false, 0
			p, store := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					if changed {
						io.WriteString(w, strings.ReplaceAll(catalog, "gpt-test", "other-model"))
					} else {
						io.WriteString(w, catalog)
					}
					return
				}
				posts++
				t.Error("unknown model sent inference")
			})
			store.token = canary
			route := selected()
			if !beforeSend {
				route.Model = "unavailable"
			}
			client, err := p.Client(t.Context(), route)
			if beforeSend {
				if err != nil {
					t.Fatal(err)
				}
				changed = true
				_, err = client.RoundTrip(chatRequest(t, `{"model":"gpt-test","messages":[]}`))
			}
			if !errors.Is(err, ErrModelUnavailable) || posts != 0 || !strings.Contains(err.Error(), "responses-only") || strings.ContainsAny(err.Error(), "\r\n") {
				t.Fatalf("missing one-line choices: %v", err)
			}
			safe(t, err)
		})
	}
}

func TestUpstreamModelRefusalDoesNotEchoCatalogOrBody(t *testing.T) {
	p, store := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, catalog)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"code":"model_not_found","secret":"`+canary+`"}`)
	})
	store.token = canary
	client, err := p.Client(t.Context(), selected())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.RoundTrip(chatRequest(t, `{"model":"gpt-test","messages":[]}`))
	if !errors.Is(err, ErrModelUnavailable) || strings.Contains(err.Error(), "responses-only") {
		t.Fatalf("service refusal acquired catalog choices: %v", err)
	}
	safe(t, err)
}
