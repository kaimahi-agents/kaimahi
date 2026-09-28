package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// Agent locations contain routing metadata, never model credential values.
// A private kubeconfig snapshot preserves AKS exec authentication and survives
// deletion of the temporary kubeconfig used during lift.
type agentLocation struct {
	Agent         string `json:"agent"`
	Namespace     string `json:"namespace"`
	Context       string `json:"context"`
	Kubeconfig    string `json:"kubeconfig,omitempty"`
	Cluster       string `json:"cluster,omitempty"`
	Subscription  string `json:"subscription,omitempty"`
	ResourceGroup string `json:"resourceGroup,omitempty"`
}

func agentLocationsDir() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agentconfigs"), nil
}

func (l agentLocation) key() string { return l.Context + "\x00" + l.Namespace + "\x00" + l.Agent }

func loadAgentLocations() ([]agentLocation, error) {
	dir, err := agentLocationsDir()
	if err != nil {
		return nil, err
	}
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var locations []agentLocation
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			return nil, err
		}
		var location agentLocation
		if err = json.Unmarshal(raw, &location); err != nil {
			return nil, fmt.Errorf("invalid agent location %s", file.Name())
		}
		if location.Agent == "" || location.Namespace == "" || location.Context == "" {
			return nil, fmt.Errorf("incomplete agent location %s", file.Name())
		}
		locations = append(locations, location)
	}
	sort.Slice(locations, func(i, j int) bool { return locations[i].key() < locations[j].key() })
	return locations, nil
}

func writeAgentLocation(location agentLocation, kubeconfig []byte) (agentLocation, error) {
	dir, err := agentLocationsDir()
	if err != nil {
		return location, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return location, err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(location.key())))
	if len(kubeconfig) > 0 {
		location.Kubeconfig = filepath.Join(dir, key+".kubeconfig")
		if err = writePrivateAgentFile(location.Kubeconfig, kubeconfig); err != nil {
			return location, err
		}
	}
	raw, err := json.MarshalIndent(location, "", "  ")
	if err != nil {
		return location, err
	}
	return location, writePrivateAgentFile(filepath.Join(dir, key+".json"), raw)
}

func writePrivateAgentFile(path string, raw []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".agent-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	// Go's Windows implementation uses MoveFileEx with REPLACE_EXISTING;
	// do not delete the destination first and introduce a missing-file window.
	return os.Rename(file.Name(), path)
}

func rememberAgentLocation(ctx context.Context, a *App, location agentLocation) (agentLocation, error) {
	raw, err := a.orkaCapture(ctx, nil, "config", "view", "--minify", "--flatten", "--raw", "-o", "yaml")
	if err != nil {
		return location, fmt.Errorf("cannot persist kubeconfig for Agent %s: %w", location.Agent, err)
	}
	return writeAgentLocation(location, raw)
}

func appAtAgentLocation(source *App, location agentLocation) *App {
	app := *source
	cfg := *source.Cfg
	runner := *source.Run
	cfg.KubeContext, cfg.ContextSource = location.Context, config.SourceFlag
	runner.Env = append([]string(nil), runner.Env...)
	if location.Kubeconfig != "" {
		filtered := runner.Env[:0]
		for _, env := range runner.Env {
			if !strings.HasPrefix(env, "KUBECONFIG=") {
				filtered = append(filtered, env)
			}
		}
		runner.Env = append(filtered, "KUBECONFIG="+location.Kubeconfig)
	}
	app.Cfg, app.Run = &cfg, &runner
	app.chatClusterName = location.Cluster
	app.guarded = false
	return &app
}

func (b *orkaChatBackend) connectAgentLocation(ctx context.Context, location agentLocation) error {
	app := appAtAgentLocation(b.app, location)
	raw, err := app.orkaCapture(ctx, nil, "-n", location.Namespace, "get", "agents.core.orka.ai", location.Agent, "-o", "json")
	if err != nil {
		return err
	}
	var object orkaObject
	if json.Unmarshal(raw, &object) != nil || object.Metadata.Name != location.Agent || object.Metadata.Namespace != location.Namespace || object.Metadata.UID == "" || object.Metadata.Generation < 1 {
		return fmt.Errorf("selected Agent returned no valid identity")
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err = app.waitOrkaReady(waitCtx, location.Namespace, orkaIdentity{Kind: "Agent", Name: location.Agent, UID: object.Metadata.UID, Generation: object.Metadata.Generation}); err != nil {
		return err
	}
	b.Close()
	app.chatInference = "local" // Use the selected cluster's configured Provider.
	b.app, b.agent, b.namespace = app, location.Agent, location.Namespace
	return nil
}
