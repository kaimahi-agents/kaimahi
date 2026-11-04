package main

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

func machineRecords(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(data))
	var records []map[string]any
	for {
		var r map[string]any
		err := d.Decode(&r)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	return records
}

func TestMachineStreamOrdering(t *testing.T) {
	var out bytes.Buffer
	m := newMachineOutput(&out, "cluster.up")
	if err := m.event("preflight", "active"); err != nil {
		t.Fatal(err)
	}
	if err := m.event("preflight", "done"); err != nil {
		t.Fatal(err)
	}
	if err := m.setupResult("apply", nil, "complete", 0, nil); err != nil {
		t.Fatal(err)
	}
	rs := machineRecords(t, out.Bytes())
	if len(rs) != 3 {
		t.Fatal(rs)
	}
	elapsed := float64(0)
	for i, r := range rs {
		if r["seq"] != float64(i+1) || r["operationId"] != rs[0]["operationId"] || r["elapsedMs"].(float64) < elapsed {
			t.Fatal(r)
		}
		elapsed = r["elapsedMs"].(float64)
	}
	if rs[0]["event"] != "stage.started" || rs[1]["status"] != "complete" || rs[2]["kind"] != "kmx.result" {
		t.Fatal(rs)
	}
	before := out.Len()
	if m.event("cluster", "active") == nil || out.Len() != before {
		t.Fatal("event after terminal")
	}
}

func TestMachineStreamFailureCancelsOnce(t *testing.T) {
	out := &machineFaultWriter{short: true}
	m := newMachineOutput(out, "cluster.up")
	cancels := 0
	m.cancel = func() { cancels++ }
	for i := 0; i < 2; i++ {
		if m.event("cluster", "active") == nil {
			t.Fatal("lost short write")
		}
	}
	if out.calls != 1 || cancels != 1 {
		t.Fatal("retry or repeated cancel", out.calls, cancels)
	}
}
