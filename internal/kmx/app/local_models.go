package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

type localModel struct {
	Provider string
	Model    string
	Endpoint string
	Size     int64
}

type localModelDetector interface {
	Detect(context.Context) ([]localModel, error)
}

type localModelEnvironment struct {
	Detectors   []localModelDetector
	Interactive func() bool
	Select      func([]localModel) (*localModel, error)
	Verify      func(*localModel) error
}

type ollamaDetector struct {
	client   *http.Client
	endpoint string
}

var localModelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

func (d ollamaDetector) Detect(ctx context.Context) ([]localModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.endpoint+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, nil // A runtime not listening is the ordinary no-match case.
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, nil
	}
	models := make([]localModel, 0, len(body.Models))
	for _, found := range body.Models {
		if name := strings.TrimSpace(found.Name); localModelName.MatchString(name) {
			models = append(models, localModel{Provider: "ollama", Model: name, Size: found.Size})
		}
	}
	return models, nil
}

func (a *App) defaultLocalModelEnvironment() *localModelEnvironment {
	return &localModelEnvironment{Detectors: []localModelDetector{ollamaDetector{
		client:   &http.Client{Timeout: 750 * time.Millisecond},
		endpoint: "http://127.0.0.1:11434",
	}}}
}

func (a *App) maybeSelectLocalModel(allowInteractive bool) error {
	if a.localModelsChecked {
		return nil
	}
	a.localModelsChecked = true
	if !allowInteractive || a.Cfg.ModelExplicit {
		return nil
	}
	env := a.localModels
	if env == nil {
		env = a.defaultLocalModelEnvironment()
	}
	interactive := a.localModelInteractive
	if env.Interactive != nil {
		interactive = env.Interactive
	}
	if !interactive() {
		return nil
	}

	// Discovery is intentionally host-local and read-only. Each provider gets
	// a bounded probe and returns only models already present; this phase never
	// starts a runtime or invokes a model-management/download endpoint.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var found []localModel
	for _, detector := range env.Detectors {
		models, err := detector.Detect(ctx)
		if err != nil {
			continue
		}
		found = append(found, models...)
	}
	if len(found) == 0 {
		return nil
	}

	var selected *localModel
	var err error
	if env.Select != nil {
		selected, err = env.Select(found)
	} else {
		selected, err = a.promptLocalModel(found)
	}
	if err != nil {
		return err
	}
	if selected != nil {
		choice := *selected
		a.selectedLocalModel = &choice
		a.notef("Will try installed local model %s/%s after the kind cluster is ready.", choice.Provider, choice.Model)
	}
	return nil
}

func (a *App) localModelInteractive() bool {
	errFile, errOK := a.Err.(*os.File)
	outFile, outOK := a.Out.(*os.File)
	return a.Stdin != nil && errOK && outOK && term.IsTerminal(int(a.Stdin.Fd())) &&
		term.IsTerminal(int(errFile.Fd())) && term.IsTerminal(int(outFile.Fd()))
}

func (a *App) promptLocalModel(found []localModel) (*localModel, error) {
	scanner := bufio.NewScanner(a.Stdin)
	if len(found) == 1 {
		fmt.Fprintf(a.Err, "Found installed local model %s/%s. Try to reuse it from kind instead of installing another model? [y/N]: ", found[0].Provider, found[0].Model)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
		case "y", "yes":
			return &found[0], nil
		case "", "n", "no":
			return nil, nil
		default:
			return nil, fmt.Errorf("expected y or n when selecting the detected model")
		}
	}

	fmt.Fprintln(a.Err, "Installed local models were detected:")
	for i, model := range found {
		fmt.Fprintf(a.Err, "  %d. %s/%s\n", i+1, model.Provider, model.Model)
	}
	fmt.Fprintln(a.Err, "  1. Install KMX's bundled model")
	for i, model := range found {
		fmt.Fprintf(a.Err, "  %d. Try %s/%s from kind\n", i+2, model.Provider, model.Model)
	}
	fmt.Fprintf(a.Err, "Select a model [1]: ")
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	answer := strings.TrimSpace(scanner.Text())
	if answer == "" {
		answer = "1"
	}
	choice, err := strconv.Atoi(answer)
	if err != nil || choice < 1 || choice > len(found)+1 {
		return nil, fmt.Errorf("model selection must be a number from 1 to %d", len(found)+1)
	}
	if choice == 1 {
		return nil, nil
	}
	return &found[choice-2], nil
}

func (a *App) verifySelectedLocalModel() {
	if a.selectedLocalModel == nil || a.localModelVerified {
		return
	}
	env := a.localModels
	if env == nil {
		env = a.defaultLocalModelEnvironment()
	}
	var err error
	if env.Verify != nil {
		err = env.Verify(a.selectedLocalModel)
	} else {
		err = a.verifyOllamaFromKind(a.selectedLocalModel)
	}
	if err != nil {
		a.notef("NOTE: host model reuse is unavailable from kind (%v); installing KMX's bundled model instead.", err)
		a.selectedLocalModel = nil
		return
	}
	a.localModelVerified = true
	a.Cfg.Model = a.selectedLocalModel.Model
	a.notef("Using installed local model %s/%s at %s.", a.selectedLocalModel.Provider, a.selectedLocalModel.Model, a.selectedLocalModel.Endpoint)
}

func (a *App) verifyOllamaFromKind(selected *localModel) error {
	node := a.Cfg.KindCluster + "-control-plane"
	gateway, err := a.Run.Capture(a.Cfg.ContainerEngine, "inspect", "--format", `{{(index .NetworkSettings.Networks "kind").Gateway}}`, node)
	if err != nil {
		gateway = ""
	}
	for _, endpoint := range ollamaKindEndpoints(a.Cfg.ContainerEngine, gateway) {
		body, probeErr := a.Run.Capture(a.Cfg.ContainerEngine, "exec", node, "curl", "-fsS", "--max-time", "2", endpoint+"/api/tags")
		if probeErr != nil {
			continue
		}
		if ollamaTagsContain([]byte(body), selected.Model) {
			selected.Endpoint = endpoint
			return nil
		}
	}
	return fmt.Errorf("no node-reachable Ollama endpoint reported model %s", selected.Model)
}

func ollamaKindEndpoints(engine, gateway string) []string {
	host := "host.docker.internal"
	if engine == "podman" {
		host = "host.containers.internal"
	}
	endpoints := []string{"http://" + host + ":11434"}
	if gateway = strings.TrimSpace(gateway); gateway != "" {
		endpoints = append(endpoints, "http://"+gateway+":11434")
	}
	return endpoints
}

func ollamaTagsContain(body []byte, model string) bool {
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &tags) != nil {
		return false
	}
	for _, found := range tags.Models {
		if strings.TrimSpace(found.Name) == model {
			return true
		}
	}
	return false
}

func (a *App) renderModelManifest(name string) ([]byte, error) {
	body, err := manifest(name)
	if err != nil {
		return nil, err
	}
	model, endpoint := a.Cfg.Model, "http://ollama.ollama.svc.cluster.local:11434"
	if a.selectedLocalModel != nil {
		model, endpoint = a.selectedLocalModel.Model, a.selectedLocalModel.Endpoint
	}
	rendered := string(body)
	for _, value := range []struct{ old, replacement string }{
		{"model: qwen2.5:3b", "model: " + model},
		{"http://ollama.ollama.svc.cluster.local:11434", endpoint},
	} {
		old, replacement := value.old, value.replacement
		if strings.Count(rendered, old) != 1 {
			return nil, fmt.Errorf("embedded k8s/%s does not contain exactly one expected %q value", name, old)
		}
		rendered = strings.Replace(rendered, old, replacement, 1)
	}
	return []byte(rendered), nil
}
