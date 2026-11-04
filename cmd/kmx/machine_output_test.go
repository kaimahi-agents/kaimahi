package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func decodeMachineRecord(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var got map[string]any
	d := json.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&got); err != nil {
		t.Fatalf("invalid record %q: %v", data, err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		t.Fatalf("trailing record or malformed bytes: %v", err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatal("record lacks newline")
	}
	return got
}

func TestMachineResultEnvelope(t *testing.T) {
	var out bytes.Buffer
	w := newMachineOutput(&out, "version")
	if err := w.result("failed", "not_applicable", 1, app.NewMachineError("invalid_arguments", "")); err != nil {
		t.Fatal(err)
	}
	r := decodeMachineRecord(t, out.Bytes())
	if r["schemaVersion"] != float64(1) || r["kind"] != "kmx.result" || r["operation"] != "version" || r["seq"] != float64(1) ||
		r["outcome"] != "failed" || r["verdict"] != "not_applicable" || r["exitCode"] != float64(1) || r["elapsedMs"].(float64) < 0 {
		t.Fatalf("bad envelope: %+v", r)
	}
	if _, err := uuid.Parse(r["operationId"].(string)); err != nil {
		t.Fatal(err)
	}
	before := out.String()
	if err := w.result("complete", "not_applicable", 0, nil); err == nil || out.String() != before {
		t.Fatal("accepted a second terminal record")
	}
}

type machineFaultWriter struct {
	calls int
	err   error
	short bool
}

func (w *machineFaultWriter) Write(b []byte) (int, error) {
	w.calls++
	if w.short {
		return len(b) - 1, nil
	}
	return 0, w.err
}

func TestMachineWriterLatchesFailure(t *testing.T) {
	sentinel := errors.New("private output details")
	for _, tc := range []struct {
		name   string
		writer *machineFaultWriter
		want   error
	}{
		{"failed", &machineFaultWriter{err: sentinel}, sentinel},
		{"short", &machineFaultWriter{short: true}, io.ErrShortWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newMachineOutput(tc.writer, "version")
			for i := 0; i < 2; i++ {
				if err := w.result("failed", "not_applicable", 1, nil); !errors.Is(err, tc.want) {
					t.Fatalf("failure replaced: %v", err)
				}
			}
			if tc.writer.calls != 1 {
				t.Fatalf("retried a possible partial write: %d", tc.writer.calls)
			}
		})
	}
}

func TestMachineWriterBoundsBeforeWrite(t *testing.T) {
	for _, n := range []int{64*1024 - 3, 64*1024 - 2} {
		var out bytes.Buffer
		w := newMachineOutput(&out, "version")
		// JSON string quotes and the terminating newline count toward the limit.
		err := w.document(strings.Repeat("x", n))
		if n == 64*1024-3 {
			if err != nil || out.Len() != 64*1024 {
				t.Fatalf("exact bound refused: %v, %d", err, out.Len())
			}
		} else if err == nil || out.Len() != 0 {
			t.Fatal("oversized record was partially written")
		}
	}
}

func TestMachineWriterVersionIsTerminal(t *testing.T) {
	var out bytes.Buffer
	w := newMachineOutput(&out, "version")
	if err := w.document(struct {
		Kind string `json:"kind"`
	}{"kmx.version"}); err != nil {
		t.Fatal(err)
	}
	if err := w.result("failed", "not_applicable", 1, nil); err == nil {
		t.Fatal("result after version document")
	}
	if r := decodeMachineRecord(t, out.Bytes()); r["kind"] != "kmx.version" {
		t.Fatal(r)
	}
}

func TestMachineWriterSerializesTerminal(t *testing.T) {
	var out bytes.Buffer
	w := newMachineOutput(&out, "version")
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w.result("complete", "not_applicable", 0, nil) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("accepted %d terminal records", successes.Load())
	}
	decodeMachineRecord(t, out.Bytes())
}

func TestMachineCommandErrorPreservesExitWithoutCauseText(t *testing.T) {
	cause := errors.New("private provider response")
	wrapped := &machineCommandError{cause: &fileCommandError{cause: cause, exit: 130}, safe: app.NewMachineError("cancelled", "")}
	if exitCode(wrapped) != 130 || !errors.Is(wrapped, cause) || wrapped.Error() != "The operation was cancelled." {
		t.Fatalf("broken safe wrapping: %v", wrapped)
	}
}
