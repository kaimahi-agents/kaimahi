package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

const previewLifetime = 5 * time.Minute

// PreviewClient establishes one bounded, non-refreshing credential snapshot for
// all selected routes, atomically with catalog validation. Setup may exchange
// once to establish five minutes plus an expiry margin; inference never does.
// A nonnil capability returned with an error is content-check-only: callers must
// use it to withhold credential-bearing identities before reporting that error.
func (p *Provider) PreviewClient(ctx context.Context, routes []modelaccess.Route) (modelaccess.PreviewAccess, error) {
	if len(routes) == 0 {
		return nil, ErrModelUnavailable
	}
	for _, r := range routes {
		if r.Provider != "copilot" || r.Validate() != nil {
			return nil, ErrModelUnavailable
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	available, err := p.listFor(ctx, previewLifetime+30*time.Second)
	if p.source == "" {
		return nil, err
	}
	s := &preview{owner: p, generation: p.generation, ctx: ctx, until: p.now().Add(previewLifetime), source: p.source, bearer: p.bearer, expires: p.expires, api: p.api, http: p.http, now: p.now}
	if err == nil {
		for _, r := range routes {
			index := slices.IndexFunc(available, func(m catalogModel) bool { return m.route == r })
			if index < 0 {
				choices := make([]modelaccess.Route, 0, len(available))
				for _, model := range available {
					choices = append(choices, model.route)
				}
				err = unavailableWithChoices(choices)
				break
			}
			s.models = append(s.models, available[index])
		}
	}
	s.failed = err
	return s, err
}

type preview struct {
	mu                  sync.Mutex
	failed              error
	owner               *Provider
	generation          uint64
	ctx                 context.Context
	until, expires      time.Time
	source, bearer, api string
	http                *http.Client
	now                 func() time.Time
	models              []catalogModel
}

// Formatting must not accidentally export the private snapshot fields.
func (s *preview) String() string   { return "copilot preview access" }
func (s *preview) GoString() string { return "copilot preview access" }

func (s *preview) stop(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed == nil {
		s.failed = err
	}
	return err
}

// Content inspection remains usable after invalidation to wipe projections.
// Strings are already decoded: do not interpret authored backslashes as JSON.
func (s *preview) CheckContent(fields ...string) error {
	for _, v := range fields {
		if s.source != "" && strings.Contains(v, s.source) || s.bearer != "" && strings.Contains(v, s.bearer) {
			return s.stop(ErrCredentialContent)
		}
	}
	return nil
}

func (s *preview) Validate(ctx context.Context) error {
	s.mu.Lock()
	err := s.failed
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return s.stop(safeError(ctx.Err()))
	}
	if s.ctx.Err() != nil {
		return s.stop(safeError(s.ctx.Err()))
	}
	s.owner.mu.Lock()
	revoked := s.owner.generation != s.generation
	s.owner.mu.Unlock()
	if revoked || !s.now().Before(s.until) || !s.expires.IsZero() && !s.now().Before(s.expires.Add(-30*time.Second)) {
		return s.stop(ErrAuthRevoked)
	}
	return nil
}

func (s *preview) Client(r modelaccess.Route) (http.RoundTripper, error) {
	if err := s.Validate(s.ctx); err != nil {
		return nil, err
	}
	index := slices.IndexFunc(s.models, func(m catalogModel) bool { return m.route == r })
	if index < 0 {
		return nil, s.stop(ErrModelUnavailable)
	}
	return &previewTransport{access: s, route: r, path: s.models[index].path}, nil
}

type previewTransport struct {
	access *preview
	route  modelaccess.Route
	path   string
}

func (s *preview) checkJSON(b []byte) error {
	if !json.Valid(b) {
		return ErrMalformed
	}
	if err := s.CheckContent(string(b)); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	// Decoder tokens retain every string and key, including overwritten duplicate
	// values that an unmarshal into a map would discard.
	for {
		token, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return ErrMalformed
		}
		if v, ok := token.(string); ok {
			if err := s.CheckContent(v); err != nil {
				return err
			}
		}
	}
}

func previewModel(b []byte) (string, error) {
	model, err := requestModel(b)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil {
		return "", ErrMalformed
	}
	if string(bytes.TrimSpace(fields["stream"])) != "false" {
		return "", ErrMalformed
	}
	// requestModel already rejects duplicate exact keys. Refuse stream aliases
	// too, rather than relying on upstream case folding or boolean coercion.
	for key, value := range fields {
		if strings.EqualFold(key, "stream") && (key != "stream" || string(bytes.TrimSpace(value)) != "false") {
			return "", ErrMalformed
		}
	}
	return model, nil
}

func (t *previewTransport) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	s := t.access
	defer func() {
		if err != nil {
			s.stop(err)
		}
	}()
	if req == nil {
		return nil, ErrMalformed
	}
	if req.Body != nil {
		defer req.Body.Close()
	}
	if err = s.Validate(req.Context()); err != nil {
		return nil, err
	}
	if req.URL == nil || req.Method != "POST" || req.URL.String() != endpoint+"/chat/completions" {
		return nil, ErrModelUnavailable
	}
	if req.Body == nil {
		return nil, ErrMalformed
	}
	b, err := readBounded(req.Body, 4<<20)
	if err != nil {
		return nil, err
	}
	if err = s.checkJSON(b); err != nil {
		return nil, err
	}
	model, err := previewModel(b)
	if err != nil {
		return nil, err
	}
	if model != t.route.Model {
		return nil, ErrModelUnavailable
	}
	ctx, cancel := context.WithDeadline(req.Context(), s.until)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	// A private catalog owner has no store or refresh path. Catalog and inference
	// use only the pinned bearer, regardless of ordinary List activity.
	pinned := &Provider{http: s.http, api: s.api, bearer: s.bearer}
	cb, status, err := pinned.catalog(ctx)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, classify(status, cb)
	}
	if err = s.checkJSON(cb); err != nil {
		return nil, err
	}
	available, err := catalogRoutes(cb, s.source, s.bearer)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(available, func(m catalogModel) bool { return m.route == t.route && slices.Contains(m.paths, t.path) }) {
		return nil, ErrModelUnavailable
	}
	if err = s.Validate(ctx); err != nil {
		return nil, err
	}
	if t.path == "/responses" {
		var stream bool
		b, stream, err = encodeResponsesRequest(b)
		if err != nil {
			return nil, err
		}
		if stream {
			return nil, ErrMalformed
		}
		// Translation introduces keys/constants; none bypass the provider check.
		if err = s.checkJSON(b); err != nil {
			return nil, err
		}
	}
	next := req.Clone(ctx)
	next.URL, err = url.Parse(s.api + t.path)
	if err != nil {
		return nil, ErrUnavailable
	}
	next.Host = ""
	next.Body = io.NopCloser(bytes.NewReader(b))
	next.ContentLength = int64(len(b))
	next.GetBody = nil
	next.Header.Del("Idempotency-Key")
	next.Header.Del("X-Idempotency-Key")
	next.Header.Del("Proxy-Authorization")
	if t.path == "/responses" {
		next.Header.Del("Accept-Encoding")
		next.Header.Del("Content-Encoding")
	}
	headers(next, s.bearer)
	next.Header.Set("Content-Type", "application/json")
	next.Header.Set("Accept", "application/json")
	next.Header.Set("Openai-Intent", "conversation-panel")
	upstream := s.http.Transport
	if upstream == nil {
		upstream = http.DefaultTransport
	}
	resp, err = upstream.RoundTrip(next)
	if err != nil {
		return nil, safeError(err)
	}
	defer resp.Body.Close()
	limit := 4 << 20
	if t.path == "/responses" {
		limit = responseLimit
	}
	reply, err := readBounded(resp.Body, limit)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classify(resp.StatusCode, reply)
	}
	if err = s.checkJSON(reply); err != nil {
		return nil, err
	}
	if t.path == "/responses" {
		// Inspect discarded fields before adapting, then inspect joined fragments.
		checked := &http.Response{Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(reply))}
		defer func() { _ = checked.Body.Close() }()
		if err = adaptResponsesResponse(checked, false); err != nil {
			return nil, err
		}
		reply, err = readBounded(checked.Body, responseLimit)
		if err != nil {
			return nil, err
		}
		if err = s.checkJSON(reply); err != nil {
			return nil, err
		}
	}
	if err = s.Validate(ctx); err != nil {
		return nil, err
	}
	// Never export upstream status text, headers, trailers, TLS metadata or the
	// authenticated request. Only checked JSON and fixed response metadata leave.
	return &http.Response{StatusCode: resp.StatusCode, Status: http.StatusText(resp.StatusCode), Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(reply)), ContentLength: int64(len(reply)), Request: &http.Request{Method: req.Method, URL: req.URL, Header: make(http.Header)}}, nil
}
