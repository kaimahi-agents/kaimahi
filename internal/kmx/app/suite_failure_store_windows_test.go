//go:build windows

package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

func TestWindowsSuiteFailureStoreLifecycle(t *testing.T) {
	root, detail := failureStoreFixture(t)
	writeFile(t, filepath.Join(root, "workspace.yaml"), "schemaVersion: 1\nname: fictional\nmember: agents/fictional/member.yaml\nbuildProfile: build-profiles/default.json\n")
	id, err := saveSuiteFailure(t.Context(), root, detail)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openSuiteFailureStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	entries, scanErr := store.scan()
	path := store.path
	closeErr := store.Close()
	if scanErr != nil || closeErr != nil || len(entries) != 1 || entries[0].record.Detail.ID != id {
		t.Fatalf("store = %+v, %v, %v", entries, scanErr, closeErr)
	}
	for _, object := range []string{path, filepath.Join(path, "lock"), filepath.Join(path, entries[0].name)} {
		if info, err := os.Stat(object); err != nil {
			t.Fatal(err)
		} else if info.IsDir() {
			dir, err := fileaccess.OpenPrivateDirectory(t.Context(), object)
			if err != nil {
				t.Fatalf("private directory %s: %v", object, err)
			}
			if err := fileaccess.CheckPrivate(t.Context(), dir, true); err != nil {
				_ = dir.Close()
				t.Fatalf("private directory %s: %v", object, err)
			}
			if err := dir.Close(); err != nil {
				t.Fatal(err)
			}
		} else {
			dir, err := fileaccess.OpenPrivateDirectory(t.Context(), filepath.Dir(object))
			if err != nil {
				t.Fatal(err)
			}
			file, err := fileaccess.OpenPrivateFileAt(t.Context(), dir, filepath.Base(object))
			if err != nil {
				_ = dir.Close()
				t.Fatalf("private file %s: %v", object, err)
			}
			if err := fileaccess.CheckPrivate(t.Context(), file, false); err != nil {
				_ = file.Close()
				_ = dir.Close()
				t.Fatalf("private file %s: %v", object, err)
			}
			if err := file.Close(); err != nil {
				_ = dir.Close()
				t.Fatal(err)
			}
			if err := dir.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	var out bytes.Buffer
	if err := (&App{Out: &out, Err: io.Discard}).ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{Workspace: root, ID: id}); err != nil || !strings.Contains(out.String(), id) {
		t.Fatalf("explain = %q, %v", out.String(), err)
	}
	if n, err := clearSuiteFailures(t.Context(), root, ""); err != nil || n != 1 {
		t.Fatalf("clear = %d, %v", n, err)
	}
}

func TestWindowsSuiteFailureWorkspaceCaseAliasesSharePartition(t *testing.T) {
	root, detail := failureStoreFixture(t)
	upper := strings.ToUpper(root)
	held, err := openSuiteFailureStore(root, true)
	if err != nil {
		t.Fatal(err)
	}
	path := held.path
	if other, err := openSuiteFailureStore(upper, true); !errors.Is(err, errSuiteFailureBusy) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("case alias split lock: %v", err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := saveSuiteFailure(t.Context(), upper, detail); err != nil {
		t.Fatal(err)
	}
	store, err := openSuiteFailureStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entries, err := store.scan()
	if err != nil || store.path != path || len(entries) != 1 {
		t.Fatalf("case alias partition = %q rows=%d err=%v; want %q", store.path, len(entries), err, path)
	}
}
