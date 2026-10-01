package app

import (
	"context"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// TestOrkaRunViewLive exercises caller-context reads against an explicitly
// selected disposable cluster. Without the opt-in environment it writes nothing.
func TestOrkaRunViewLive(t *testing.T) {
	contextName, rootName, rootUID := os.Getenv("KMX_RUN_VIEW_CONTEXT"), os.Getenv("KMX_RUN_VIEW_TASK"), os.Getenv("KMX_RUN_VIEW_UID")
	if contextName == "" || rootName == "" || rootUID == "" {
		t.Skip("requires an explicit context and Task identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	a := &App{Cfg: &config.Config{KubeContext: contextName, ContextSource: config.SourceFlag}, Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard}, Out: io.Discard, Err: io.Discard}
	view, err := a.ReadOrkaRun(ctx, OrkaNamespace, rootName, rootUID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ID != rootUID || len(view.Tasks) == 0 || view.Tasks[0].ID != rootUID {
		t.Fatalf("unexpected root identity: %q", view.ID)
	}
	if expected := os.Getenv("KMX_RUN_VIEW_EXPECT_HELPERS"); expected != "" {
		n, err := strconv.Atoi(expected)
		if err != nil {
			t.Fatal("invalid expected helper count")
		}
		if len(view.DeclaredHelpers) != n {
			t.Fatalf("declared helpers=%d, want %d", len(view.DeclaredHelpers), n)
		}
	}
	if view.EventsMissing != nil {
		t.Fatalf("event history unavailable: %s", view.EventsMissing.Reason)
	}
	if view.TraceMissing != nil {
		t.Fatalf("trace unavailable: %s", view.TraceMissing.Reason)
	}
}
