package localtools

import (
	"encoding/json"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// decodePullRequests consumes the qualified search projection, never inferring
// repository or merged state from the request's query alone.
func decodePullRequests(data []byte, b Binding) ([]PullRequest, error) {
	if len(data) > maxMCPBytes || !agentruntime.JSONKeysUnique(data) {
		return nil, errInvalidResult
	}
	var search struct {
		Total      *int  `json:"total_count"`
		Incomplete *bool `json:"incomplete_results"`
		Items      *[]struct {
			Number      *int    `json:"number"`
			Title       *string `json:"title"`
			Repository  *string `json:"repository_url"`
			PullRequest *struct {
				MergedAt *string `json:"merged_at"`
			} `json:"pull_request"`
		} `json:"items"`
	}
	if json.Unmarshal(data, &search) != nil || search.Total == nil || search.Incomplete == nil || *search.Incomplete || search.Items == nil || *search.Total < len(*search.Items) || len(*search.Items) > 20 || *search.Total > 0 && len(*search.Items) == 0 {
		return nil, errInvalidResult
	}
	rows := make([]PullRequest, 0, len(*search.Items))
	seen := map[int]bool{}
	for _, row := range *search.Items {
		if row.Number == nil || *row.Number <= 0 || seen[*row.Number] || row.Title == nil || len(*row.Title) > 512 || row.Repository == nil || *row.Repository != "https://api.github.com/repos/"+b.repository || row.PullRequest == nil || row.PullRequest.MergedAt == nil {
			return nil, errInvalidResult
		}
		if _, err := time.Parse(time.RFC3339Nano, *row.PullRequest.MergedAt); err != nil {
			return nil, errInvalidResult
		}
		seen[*row.Number] = true
		rows = append(rows, PullRequest{Number: *row.Number, Title: *row.Title, Repository: b.repository, Merged: true})
	}
	return rows, nil
}

const maxMCPBytes = 1024 * 1024
