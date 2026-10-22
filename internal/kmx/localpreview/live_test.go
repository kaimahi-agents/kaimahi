package localpreview

import (
	"os"
	"testing"
)

// TestLiveCompatibleModel records one real compatible-model run. It is skipped
// unless KMX_LOCAL_PREVIEW_LIVE_BASE_URL and KMX_LOCAL_PREVIEW_LIVE_MODEL are
// set; KMX_LOCAL_PREVIEW_LIVE_KEY_ENV optionally names the credential variable.
// It needs no cluster tools or background service beyond the endpoint itself,
// and asserts a terminal attributable result and a zero-model replay, not that
// the model satisfies the greeter's example.
func TestLiveCompatibleModel(t *testing.T) {
	base, model := os.Getenv("KMX_LOCAL_PREVIEW_LIVE_BASE_URL"), os.Getenv("KMX_LOCAL_PREVIEW_LIVE_MODEL")
	if base == "" || model == "" {
		t.Skip("set KMX_LOCAL_PREVIEW_LIVE_BASE_URL and KMX_LOCAL_PREVIEW_LIVE_MODEL to record a real compatible-model run")
	}
	dir := journalDir(t)
	result, err := Test(testContext(t), modelAgent(t, model), Options{JournalDir: dir, Endpoint: &Endpoint{BaseURL: base, KeyEnv: os.Getenv("KMX_LOCAL_PREVIEW_LIVE_KEY_ENV")}})
	if err != nil {
		t.Fatal(err)
	}
	row := result.Examples[0]
	t.Logf("%s: model=%s route=%s verdict=%s execution=%s reason=%q sends=%d", result.Label, result.Model, result.ModelRoute, result.Verdict, row.Execution, row.Reason, result.ModelSends)
	if row.Execution != "completed" || result.ModelSends != 1 || row.SessionUID == "" {
		t.Fatalf("live run did not complete: %+v", row)
	}
	inspection, err := Inspect(testContext(t), dir, result.RunID)
	if err != nil || inspection.ModelCalls != 0 || inspection.Sessions[0].Answer != row.Answer {
		t.Fatalf("replay = %v %+v", err, inspection)
	}
}
