package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// DiscoveryOptions selects one progressive read scope, never a remembered target.
type DiscoveryOptions struct{ Context, Namespace string }
type DiscoveryCheck struct {
	Scope  string `json:"scope"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}
type DiscoveryRow struct {
	Context   string `json:"context"`
	Current   *bool  `json:"current,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	UID       string `json:"uid,omitempty"`
}
type DiscoveryReport struct {
	Scope    string           `json:"scope"`
	Complete bool             `json:"complete"`
	Checks   []DiscoveryCheck `json:"checks"`
	Rows     []DiscoveryRow   `json:"rows"`
 Target *DiscoveryTarget `json:"target,omitempty"`
}

// Go quoting is decoded as Go quoting, not incorrectly treated as JSON. Names
// containing newlines cannot create extra records. No credential/server fields
// enter stdout, even temporarily.
const discoveryContextTemplate = `{{with index . "current-context"}}{{printf "%q\n" .}}{{else}}{{printf "%q\n" ""}}{{end}}{{range .contexts}}{{printf "%q\n" .name}}{{end}}`

var discoveryNamespaceName = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

// DiscoveryError projects only fixed vocabulary; raw capture causes stay private.
func DiscoveryError(err error) *MachineError {
	var safe *MachineError
	if errors.As(err, &safe) {
		return safe
	}
	var read *discoveryReadFailure
	if errors.As(err, &read) {
		return NewMachineError(read.safeCode(), "cluster")
	}
	return NewMachineError("operation_failed", "cluster")
}
func (r *DiscoveryReport) problem(reason discoveryReadReason) {
	r.Complete = false
	if len(r.Checks) == 0 {
		r.Checks = append(r.Checks, DiscoveryCheck{r.Scope, "unknown", string(reason)})
	}
}
func (r *DiscoveryReport) add(row DiscoveryRow) bool {
	if len(r.Rows) >= 128 {
		r.problem("read_unknown")
		return false
	}
	candidate := *r
	candidate.Rows = append(append([]DiscoveryRow(nil), r.Rows...), row)
	// Reserve the largest fixed check before accepting a row, so an overflow
	// observation never makes the already retained report too large to emit.
	candidate.Complete = false
	candidate.Checks = []DiscoveryCheck{{r.Scope, "unknown", "network_unavailable"}}
	encoded, _ := json.Marshal(candidate)
	if len(encoded) > 60<<10 {
		r.problem("read_unknown")
		return false
	}
	r.Rows = append(r.Rows, row)
	return true
}

// Discover performs only bounded local context projection or context-pinned
// namespace GETs. Completeness is an observation, not runtime/evidence authority.
func (a *App) Discover(parent context.Context, opt DiscoveryOptions) (DiscoveryReport, error) {
	r := DiscoveryReport{Scope: "contexts", Complete: true, Checks: []DiscoveryCheck{}, Rows: []DiscoveryRow{}}
	if opt.Context != "" {
		r.Scope = "namespaces"
	}
	if opt.Context != "" && !statusSafeText(opt.Context) {
		r.problem("malformed")
		return r, NewMachineError("invalid_arguments", "preflight")
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
 if opt.Namespace!="" {return a.discoverTarget(ctx,opt)}
	var failure *discoveryReadFailure
	if opt.Context == "" {
		raw, readErr := a.discoveryCapture(ctx, "", "config", "view", "-o", "go-template="+discoveryContextTemplate)
		if len(raw) == 0 {
			if readErr == nil {
				r.problem("malformed")
			}
		} else {
			if !strings.HasSuffix(string(raw), "\n") {
				r.problem("malformed")
			}
			lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
			current := ""
			seen := map[string]bool{}
			for i, line := range lines {
				name, err := strconv.Unquote(line)
				if err != nil || !strings.HasPrefix(line, `"`) || !utf8.ValidString(name) {
					r.problem("malformed")
					continue
				}
				if i == 0 {
					if name != "" && !statusSafeText(name) {
						r.problem("malformed")
					} else {
						current = name
					}
					continue
				}
				if !statusSafeText(name) || seen[name] {
					r.problem("malformed")
					continue
				}
				seen[name] = true
				isCurrent := name == current
				if !r.add(DiscoveryRow{Context: name, Current: &isCurrent}) {
					break
				}
			}
		}
		failure = readErr
	} else {
		var cursor, version string
		total := 0
		seenCursor := map[string]bool{}
		seenName, seenUID := map[string]bool{}, map[string]bool{}
		for page := 0; page < 32; page++ {
			query := url.Values{"limit": {"128"}}
			if cursor != "" {
				query.Set("continue", cursor)
			}
			raw, readErr := a.discoveryCapture(ctx, opt.Context, "get", "--raw", "/api/v1/namespaces?"+query.Encode())
			total += len(raw)
			if total > 8<<20 {
				r.problem("read_unknown")
				break
			}
			// Project exact Kubernetes map keys. A typed encoding/json decode
			// would let an unknown case-folded alias override canonical fields.
			var list map[string]any
			var pageCursor string
			if len(raw) > 0 {
				if !statusDecode(raw, &list) || list["apiVersion"] != "v1" || list["kind"] != "NamespaceList" {
					r.problem("malformed")
					break
				}
				items, ok := list["items"].([]any)
				if !ok {
					r.problem("malformed")
					break
				}
				metadata := statusMap(list, "metadata")
				if value, present := list["metadata"]; present {
					if _, ok := value.(map[string]any); !ok {
						r.problem("malformed")
						break
					}
				}
				pageVersion, versionOK := metadata["resourceVersion"].(string)
				_, hasVersion := metadata["resourceVersion"]
				var cursorOK bool
				pageCursor, cursorOK = metadata["continue"].(string)
				_, hasCursor := metadata["continue"]
				if (hasVersion && !versionOK) || (hasCursor && !cursorOK) {
					r.problem("malformed")
					break
				}
				if page > 0 && (version == "" || pageVersion != version) {
					r.problem("read_unknown")
					break
				}
				version = pageVersion
				for _, rawEntry := range items {
					entry, ok := rawEntry.(map[string]any)
					kind, hasKind := entry["kind"]
					apiVersion, hasVersion := entry["apiVersion"]
					m := statusMap(entry, "metadata")
					name, uid := statusString(m, "name"), statusString(m, "uid")
					if !ok || (hasKind && kind != "Namespace") || (hasVersion && apiVersion != "v1") || len(name) > 63 || !discoveryNamespaceName.MatchString(name) || !statusSafeText(name) || !statusOpaque(uid) || seenName[name] || seenUID[uid] {
						r.problem("malformed")
						continue
					}
					seenName[name], seenUID[uid] = true, true
					if !r.add(DiscoveryRow{Context: opt.Context, Namespace: name, UID: uid}) {
						break
					}
				}
			}
			if readErr != nil {
				failure = readErr
				break
			}
			if len(raw) == 0 {
				r.problem("malformed")
				break
			}
			if !r.Complete {
				break
			}
			cursor = pageCursor
			if cursor == "" {
				break
			}
			if seenCursor[cursor] || version == "" || page == 31 {
				r.problem("read_unknown")
				break
			}
			seenCursor[cursor] = true
		}
	}
	// Decoding and projection also consume the operation budget. Cancellation
	// after a successful subprocess must still veto a complete observation.
	if err := ctx.Err(); err != nil {
		failure = &discoveryReadFailure{reason: "read_unknown", cause: err}
		r.Complete = false
		r.Checks = []DiscoveryCheck{{r.Scope, "unknown", "read_unknown"}}
	}
	if failure != nil {
		// Only an exact, prerequisite-qualified read can claim missing. This PR
		// never performs such reads; even API/namespace NotFound is unknown.
		if failure.reason == "resource_missing" {
			failure = &discoveryReadFailure{reason: "read_unknown", cause: failure.cause}
		}
		r.problem(failure.reason)
	}
	sort.SliceStable(r.Rows, func(i, j int) bool {
		if r.Rows[i].Context != r.Rows[j].Context {
			return r.Rows[i].Context < r.Rows[j].Context
		}
		return r.Rows[i].Namespace < r.Rows[j].Namespace
	})
	if !r.Complete {
		if failure == nil {
			failure = &discoveryReadFailure{reason: discoveryReadReason(r.Checks[0].Reason)}
		}
		return r, failure
	}
	return r, nil
}
