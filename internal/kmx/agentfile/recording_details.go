package agentfile

import (
	"go.yaml.in/yaml/v3"
	"strings"
)

type RecordingDetail struct {
	ExampleID string `json:"exampleId"`
	Name      string `json:"name"`
	Text      string `json:"text"`
}

// RecordingDetails displays authored YAML nodes without a float64/JS round trip.
// Encoding may normalize presentation, never the original scalar lexemes or file.
func RecordingDetails(source []byte) []RecordingDetail {
	a, err := Parse("details.agent.md", source)
	if err != nil {
		return nil
	}
	if _, ok := a.Locations["Examples"]; !ok {
		return nil
	}
	lines := strings.Split(string(source), "\n")
	open := a.Locations["Examples"].Line
	for strings.TrimSpace(lines[open]) == "" {
		open++
	}
	marker, _, _ := openFence(strings.TrimSuffix(lines[open], "\r"))
	end := open + 1
	for !closeFence(strings.TrimSuffix(lines[end], "\r"), marker) {
		end++
	}
	var root yaml.Node
	if yaml.Unmarshal([]byte(strings.Join(lines[open+1:end], "\n")), &root) != nil {
		return nil
	}
	field := func(n *yaml.Node, key string) *yaml.Node {
		for i := 0; n != nil && i < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				return n.Content[i+1]
			}
		}
		return nil
	}
	var result []RecordingDetail
	for _, e := range field(root.Content[0], "examples").Content {
		mocks := field(e, "mocks")
		if mocks == nil {
			continue
		}
		for _, tool := range field(mocks, "tools").Content {
			calls := field(tool, "calls")
			if calls != nil && len(calls.Content) == 1 {
				raw, err := yaml.Marshal(calls.Content[0])
				if err == nil {
					result = append(result, RecordingDetail{field(e, "id").Value, field(tool, "name").Value, string(raw)})
				}
			}
		}
	}
	return result
}
