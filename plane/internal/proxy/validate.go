package proxy

// Validating an overlay BEFORE it is applied.
//
// Until now a malformed upstream entry was discovered by applying it,
// rolling the proxy, and watching the new pod refuse to boot — a real
// control (maxUnavailable: 0 keeps the old replicas serving) but a poor
// one to learn from, because it happens after the mutation, in a
// rollout, to an operator who is no longer looking.
//
// This endpoint moves the same decision earlier WITHOUT writing a second
// validator: it merges the candidate overlay over the base table the
// running proxy booted from and calls config.Parse — the one function
// `main` loads with. If Parse accepts it here, the pod that reads it
// will accept it too, because it is literally the same code in the same
// binary. Nothing is stored and nothing is changed; this is a read.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
)

// maxOverlayBytes bounds the submitted overlay. The committed table is
// around 3KB; an overlay is one entry per onboarded server.
const maxOverlayBytes = 256 << 10

type validateRequest struct {
	// Fragments is filename -> the fragment's JSON, exactly as the
	// overlay ConfigMap would carry it.
	Fragments map[string]json.RawMessage `json:"fragments"`
}

type validateResponse struct {
	OK bool `json:"ok"`
	// Error is Parse's own message, verbatim — the same line the pod
	// would have logged before exiting.
	Error string `json:"error,omitempty"`
	// ToolUpstreams is every tool upstream the merged table would carry,
	// so an operator can see their entry take its place beside the
	// committed ones rather than in place of one.
	ToolUpstreams []string `json:"tool_upstreams,omitempty"`
	// Declared echoes back what the plane understood each newly declared
	// tool's policy-relevant fields to be. An empty list is a real
	// answer (a verb-level binding); a tool absent from this map binds
	// its whole canonical argument object.
	Declared map[string][]string `json:"declared,omitempty"`
	// TableDeclared is every tool's policy fields in the MERGED table,
	// not only the submitted overlay's. A caller declaring a workflow
	// against this plane states the declarations it depends on and
	// has to be able to check them; without this it would either bind
	// whatever happened to be configured, or have to merge the table a
	// second time somewhere else — and there is exactly one merge.
	TableDeclared map[string][]string `json:"table_declared,omitempty"`
	// AlreadyAllowlisted maps a tool the overlay declares to the
	// credentials that ALREADY allowlist that name. The gateway's
	// allowlist is per-credential and not per-(credential, upstream), so
	// each of those credentials can call the tool on this new server the
	// moment it is in the table — with no allowlist change and nothing
	// else to notice it. Empty is the ordinary case.
	AlreadyAllowlisted map[string][]string `json:"already_allowlisted,omitempty"`
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
		// Refuse, rather than filter, a key the BOOT path would ignore.
		// Filtering here would validate a table the plane is never going
		// to read — and the docs invite hand-editing the overlay to add
		// standing constraints, so "my constraint is in the ConfigMap
		// and validated" must not be compatible with "the plane drops
		// it without a word".
		if !config.FragmentName(name) {
			writeJSON(w, http.StatusBadRequest, validateResponse{Error: fmt.Sprintf(
				"overlay key %q would be ignored at boot: a fragment must end in .json and may not begin with a dot",
				name)})
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
	resp := validateResponse{OK: true, Declared: map[string][]string{}, TableDeclared: cfg.Policy().AllDeclared()}
	for name := range cfg.ToolUpstreams {
		resp.ToolUpstreams = append(resp.ToolUpstreams, name)
	}
	sort.Strings(resp.ToolUpstreams)
	// Only the tools the submitted overlay declares — the committed
	// table's declarations are not this answer's business.
	for _, f := range frags {
		var frag struct {
			ToolUpstreams map[string]struct {
				Tools map[string]json.RawMessage `json:"tools"`
			} `json:"tool_upstreams"`
		}
		if json.Unmarshal(f.Raw, &frag) != nil {
			continue
		}
		for _, up := range frag.ToolUpstreams {
			for tool := range up.Tools {
				fields, ok := cfg.Policy().Declared(tool)
				if !ok {
					continue
				}
				if fields == nil {
					fields = []string{}
				}
				resp.Declared[tool] = fields
			}
		}
	}
	declared := make([]string, 0, len(resp.Declared))
	for tool := range resp.Declared {
		declared = append(declared, tool)
	}
	sort.Strings(declared)
	if existing, err := h.d.Store.CredentialsAllowlisting(r.Context(), declared); err == nil {
		if len(existing) > 0 {
			resp.AlreadyAllowlisted = existing
		}
	} else {
		// A store that cannot answer must not read as "nobody has it":
		// that is the direction that produces a reassuring silence.
		slog.Error("admin: allowlist collision check", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, validateResponse{Error: "cannot read the existing allowlists: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
