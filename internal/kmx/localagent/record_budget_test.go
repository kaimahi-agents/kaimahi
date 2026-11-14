package localagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInstanceRecordWriterAndReaderShareExactLimit(t *testing.T) {
	base, err := json.Marshal(Instance{})
	if err != nil {
		t.Fatal(err)
	}
	record := Instance{Source: strings.Repeat("x", maxInstanceRecordBytes-len(base))}
	raw, err := marshalInstanceRecord(record)
	if err != nil || len(raw) != maxInstanceRecordBytes {
		t.Fatalf("boundary %d: %v", len(raw), err)
	}
	record.Source += "x"
	if _, err := marshalInstanceRecord(record); err == nil {
		t.Fatal("oversized record accepted")
	}
	record.Source = strings.Repeat("\n", maxInstanceRecordBytes/2)
	if _, err := marshalInstanceRecord(record); err == nil {
		t.Fatal("JSON escaping bypassed record budget")
	}
}
