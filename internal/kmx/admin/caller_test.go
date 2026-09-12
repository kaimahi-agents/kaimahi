package admin

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestTheLedgerSaysWhoCalled(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"entries":[{"created_at":"2026-09-08T14:02:01Z","credential":"foreign-runtime","upstream":"ollama","model":"qwen2.5:3b","input_tokens":0,"output_tokens":0,"cost_cents":0,"cost_source":"denied","status":429,"acted_for":"none","caller_claim":"ua:curl/8.5.0","caller_addr":"10.244.4.2"}]}`))
	}))
	var out bytes.Buffer
	if err := c.Ledger(&out, ""); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !regexp.MustCompile(`(?m)ua:curl/8\.5\.0 +10\.244\.4\.2 +none *$`).MatchString(got) {
		t.Fatalf("caller columns moved: %s", got)
	}
	if !regexp.MustCompile(`foreign-runtime +ollama +qwen2\.5:3b +0 +0 +0 +denied +429`).MatchString(got) {
		t.Fatalf("ledger columns moved: %s", got)
	}
}

func TestAPlaneTooOldToRecordACallerSaysSoRatherThanNothing(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"entries":[{"created_at":"2026-09-08T13:58:28Z","credential":"agent","upstream":"ollama","model":"model","status":200,"acted_for":"none"}]}`))
	}))
	var out bytes.Buffer
	if err := c.Ledger(&out, ""); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)unrecorded +unrecorded +none *$`).MatchString(out.String()) {
		t.Fatalf("missing caller not marked: %s", &out)
	}
}

func TestAHostileCallerNameCannotBreakTheRenderedTable(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"entries":[{"created_at":"2026-09-08T13:58:28Z","credential":"agent","model":"model","status":200,"acted_for":"none","caller_claim":"ua:evil\" ,\n2026-09-08T00:00:00 agent model forged_row","caller_addr":"10.244.1.7"}]}`))
	}))
	var out bytes.Buffer
	if err := c.Ledger(&out, ""); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Count(strings.TrimRight(got, "\n"), "\n") != 1 || strings.Contains(got, "forged_row") {
		t.Fatalf("forged row reached table: %s", got)
	}
}

func TestLedgerEscapesAndClipsHostileCells(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"entries":[{"model":"name\ttrailing","upstream":" openai","caller_claim":"ua:naïve/1.0 \u200b","caller_addr":"fe80::1ff:fe23:4567:890a%eth0","acted_for":"none"},{"model":"old","caller_claim":"legacy","caller_addr":"legacy","acted_for":"none"}]}`))
	}))
	var out bytes.Buffer
	if err := c.Ledger(&out, ""); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if len(strings.Split(strings.TrimSuffix(got, "\n"), "\n")) != 3 {
		t.Fatalf("forged physical line: %s", got)
	}
	for _, want := range []string{`"name\ttrailing"`, `\u200b"`, `" openai"`, "fe80::1ff:fe23:…"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %s", want, got)
		}
	}
	if !regexp.MustCompile(`(?m)legacy +legacy +none *$`).MatchString(got) {
		t.Fatalf("legacy caller columns moved: %s", got)
	}
}
