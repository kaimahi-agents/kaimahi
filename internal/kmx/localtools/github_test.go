package localtools

import (
	"encoding/json"
	"strings"
	"testing"
)

const mergedSearch = `{"total_count":1,"incomplete_results":false,"items":[{"number":7,"title":"Fix parser","repository_url":"https://api.github.com/repos/owner/repo","pull_request":{"url":"https://api.github.com/repos/owner/repo/pulls/7","html_url":"https://github.com/owner/repo/pull/7","diff_url":"https://github.com/owner/repo/pull/7.diff","patch_url":"https://github.com/owner/repo/pull/7.patch","merged_at":"2026-10-23T00:00:00Z"}}]}`

func TestDecodeMergedPullRequests(t *testing.T) {
	b := Binding{"owner/repo", endpointURL, "TEST_MCP_TOKEN"}
	rows, err := decodePullRequests([]byte(mergedSearch), b)
	if err != nil || len(rows) != 1 || rows[0] != (PullRequest{Number: 7, Title: "Fix parser", Repository: "owner/repo", Merged: true}) {
		t.Fatal("qualified fields not projected", err)
	}
	rows, err = decodePullRequests([]byte(`{"total_count":0,"incomplete_results":false,"items":[]}`), b)
	if err != nil || len(rows) != 0 {
		t.Fatal("valid empty search refused", err)
	}
	for name, input := range map[string]string{
		"foreign repo":       strings.ReplaceAll(mergedSearch, "repos/owner/repo", "repos/other/repo"),
		"missing repo":       strings.Replace(mergedSearch, `"repository_url":"https://api.github.com/repos/owner/repo",`, "", 1),
		"closed not merged":  strings.Replace(mergedSearch, `"merged_at":"2026-10-23T00:00:00Z"`, `"state":"closed"`, 1),
		"null merged":        strings.Replace(mergedSearch, `"2026-10-23T00:00:00Z"`, "null", 1),
		"invalid timestamp":  strings.Replace(mergedSearch, "2026-10-23T00:00:00Z", "yesterday", 1),
		"missing timezone":   strings.Replace(mergedSearch, "2026-10-23T00:00:00Z", "2026-10-23T00:00:00", 1),
		"incomplete":         strings.Replace(mergedSearch, `"incomplete_results":false`, `"incomplete_results":true`, 1),
		"missing incomplete": strings.Replace(mergedSearch, `"incomplete_results":false,`, "", 1),
		"null items":         `{"total_count":0,"incomplete_results":false,"items":null}`,
		"missing count":      strings.Replace(mergedSearch, `"total_count":1,`, "", 1),
		"inconsistent count": strings.Replace(mergedSearch, `"total_count":1`, `"total_count":0`, 1),
		"bad number":         strings.Replace(mergedSearch, `"number":7`, `"number":0`, 1),
		"missing title":      strings.Replace(mergedSearch, `"title":"Fix parser",`, "", 1),
		"large title":        strings.Replace(mergedSearch, "Fix parser", strings.Repeat("x", 513), 1),
		"duplicate":          strings.Replace(mergedSearch, `"number":7`, `"number":1,"NUMBER":7`, 1),
		"trailing":           mergedSearch + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodePullRequests([]byte(input), b); err == nil {
				t.Fatal("invalid attribution accepted")
			}
		})
	}
	var value map[string]any
	if json.Unmarshal([]byte(mergedSearch), &value) != nil {
		t.Fatal("fixture")
	}
	row := value["items"].([]any)[0]
	value["total_count"] = 21
	value["items"] = make([]any, 21)
	for i := range value["items"].([]any) {
		value["items"].([]any)[i] = row
	}
	raw, _ := json.Marshal(value)
	if _, err := decodePullRequests(raw, b); err == nil {
		t.Fatal("row budget ignored")
	}
}
