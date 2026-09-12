package proxy

// Validate an overlay before it is applied, against the committed table
// this replica booted from. Merge and Parse are the same functions used at
// startup; this endpoint stores nothing and changes nothing.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
)

const maxOverlayBytes = 256 << 10

type validateRequest struct {
	Fragments map[string]json.RawMessage `json:"fragments"`
}

type validateResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Include the resolved protocol, which determines how usage is metered.
	Upstreams []string `json:"upstreams,omitempty"`
}

func (h *handler) validateConfig(w http.ResponseWriter, r *http.Request) {
	var req validateRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOverlayBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, validateResponse{Error: "malformed request: " + err.Error()})
		return
	}
	frags := make([]config.Fragment, 0, len(req.Fragments))
	for name, raw := range req.Fragments {
		// Validation must not accept a key the boot path silently ignores.
		if !config.FragmentName(name) {
			writeJSON(w, http.StatusBadRequest, validateResponse{Error: fmt.Sprintf(
				"overlay key %q would be ignored at boot: a fragment must end in .json and may not begin with a dot", name)})
			return
		}
		frags = append(frags, config.Fragment{Name: name, Raw: raw})
	}
	sort.Slice(frags, func(i, j int) bool { return frags[i].Name < frags[j].Name })
	merged, err := config.Merge(h.d.ConfigBase, frags)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, validateResponse{Error: err.Error()})
		return
	}
	cfg, err := config.Parse(merged)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, validateResponse{Error: err.Error()})
		return
	}
	resp := validateResponse{OK: true}
	for name, up := range cfg.Upstreams {
		resp.Upstreams = append(resp.Upstreams, name+" ("+up.Protocol+")")
	}
	sort.Strings(resp.Upstreams)
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
