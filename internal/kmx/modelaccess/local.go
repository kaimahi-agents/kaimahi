package modelaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type State string

const (
	Absent    State = "absent"
	Empty     State = "empty"
	Installed State = "installed"
	Failure   State = "failure"
)

type Model struct {
	Route     Route `json:"route"`
	SizeBytes int64 `json:"sizeBytes,omitempty"`
}
type Report struct {
	Provider string  `json:"provider"`
	State    State   `json:"state"`
	Models   []Model `json:"models"`
	Reason   string  `json:"reason,omitempty"`
	Install  Install `json:"install"`
}

// LocalDiscovery's zero value uses bounded, read-only local probes. Overrides
// support callers' explicit local endpoint and hermetic process/HTTP fixtures.
type LocalDiscovery struct {
	LookPath       func(string) (string, error)
	Run            func(context.Context, string, ...string) ([]byte, error)
	Client         *http.Client
	OllamaEndpoint string
}

const probeLimit = 1 << 20
const probeTimeout = 2 * time.Second

func Discover(ctx context.Context) []Report {
	d := LocalDiscovery{}
	return []Report{d.Foundry(ctx), d.Ollama(ctx)}
}
func report(provider string) Report {
	return Report{Provider: provider, State: Failure, Models: []Model{}, Install: InstallGuidance(provider, runtime.GOOS, runtime.GOARCH)}
}
func (d LocalDiscovery) lookPath(name string) (string, error) {
	if d.LookPath != nil {
		return d.LookPath(name)
	}
	return exec.LookPath(name)
}
func (d LocalDiscovery) run(ctx context.Context, path string, args ...string) ([]byte, error) {
	if d.Run != nil {
		return d.Run(ctx, path, args...)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = 100 * time.Millisecond
	out := &boundedOutput{}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("runtime status unavailable")
	}
	return out.Bytes(), nil
}

type boundedOutput struct{ buffer bytes.Buffer }

func (b *boundedOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > probeLimit {
		return 0, errors.New("runtime status exceeds limit")
	}
	return b.buffer.Write(p)
}
func (d LocalDiscovery) read(ctx context.Context, endpoint string) ([]byte, error) {
	client := http.Client{Timeout: probeTimeout}
	if d.Client != nil {
		client = *d.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("invalid runtime endpoint")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("runtime inventory request refused")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, probeLimit+1))
	if err != nil || len(b) > probeLimit {
		return nil, errors.New("runtime inventory exceeds limit or could not be read")
	}
	return b, nil
}
func (d LocalDiscovery) Ollama(parent context.Context) Report {
	return d.ollama(parent, false)
}

func (d LocalDiscovery) ollama(parent context.Context, legacy bool) Report {
	r := report("ollama")
	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()
	endpoint := d.OllamaEndpoint
	if endpoint == "" {
		endpoint = "http://127.0.0.1:11434"
	}
	base, err := cleanBase(endpoint)
	if err != nil {
		r.Reason = "invalid local Ollama endpoint"
		return r
	}
	b, err := d.read(ctx, base+"/api/tags")
	if err != nil {
		_, pathErr := d.lookPath("ollama")
		if errors.Is(pathErr, exec.ErrNotFound) && (errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.Errno(10061))) {
			r.State = Absent
			r.Reason = "Ollama is not installed or listening"
		} else {
			r.Reason = "Ollama inventory unavailable; start the runtime yourself and retry"
		}
		return r
	}
	var body struct {
		Models *[]struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &body) != nil || body.Models == nil {
		r.Reason = "invalid Ollama inventory"
		return r
	}
	for _, m := range *body.Models {
		if legacy {
			m.Name = strings.TrimSpace(m.Name)
		}
		if !modelName.MatchString(m.Name) || m.Size < 0 {
			if legacy {
				continue
			}
			r.Reason = "invalid Ollama model entry"
			r.Models = []Model{}
			return r
		}
		r.Models = append(r.Models, Model{Route: Route{Provider: r.Provider, Model: m.Name, Endpoint: base + "/v1", Auth: "none"}, SizeBytes: m.Size})
	}
	r.State = Installed
	if len(r.Models) == 0 {
		r.State = Empty
		r.Reason = "no installed Ollama models; choose a model to download yourself"
	}
	return r
}

var statusURLs = regexp.MustCompile(`https?://[^\s<>\x22]+`)

func (d LocalDiscovery) Foundry(parent context.Context) Report {
	r := report("foundry-local")
	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()
	path, err := d.lookPath("foundry")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			r.State = Absent
			r.Reason = "Foundry Local is not installed"
		} else {
			r.Reason = "Foundry Local executable unavailable"
		}
		return r
	}
	b, err := d.run(ctx, path, "server", "status")
	if err != nil || len(b) > probeLimit {
		r.Reason = "Foundry Local status unavailable; start the runtime yourself and retry"
		return r
	}
	candidates := statusURLs.FindAllString(string(b), -1)
	bases := map[string]bool{}
	for _, s := range candidates {
		base, e := cleanBase(strings.TrimRight(s, ".,;"))
		if e != nil {
			continue // Status may include unrelated documentation URLs.
		}
		bases[base] = true
	}
	if len(bases) != 1 {
		r.Reason = "Foundry Local did not report one usable endpoint"
		return r
	}
	var base string
	for s := range bases {
		base = s
	}
	b, err = d.read(ctx, base+"/openai/models")
	if err != nil {
		r.Reason = "Foundry Local cached inventory unavailable"
		return r
	}
	var models []string
	if json.Unmarshal(b, &models) != nil || models == nil {
		r.Reason = "invalid Foundry Local cached inventory"
		return r
	}
	for _, name := range models {
		if !modelName.MatchString(name) {
			r.Reason = "invalid Foundry Local model entry"
			r.Models = []Model{}
			return r
		}
		r.Models = append(r.Models, Model{Route: Route{Provider: r.Provider, Model: name, Endpoint: base + "/v1", Auth: "none"}})
	}
	r.State = Installed
	if len(r.Models) == 0 {
		r.State = Empty
		r.Reason = "no cached Foundry Local models; choose a model to download yourself"
	}
	return r
}

// DetectOllama exposes only successful inventory to the legacy kind-reuse path.
// Day-0 callers use the full Report and never reinterpret failure as absence.
func DetectOllama(ctx context.Context, client *http.Client, endpoint string) ([]Model, error) {
	r := (LocalDiscovery{Client: client, OllamaEndpoint: endpoint}).ollama(ctx, true)
	if r.State == Failure {
		return nil, errors.New("Ollama inventory unavailable")
	}
	return r.Models, nil
}
