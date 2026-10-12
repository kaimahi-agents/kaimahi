package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func privateSuiteDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSuitePassingExportExclusiveCompletePrivate(t *testing.T) {
	root, prepared, suite, placement, observations := passingFixture(t)
	receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(privateSuiteDir(t), "receipt.json")
	first, err := preflightSuitePassingExport(t.Context(), destination, root, filepath.Dir(prepared.Path))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := preflightSuitePassingExport(t.Context(), destination, root, filepath.Dir(prepared.Path))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	results := make(chan error, 2)
	for _, export := range []*suitePassingExport{first, second} {
		go func() { results <- export.publish(t.Context(), receipt, suite) }()
	}
	a, b := <-results, <-results
	if (a == nil) == (b == nil) {
		t.Fatalf("exclusive winner missing: %v / %v", a, b)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("not a private receipt", err)
	}
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil || len(entries) != 1 {
		t.Fatal("staging residue", err)
	}
	read, err := readSuitePassingReceipt(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := canonicalSuitePassingReceipt(t.Context(), receipt, suite)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := canonicalSuitePassingReceipt(t.Context(), read, suite)
	if err != nil || !bytes.Equal(actual, expected) {
		t.Fatal("publication incomplete", err)
	}
}

func TestSuitePassingExportPreflightAndPublishRefusals(t *testing.T) {
	root, prepared, suite, placement, observations := passingFixture(t)
	receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	for _, refusal := range []string{"existing", "directory", "symlink file", "symlink parent", "public parent", "workspace", "cache", "canceled", "collision after preflight", "cancel after preflight", "incoherent", "parent moved"} {
		t.Run(refusal, func(t *testing.T) {
			parent := privateSuiteDir(t)
			destination := filepath.Join(parent, "receipt.json")
			ctx := t.Context()
			switch refusal {
			case "existing":
				writeFile(t, destination, "original")
			case "directory":
				if err := os.Mkdir(destination, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink file":
				if err := os.Symlink(filepath.Join(parent, "missing"), destination); err != nil {
					t.Fatal(err)
				}
			case "symlink parent":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(parent, alias); err != nil {
					t.Fatal(err)
				}
				destination = filepath.Join(alias, "receipt.json")
			case "public parent":
				if err := os.Chmod(parent, 0750); err != nil {
					t.Fatal(err)
				}
			case "workspace":
				if err := os.Chmod(root, 0700); err != nil {
					t.Fatal(err)
				}
				destination = filepath.Join(root, "eval", "..", "receipt.json")
			case "cache":
				destination = filepath.Join(filepath.Dir(prepared.Path), "receipt.json")
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			export, err := preflightSuitePassingExport(ctx, destination, root, filepath.Dir(prepared.Path))
			late := strings.Contains(refusal, "after preflight") || refusal == "incoherent" || refusal == "parent moved"
			if !late {
				want := errSuitePassingFile
				if refusal == "canceled" {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					if export != nil {
						export.Close()
					}
					t.Fatalf("wrong preflight refusal: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal("preflight prerequisite failed", err)
			}
			defer export.Close()
			r := *receipt
			switch refusal {
			case "collision after preflight":
				writeFile(t, destination, "original")
			case "cancel after preflight":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "incoherent":
				r.Subject.PayloadDigest = strings.Repeat("0", 64)
			case "parent moved":
				if err := os.Rename(parent, parent+"-moved"); err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(parent + "-moved")
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
			}
			want := errSuitePassingExport
			if refusal == "cancel after preflight" {
				want = context.Canceled
			} else if refusal == "incoherent" {
				want = errSuitePassingReceipt
			}
			if err := export.publish(ctx, &r, suite); !errors.Is(err, want) {
				t.Fatalf("wrong publication refusal: %v", err)
			}
			raw, err := os.ReadFile(destination)
			if refusal == "collision after preflight" {
				if err != nil || string(raw) != "original" {
					t.Fatal("overwrote collision", err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("refusal published output", err)
			}
		})
	}
}

func TestSuitePassingExportReportsPublicationSyncFailureHonestly(t *testing.T) {
	root, prepared, suite, placement, observations := passingFixture(t)
	receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(privateSuiteDir(t), "receipt.json")
	export, err := preflightSuitePassingExport(t.Context(), destination, root, filepath.Dir(prepared.Path))
	if err != nil {
		t.Fatal(err)
	}
	defer export.Close()
	if err := export.directory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := export.publish(t.Context(), receipt, suite); err == nil || !strings.Contains(err.Error(), "output may exist") {
		t.Fatalf("dishonest sync failure: %v", err)
	}
	if _, err := readSuitePassingReceipt(t.Context(), destination); err != nil {
		t.Fatal("expected complete output after actual link", err)
	}
}

func TestSuitePassingReceiptPrivateReader(t *testing.T) {
	_, prepared, suite, placement, observations := passingFixture(t)
	receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), receipt, suite)
	if err != nil {
		t.Fatal(err)
	}
	for _, refusal := range []string{"public file", "public parent", "symlink", "directory", "oversize", "malformed", "canceled"} {
		t.Run(refusal, func(t *testing.T) {
			parent := privateSuiteDir(t)
			name := filepath.Join(parent, "receipt.json")
			ctx := t.Context()
			if err := os.WriteFile(name, raw, 0600); err != nil {
				t.Fatal(err)
			}
			var setupErr error
			switch refusal {
			case "public file":
				setupErr = os.Chmod(name, 0644)
			case "public parent":
				setupErr = os.Chmod(parent, 0750)
			case "symlink":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				setupErr = os.Symlink("missing", name)
			case "directory":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				setupErr = os.Mkdir(name, 0700)
			case "oversize":
				setupErr = os.WriteFile(name, []byte(strings.Repeat(" ", maxSuitePassingReceiptBytes+1)), 0600)
			case "malformed":
				if err := os.WriteFile(name, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			want := errSuitePassingFile
			if refusal == "malformed" {
				want = errSuitePassingReceipt
			} else if refusal == "canceled" {
				want = context.Canceled
			}
			if _, err := readSuitePassingReceipt(ctx, name); !errors.Is(err, want) || strings.Contains(err.Error(), name) {
				t.Fatalf("wrong private-reader refusal/error: %v", err)
			}
		})
	}
}
