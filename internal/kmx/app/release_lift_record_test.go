package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var recordTestID = strings.Repeat("0123456789abcdef", 2)

func recordTestInput(t *testing.T) (ReleaseLiftSummary, []byte, string) {
	t.Helper()
	t.Setenv("KMX_HOME", t.TempDir())
	raw := []byte(`{"canonical":"retained <exactly>"}`)
	return ReleaseLiftSummary{EvidenceDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), Resources: []ReleaseLiftResource{{Kind: "Provider", Name: "greeter", Action: "create", SpecDigest: "provider-spec"}, {Kind: "Agent", Name: "greeter", Action: "create", SpecDigest: "agent-spec"}}}, raw, `{"version":1,"liftID":"` + recordTestID + `"}`
}

func recordTestPath(name string) string {
	return filepath.Join(os.Getenv("KMX_HOME"), "release-lifts", recordTestID, name)
}

func TestReleaseLiftRecordImmutableSnapshots(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	a, _, opt, _ := releaseLiftFixture(t)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := os.ReadFile(opt.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	summary := p.Summary()
	annotation := `{"version":1,"liftID":"` + recordTestID + `"}`
	w, err := preflightReleaseLiftRecord(t.Context(), recordTestID, summary, receipt, annotation)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	intent, err := os.ReadFile(recordTestPath("intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Mutating retained caller inputs must not change subsequent snapshots.
	receipt[0] = ' '
	summary.Resources[0].Name = "mutated"
	result := ReleaseLiftResult{State: "partial", Resources: []ReleaseLiftOutcome{{ReleaseLiftResource: p.Summary().Resources[0], Outcome: "created"}}}
	result.Resources[0].UID, result.Resources[0].Generation = "acknowledged-uid", 2
	for i := 1; i <= 3; i++ {
		if i == 3 {
			result.State = "ready"
		}
		if err := w.checkpoint(t.Context(), result); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(recordTestPath(fmt.Sprintf("%d.json", i)))
		if err != nil {
			t.Fatal(err)
		}
		var snapshot struct {
			SchemaVersion int                  `json:"schemaVersion"`
			ID            string               `json:"id"`
			Summary       ReleaseLiftSummary   `json:"summary"`
			Receipt       json.RawMessage      `json:"receipt"`
			Annotation    json.RawMessage      `json:"annotation"`
			Resources     []ReleaseLiftOutcome `json:"resources"`
			State         string               `json:"state"`
		}
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			t.Fatal(err)
		}
		original, _ := os.ReadFile(opt.Evidence)
		if snapshot.SchemaVersion != 1 || snapshot.ID != recordTestID || !bytes.Equal(snapshot.Receipt, original) || string(snapshot.Annotation) != annotation || !reflect.DeepEqual(snapshot.Summary, p.Summary()) || snapshot.State != result.State || !reflect.DeepEqual(snapshot.Resources, result.Resources) {
			t.Fatalf("incorrect frozen snapshot: %s", raw)
		}
		if bytes.Contains(raw, []byte("systemPrompt")) || bytes.Contains(raw, []byte(nativeCanary)) {
			t.Fatal("payload in private history")
		}
	}
	if err := w.checkpoint(t.Context(), result); err == nil {
		t.Fatal("fourth checkpoint accepted")
	}
	after, _ := os.ReadFile(recordTestPath("intent.json"))
	if !bytes.Equal(intent, after) {
		t.Fatal("intent overwritten")
	}
	for _, name := range []string{"intent.json", "1.json", "2.json", "3.json"} {
		info, err := os.Stat(recordTestPath(name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("nonprivate snapshot %s: %v", name, err)
		}
	}
	for _, name := range []string{filepath.Dir(recordTestPath("")), recordTestPath("")} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("nonprivate directory: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.checkpoint(t.Context(), result); err == nil {
		t.Fatal("closed writer accepted checkpoint")
	}
}

func TestReleaseLiftRecordRejectsInputBeforeState(t *testing.T) {
	for _, which := range []string{"id", "uppercase", "traversal", "digest", "receipt-size", "receipt-json", "summary-size", "annotation-size", "annotation-json", "canceled"} {
		t.Run(which, func(t *testing.T) {
			summary, receipt, annotation := recordTestInput(t)
			id, ctx := recordTestID, t.Context()
			switch which {
			case "id":
				id = "short"
			case "uppercase":
				id = strings.ToUpper(id)
			case "traversal":
				id = "../" + id
			case "digest":
				summary.EvidenceDigest = "sha256:" + strings.Repeat("0", 64)
			case "receipt-size":
				receipt = []byte(`"` + strings.Repeat("x", (1<<20)-1) + `"`)
				summary.EvidenceDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(receipt))
			case "receipt-json":
				receipt = []byte("{")
			case "summary-size":
				summary.Model = strings.Repeat("x", 16<<10)
			case "annotation-size":
				annotation = `{"x":"` + strings.Repeat("x", 8<<10) + `"}`
			case "annotation-json":
				annotation = "{"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			w, err := preflightReleaseLiftRecord(ctx, id, summary, receipt, annotation)
			if w != nil {
				w.Close()
			}
			if err == nil {
				t.Fatal("invalid input accepted")
			}
			if which == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			entries, _ := os.ReadDir(os.Getenv("KMX_HOME"))
			if len(entries) != 0 {
				t.Fatal("invalid input created state")
			}
		})
	}
}

func TestReleaseLiftRecordRefusesUnsafeStorage(t *testing.T) {
	for _, which := range []string{"state-link", "base-link", "base-mode", "lock-link", "lock-mode", "record-link", "record-mode"} {
		t.Run(which, func(t *testing.T) {
			summary, receipt, annotation := recordTestInput(t)
			home := os.Getenv("KMX_HOME")
			base := filepath.Join(home, "release-lifts")
			outside := t.TempDir()
			if err := os.Chmod(outside, 0700); err != nil {
				t.Fatal(err)
			}
			if which == "state-link" {
				t.Setenv("KMX_HOME", filepath.Join(home, "link"))
				if err := os.Symlink(outside, os.Getenv("KMX_HOME")); err != nil {
					t.Fatal(err)
				}
			} else if which == "base-link" {
				if err := os.Symlink(outside, base); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(base, 0700); err != nil {
					t.Fatal(err)
				}
				switch which {
				case "base-mode":
					if err := os.Chmod(base, 0755); err != nil {
						t.Fatal(err)
					}
				case "lock-link":
					if err := os.Symlink(filepath.Join(outside, "lock"), filepath.Join(base, "lock")); err != nil {
						t.Fatal(err)
					}
				case "lock-mode":
					if err := os.WriteFile(filepath.Join(base, "lock"), nil, 0644); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(filepath.Join(base, "lock"), 0644); err != nil {
						t.Fatal(err)
					}
				default:
					old := filepath.Join(base, strings.Repeat("a", 32))
					if err := os.Mkdir(old, 0700); err != nil {
						t.Fatal(err)
					}
					if which == "record-link" {
						if err := os.Symlink(outside, filepath.Join(old, "intent.json")); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := os.WriteFile(filepath.Join(old, "intent.json"), []byte("{}"), 0644); err != nil {
							t.Fatal(err)
						}
						if err := os.Chmod(filepath.Join(old, "intent.json"), 0644); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			w, err := preflightReleaseLiftRecord(t.Context(), recordTestID, summary, receipt, annotation)
			if w != nil {
				w.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "unsafe") {
				t.Fatalf("unsafe storage accepted: %v", err)
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 0 {
				t.Fatal("followed symlink")
			}
		})
	}
}

func TestReleaseLiftRecordExclusiveAndCanceledCheckpoint(t *testing.T) {
	summary, receipt, annotation := recordTestInput(t)
	w, err := preflightReleaseLiftRecord(t.Context(), recordTestID, summary, receipt, annotation)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := preflightReleaseLiftStorage(t.Context()); err == nil {
		t.Fatal("concurrent storage reservation accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := w.checkpoint(ctx, ReleaseLiftResult{State: "partial"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	if _, err := os.Stat(recordTestPath("1.json")); !os.IsNotExist(err) {
		t.Fatal("canceled checkpoint published")
	}
	if err := os.WriteFile(recordTestPath("1.json"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.checkpoint(t.Context(), ReleaseLiftResult{State: "partial"}); err == nil {
		t.Fatal("checkpoint overwritten")
	}
	raw, _ := os.ReadFile(recordTestPath("1.json"))
	if string(raw) != "existing" {
		t.Fatal("collision changed existing file")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := preflightReleaseLiftRecord(t.Context(), recordTestID, summary, receipt, annotation)
	if other != nil {
		other.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("duplicate ID accepted: %v", err)
	}
}

func TestReleaseLiftRecordPreflightsAllSlotsAndAnchors(t *testing.T) {
	for _, which := range []string{"future-slot", "directory-replaced", "mode-changed", "oversize-result"} {
		t.Run(which, func(t *testing.T) {
			summary, receipt, annotation := recordTestInput(t)
			w, err := preflightReleaseLiftRecord(t.Context(), recordTestID, summary, receipt, annotation)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			result := ReleaseLiftResult{State: "partial"}
			switch which {
			case "future-slot":
				if err := os.WriteFile(recordTestPath("3.json"), []byte("existing"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory-replaced":
				path := recordTestPath("")
				if err := os.Rename(path, path+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "mode-changed":
				if err := os.Chmod(recordTestPath(""), 0755); err != nil {
					t.Fatal(err)
				}
			case "oversize-result":
				result.Resources = []ReleaseLiftOutcome{{Outcome: strings.Repeat("x", 16<<10)}}
			}
			if err := w.checkpoint(t.Context(), result); err == nil {
				t.Fatal("unsafe or oversized checkpoint accepted")
			}
			if _, err := os.Stat(recordTestPath("1.json")); !os.IsNotExist(err) {
				t.Fatal("checkpoint published despite unsafe future slot")
			}
		})
	}
}

func TestReleaseLiftRecordReceiptBoundaryAndPartialCancellation(t *testing.T) {
	summary, _, annotation := recordTestInput(t)
	// Valid JSON at the exact 1 MiB limit, including characters that json.Marshal
	// would escape if it re-encoded the canonical receipt.
	receipt := []byte(`"<` + strings.Repeat("x", (1<<20)-4) + `>"`)
	summary.EvidenceDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(receipt))
	w, err := preflightReleaseLiftRecord(t.Context(), recordTestID, summary, receipt, annotation)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	result := ReleaseLiftResult{State: "partial", Resources: []ReleaseLiftOutcome{{ReleaseLiftResource: ReleaseLiftResource{Kind: "Provider", UID: "known-uid", Generation: 1, SpecDigest: "known-spec"}, Outcome: "created", Ready: false}, {ReleaseLiftResource: ReleaseLiftResource{Kind: "Agent"}, Outcome: "unknown"}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := w.checkpoint(ctx, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if err := w.checkpoint(context.WithoutCancel(ctx), result); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(recordTestPath("1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Receipt   json.RawMessage      `json:"receipt"`
		State     string               `json:"state"`
		Resources []ReleaseLiftOutcome `json:"resources"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshot.Receipt, receipt) || snapshot.State != "partial" || !reflect.DeepEqual(snapshot.Resources, result.Resources) {
		t.Fatal("partial cancellation discarded exact receipt or acknowledged facts")
	}
}

func TestReleaseLiftRecordCapacityBeforeIntent(t *testing.T) {
	for _, which := range []string{"count", "bytes", "scan-limit"} {
		t.Run(which, func(t *testing.T) {
			summary, receipt, annotation := recordTestInput(t)
			if err := preflightReleaseLiftStorage(t.Context()); err != nil {
				t.Fatal(err)
			}
			base := filepath.Dir(recordTestPath(""))
			count := 256
			if which == "bytes" {
				count = 16
			}
			if which == "scan-limit" {
				count = 258
			}
			for i := 0; i < count; i++ {
				dir := filepath.Join(base, fmt.Sprintf("%032x", i+1))
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if which == "bytes" {
					for _, name := range []string{"intent.json", "1.json", "2.json", "3.json"} {
						f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY, 0600)
						if err != nil {
							t.Fatal(err)
						}
						if err := f.Truncate(1 << 20); err != nil {
							t.Fatal(err)
						}
						if err := f.Close(); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			w, err := preflightReleaseLiftRecord(t.Context(), recordTestID, summary, receipt, annotation)
			if w != nil {
				w.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "capacity") {
				t.Fatalf("capacity accepted: %v", err)
			}
			if _, err := os.Stat(recordTestPath("intent.json")); !os.IsNotExist(err) {
				t.Fatal("intent published over capacity")
			}
		})
	}
}
