package runtime

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func referenceSource() SessionReference {
	return SessionReference{Store: "127.0.0.1:9000", SessionUID: "source-1", Harness: "chat", HarnessVersion: "v0.1.3-0.20261008190619-e680ea10d3b4", Model: "host-model", InputSeq: 2, PrefixSeq: 5, PrefixHash: strings.Repeat("a", 64), InputDigest: strings.Repeat("b", 64), AnswerDigest: strings.Repeat("c", 64), ConfigDigest: strings.Repeat("d", 64)}
}
func referenceManifest(t *testing.T) SessionReferenceManifest {
	t.Helper()
	authored, err := ParseSessionReferenceAssertions([]byte("id: capital\nexpectContains: [Paris]\nassertions:\n- {id: no-secret, type: notContains, value: private-canary}\n"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewSessionReferenceCase(authored, referenceSource())
	if err != nil {
		t.Fatal(err)
	}
	return SessionReferenceManifest{SchemaVersion: 1, Policy: SessionReferencePolicy, Cases: []SessionReferenceCase{c}}
}

// Losing source binding, persisting input, or omitting assertions from identity breaks this contract.
func TestSessionReferenceManifestRoundTripAndBinding(t *testing.T) {
	m := referenceManifest(t)
	if m.Cases[0].ID != "capital-df9272883f97" {
		t.Fatalf("id=%s", m.Cases[0].ID)
	}
	data, err := MarshalSessionReferenceManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"input"`) || !strings.Contains(string(data), `"sessionUID"`) {
		t.Fatalf("manifest=%s", data)
	}
	got, err := ParseSessionReferenceManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	again, err := MarshalSessionReferenceManifest(got)
	if err != nil || string(again) != string(data) {
		t.Fatalf("canonical round trip: %v", err)
	}
	authored := EvaluationCase{ID: strings.Repeat("a", 63), ExpectContains: []string{"Paris"}}
	a, err := NewSessionReferenceCase(authored, referenceSource())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.ID) != 63 {
		t.Fatalf("long id=%s", a.ID)
	}
	source := referenceSource()
	source.SessionUID = "source-2"
	b, err := NewSessionReferenceCase(authored, source)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("different source UID did not change ID")
	}
	source = referenceSource()
	source.Store = "other:9000"
	b, err = NewSessionReferenceCase(authored, source)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("different store did not change ID")
	}
	authored.ExpectContains = []string{"Lyon"}
	b, err = NewSessionReferenceCase(authored, referenceSource())
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || a.AssertionsDigest == b.AssertionsDigest {
		t.Fatal("assertion change must change digest, not source case ID")
	}
}

func TestSessionReferenceManifestRejectsInvalidContracts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*SessionReferenceManifest)
	}{
		{"version", func(m *SessionReferenceManifest) { m.SchemaVersion = 2 }},
		{"policy", func(m *SessionReferenceManifest) { m.Policy = "other" }},
		{"empty cases", func(m *SessionReferenceManifest) { m.Cases = nil }},
		{"duplicate ID", func(m *SessionReferenceManifest) {
			c := m.Cases[0]
			c.Source.SessionUID = "source-2"
			m.Cases = append(m.Cases, c)
		}},
		{"duplicate UID", func(m *SessionReferenceManifest) { c := m.Cases[0]; c.ID = "other"; m.Cases = append(m.Cases, c) }},
		{"unsafe ID", func(m *SessionReferenceManifest) { m.Cases[0].ID = "private\ncanary" }},
		{"unsafe UID", func(m *SessionReferenceManifest) { m.Cases[0].Source.SessionUID = "private\ncanary" }},
		{"missing store", func(m *SessionReferenceManifest) { m.Cases[0].Source.Store = "" }},
		{"unsafe model", func(m *SessionReferenceManifest) { m.Cases[0].Source.Model = "private canary" }},
		{"harness", func(m *SessionReferenceManifest) { m.Cases[0].Source.Harness = "custom" }},
		{"revision", func(m *SessionReferenceManifest) { m.Cases[0].Source.HarnessVersion = "new" }},
		{"input seq", func(m *SessionReferenceManifest) { m.Cases[0].Source.InputSeq = 3 }},
		{"prefix seq", func(m *SessionReferenceManifest) { m.Cases[0].Source.PrefixSeq = 6 }},
		{"prefix hash", func(m *SessionReferenceManifest) { m.Cases[0].Source.PrefixHash = "" }},
		{"input digest", func(m *SessionReferenceManifest) { m.Cases[0].Source.InputDigest = strings.Repeat("A", 64) }},
		{"answer digest", func(m *SessionReferenceManifest) { m.Cases[0].Source.AnswerDigest = "private-canary" }},
		{"config digest", func(m *SessionReferenceManifest) { m.Cases[0].Source.ConfigDigest = "" }},
		{"changed assertions", func(m *SessionReferenceManifest) { m.Cases[0].ExpectContains = []string{"Lyon"} }},
		{"changed assertion identity", func(m *SessionReferenceManifest) { m.Cases[0].Assertions[0].ID = "changed" }},
		{"empty assertions", func(m *SessionReferenceManifest) { m.Cases[0].ExpectContains = nil; m.Cases[0].Assertions = nil }},
		{"duplicate assertions", func(m *SessionReferenceManifest) {
			m.Cases[0].Assertions = append(m.Cases[0].Assertions, m.Cases[0].Assertions[0])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := referenceManifest(t)
			tc.change(&m)
			data, _ := json.Marshal(m)
			if _, err := ParseSessionReferenceManifest(data); err == nil || strings.Contains(err.Error(), "private-canary") {
				t.Fatalf("parse error=%v", err)
			}
			if _, err := MarshalSessionReferenceManifest(m); err == nil {
				t.Fatal("marshal accepted invalid contract")
			}
		})
	}
}

func TestSessionReferenceManifestStrictJSONAndBounds(t *testing.T) {
	data, err := MarshalSessionReferenceManifest(referenceManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	for _, bad := range []string{
		"null", "[]", raw + " {}", strings.Replace(raw, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1),
		strings.Replace(raw, `"source":{`, `"source":{"store":"other",`, 1),
		strings.Replace(raw, `"source":{`, `"source":{"input":"private-canary",`, 1),
		strings.Replace(raw, `"source":{`, `"source":{"Store":"other",`, 1),
		strings.Replace(raw, `"inputSeq":2`, `"inputSeq":null`, 1),
		strings.Replace(raw, `"expectContains":["Paris"]`, `"expectContains":[null]`, 1),
		strings.Replace(raw, `"assertions":[{`, `"assertions":[null,{`, 1),
		strings.Replace(raw, `"type":"notContains"`, `"type":"notContains","value":"duplicate"`, 1),
		strings.Replace(raw, `"type":"notContains"`, `"type":"notContains","tool":""`, 1),
		strings.Replace(raw, `"policy":`, `"Policy":`, 1),
		strings.Repeat(" ", 1<<20) + raw,
	} {
		if _, err := ParseSessionReferenceManifest([]byte(bad)); err == nil {
			t.Fatalf("accepted malformed JSON: %.150s", bad)
		}
	}
}

func TestSessionReferenceManifestAcceptsExactBounds(t *testing.T) {
	m := referenceManifest(t)
	base := m.Cases[0]
	for i := 1; i < 1000; i++ {
		c := base
		c.ID = "case-" + strconv.Itoa(i)
		c.Source.SessionUID = "source-" + strconv.Itoa(i+1)
		m.Cases = append(m.Cases, c)
	}
	data, err := MarshalSessionReferenceManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSessionReferenceManifest(data); err != nil {
		t.Fatal(err)
	}
	c := base
	c.ID = "case-1000"
	c.Source.SessionUID = "source-1001"
	m.Cases = append(m.Cases, c)
	if _, err := MarshalSessionReferenceManifest(m); err == nil {
		t.Fatal("accepted >1000 valid cases")
	}
	tooMany, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(tooMany) > 1<<20 {
		t.Fatal("count fixture must isolate count, not byte bound")
	}
	if _, err := ParseSessionReferenceManifest(tooMany); err == nil {
		t.Fatal("parsed >1000 valid cases")
	}
	data, err = MarshalSessionReferenceManifest(referenceManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	padded := append(data, []byte(strings.Repeat(" ", (1<<20)-len(data)))...)
	if _, err := ParseSessionReferenceManifest(padded); err != nil {
		t.Fatalf("exactly 1 MiB: %v", err)
	}
	if _, err := ParseSessionReferenceManifest(append(padded, ' ')); err == nil {
		t.Fatal("parsed >1 MiB")
	}

	m = referenceManifest(t)
	m.Cases = nil
	for i := 0; i < 2; i++ {
		source := referenceSource()
		source.SessionUID = "source-" + strconv.Itoa(i)
		c, err := NewSessionReferenceCase(EvaluationCase{ID: "case", ExpectContains: []string{strings.Repeat("x", 600000)}}, source)
		if err != nil {
			t.Fatal(err)
		}
		m.Cases = append(m.Cases, c)
	}
	if _, err := MarshalSessionReferenceManifest(m); err == nil {
		t.Fatal("marshaled >1 MiB of valid cases")
	}
}

func TestSessionReferenceAssertionsStrictAuthoredYAML(t *testing.T) {
	for _, data := range []string{
		"id: a\ninput: secret-input\nexpectContains: [x]\n", "id: a\ninput: null\nexpectContains: [x]\n",
		"id: a\nid: b\nexpectContains: [x]\n", "id: a\nexpectContains: [x]\nunknown: x\n", "id: a\nexpectContains: [null]\n",
		"id: a\nexpectContains: [x]\n---\nid: b\n", "id: a\nexpectContains: &v [x]\nassertions: *v\n",
		"id: a\nassertions: [null]\n", "id: a\nassertions: [{id: x, type: contains, value: x, tool: null}]\n", "id: a\nassertions: null\n",
		"id: a\nassertions: [{id: x, type: regex, pattern: '['}]\n", "id: a\n",
	} {
		if _, err := ParseSessionReferenceAssertions([]byte(data)); err == nil || strings.Contains(err.Error(), "secret-input") {
			t.Fatalf("authored error=%v for %q", err, data)
		}
	}
	if _, err := NewSessionReferenceCase(EvaluationCase{ID: "a", Input: "secret-input", ExpectContains: []string{"x"}}, referenceSource()); err == nil {
		t.Fatal("constructor accepted authored input")
	}
}

func TestSessionReferenceTypedAssertionsAndStoreScopedUID(t *testing.T) {
	authored, err := ParseSessionReferenceAssertions([]byte("id: typed\nassertions:\n- {id: regex, type: regex, pattern: '^Paris$'}\n- {id: tool, type: toolNotCalled, tool: inventory}\n- {id: judge, type: llmJudge, rubric: private-rubric, minScore: 0, judgeModel: judge-model, judgeVersion: v1}\n"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewSessionReferenceCase(authored, referenceSource())
	if err != nil {
		t.Fatal(err)
	}
	m := SessionReferenceManifest{SchemaVersion: 1, Policy: SessionReferencePolicy, Cases: []SessionReferenceCase{c}}
	// The same UID at a separately selected store is a distinct source.
	source := referenceSource()
	source.Store = "other:9000"
	other, err := NewSessionReferenceCase(authored, source)
	if err != nil {
		t.Fatal(err)
	}
	m.Cases = append(m.Cases, other)
	data, err := MarshalSessionReferenceManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSessionReferenceManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cases[0].Assertions[2].MinScore == nil || *got.Cases[0].Assertions[2].MinScore != 0 {
		t.Fatal("zero judge threshold was lost")
	}
	for _, bad := range []string{
		strings.Replace(string(data), `"minScore":0`, `"minScore":null`, 1),
		strings.Replace(string(data), `"minScore":0`, `"minScore":"0"`, 1),
		strings.Replace(string(data), `"rubric":"private-rubric"`, `"rubric":"private-rubric","tool":""`, 1),
	} {
		if _, err := ParseSessionReferenceManifest([]byte(bad)); err == nil {
			t.Fatal("parsed malformed typed assertion")
		}
	}
}
