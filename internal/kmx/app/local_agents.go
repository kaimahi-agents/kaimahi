package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"go.yaml.in/yaml/v3"
)

const localAgentSchema = 1

type LocalAgentInstance struct {
	SchemaVersion    int        `json:"schemaVersion"`
	EventID          string     `json:"eventId"`
	Sequence         uint64     `json:"sequence"`
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Source           string     `json:"source"`
	AgentRevision    string     `json:"agentRevision"`
	SnapshotRevision string     `json:"snapshotRevision"`
	TestsState       string     `json:"testsState"`
	TestsRevision    string     `json:"testsRevision,omitempty"`
	TestsPath        string     `json:"testsPath,omitempty"`
	TestsExplicit    bool       `json:"testsExplicit,omitempty"`
	CachedAgent      string     `json:"cachedAgent"`
	CachedTests      string     `json:"cachedTests,omitempty"`
	Provider         string     `json:"provider"`
	Model            string     `json:"model"`
	Started          time.Time  `json:"started"`
	Stopped          *time.Time `json:"stopped,omitempty"`
	State            string     `json:"state"`
}

type localAgentStop struct {
	SchemaVersion int       `json:"schemaVersion"`
	EventID       string    `json:"eventId"`
	Stopped       time.Time `json:"stopped"`
}

func LocalAgentID(source string) string {
	sum := sha256.Sum256([]byte(source))
	return fmt.Sprintf("md-%x", sum[:16])
}

func validLocalAgentID(id string) bool {
	return validHexValue(strings.TrimPrefix(id, "md-"), 32) && strings.HasPrefix(id, "md-")
}
func validDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validHexValue(value[7:], 64)
}
func validHexValue(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return false
		}
	}
	return true
}

func localAgentDirectories(ctx context.Context, create bool) (*fileaccess.Directory, error) {
	root, err := config.ExistingStateDir()
	if create {
		root, err = config.PrepareStateDir()
	}
	if err != nil {
		return nil, err
	}
	base, err := fileaccess.OpenPrivateDirectory(ctx, root)
	if err != nil {
		return nil, err
	}
	open := fileaccess.OpenDirectoryAt
	if create {
		open = fileaccess.EnsurePrivateDirectoryAt
	}
	local, err := open(ctx, base, "local-agents")
	_ = base.Close()
	if err != nil {
		return nil, err
	}
	if err := fileaccess.CheckPrivate(ctx, local, true); err != nil {
		_ = local.Close()
		return nil, err
	}
	instances, err := open(ctx, local, "instances")
	_ = local.Close()
	if err == nil {
		err = fileaccess.CheckPrivate(ctx, instances, true)
		if err != nil {
			_ = instances.Close()
			instances = nil
		}
	}
	return instances, err
}

func localAgentInstanceDirectory(ctx context.Context, id string, create bool) (*fileaccess.Directory, string, error) {
	if !validLocalAgentID(id) {
		return nil, "", errors.New("invalid local agent identity")
	}
	instances, err := localAgentDirectories(ctx, create)
	if err != nil {
		return nil, "", err
	}
	open := fileaccess.OpenDirectoryAt
	if create {
		open = fileaccess.EnsurePrivateDirectoryAt
	}
	dir, err := open(ctx, instances, id)
	_ = instances.Close()
	root, _ := config.StateDir()
	return dir, filepath.Join(root, "local-agents", "instances", id), err
}

func readPrivateBytes(ctx context.Context, dir *fileaccess.Directory, name string, limit int64) ([]byte, error) {
	file, err := fileaccess.OpenPrivateFileAt(ctx, dir, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("private state file exceeds its limit")
	}
	return raw, nil
}

func withLocalAgentTransition(ctx context.Context, dir *fileaccess.Directory, work func() error) error {
	lock, err := fileaccess.EnsurePrivateFileAt(ctx, dir, "transition.lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := fileaccess.TryLock(ctx, lock); err == nil {
			break
		} else if !errors.Is(err, fileaccess.ErrUnsafe) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("local agent lifecycle is busy")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return work()
}

func ensureLocalAgentCapacity(dir *fileaccess.Directory, names ...string) error {
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, entry := range entries {
		present[entry.Name()] = true
	}
	count := len(entries)
	for _, name := range names {
		if !present[name] {
			count++
			present[name] = true
		}
	}
	if count > 1024 {
		return errors.New("local agent lifecycle history reached its entry limit")
	}
	return nil
}

func strictLocalAgentJSON(raw []byte, target any) error {
	var node yaml.Node
	if yaml.Unmarshal(raw, &node) != nil || len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return errors.New("invalid local agent JSON")
	}
	seen := map[string]bool{}
	for i := 0; i < len(node.Content[0].Content); i += 2 {
		key := node.Content[0].Content[i].Value
		if seen[key] {
			return errors.New("duplicate local agent JSON field")
		}
		seen[key] = true
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return errors.New("trailing local agent JSON")
	}
	return nil
}

func publishPrivateBytes(ctx context.Context, dir *fileaccess.Directory, name string, raw []byte) error {
	result, err := fileaccess.PublishPrivate(ctx, dir, name, raw)
	if err == nil || result.Visible {
		return err
	}
	if existing, readErr := readPrivateBytes(ctx, dir, name, int64(len(raw))); readErr == nil && string(existing) == string(raw) {
		return nil
	}
	return err
}

func cachePair(ctx context.Context, dir *fileaccess.Directory, dirPath string, pair *agentfile.Pair) (string, string, error) {
	digest := strings.TrimPrefix(pair.SnapshotRevision, "sha256:")
	if !validDigest(pair.SnapshotRevision) {
		return "", "", errors.New("invalid local agent snapshot identity")
	}
	copyOne := func(source, revision, name string) (string, error) {
		parent, err := fileaccess.OpenDirectory(ctx, filepath.Dir(source))
		if err != nil {
			return "", err
		}
		file, err := fileaccess.OpenRegularAt(ctx, parent, filepath.Base(source))
		_ = parent.Close()
		if err != nil {
			return "", err
		}
		raw, err := io.ReadAll(io.LimitReader(file, agentfile.MaxFileBytes+1))
		err = errors.Join(err, file.Close())
		if err != nil || len(raw) > agentfile.MaxFileBytes || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != revision {
			return "", errors.New("agent source changed while persisting")
		}
		if err := publishPrivateBytes(ctx, dir, name, raw); err != nil {
			return "", err
		}
		return filepath.Join(dirPath, name), nil
	}
	agent, err := copyOne(pair.AgentPath, pair.Agent.Revision, "snapshot-"+digest+".agent.md")
	if err != nil {
		return "", "", err
	}
	tests := ""
	if pair.Tests.State == "external" {
		tests, err = copyOne(pair.Tests.Selection.Path, pair.Tests.Revision, "snapshot-"+digest+".agent.test.md")
	}
	return agent, tests, err
}

func randomEventID() (string, error) {
	raw := make([]byte, 12)
	_, err := rand.Read(raw)
	return hex.EncodeToString(raw), err
}

func ActivateLocalAgent(ctx context.Context, _ string, pair *agentfile.Pair, provider, _ string, model string) (LocalAgentInstance, bool, error) {
	if pair == nil || pair.Agent == nil {
		return LocalAgentInstance{}, false, errors.New("local agent requires a checked agent file")
	}
	id := LocalAgentID(pair.AgentPath)
	dir, path, err := localAgentInstanceDirectory(ctx, id, true)
	if err != nil {
		return LocalAgentInstance{}, false, err
	}
	defer dir.Close()
	var result LocalAgentInstance
	var created bool
	err = withLocalAgentTransition(ctx, dir, func() error {
		if current, err := resolveLocalAgent(id, true); err == nil && current.State == "active" {
			if current.SnapshotRevision != pair.SnapshotRevision || current.Provider != provider || current.Model != model {
				return errors.New("local agent definition or route changed; stop it before persisting the new snapshot")
			}
			result = current
			return nil
		}
		event, err := randomEventID()
		if err != nil {
			return err
		}
		digest := strings.TrimPrefix(pair.SnapshotRevision, "sha256:")
		names := []string{"snapshot-" + digest + ".agent.md", "start-" + event + ".json"}
		if pair.Tests.State == "external" {
			names = append(names, "snapshot-"+digest+".agent.test.md")
		}
		if err := ensureLocalAgentCapacity(dir, names...); err != nil {
			return err
		}
		agent, tests, err := cachePair(ctx, dir, path, pair)
		if err != nil {
			return err
		}
		starts, _, err := readInstanceEvents(ctx, dir, path)
		if err != nil {
			return err
		}
		var sequence uint64 = 1
		for _, prior := range starts {
			if prior.Sequence >= sequence {
				sequence = prior.Sequence + 1
			}
		}
		if sequence == 0 {
			return errors.New("local agent registration sequence exhausted")
		}
		result = LocalAgentInstance{SchemaVersion: localAgentSchema, EventID: event, Sequence: sequence, ID: id, Name: pair.Agent.Identity.ID, Source: pair.AgentPath,
			AgentRevision: pair.Agent.Revision, SnapshotRevision: pair.SnapshotRevision, TestsState: pair.Tests.State, TestsRevision: pair.Tests.Revision,
			TestsPath: pair.Tests.Selection.Path, TestsExplicit: pair.Tests.Selection.Explicit, CachedAgent: agent, CachedTests: tests,
			Provider: provider, Model: model, Started: time.Now().UTC(), State: "active"}
		raw, _ := json.Marshal(result)
		if err := publishPrivateBytes(ctx, dir, "start-"+event+".json", raw); err != nil {
			return err
		}
		created = true
		return nil
	})
	return result, created, err
}

func readInstanceEvents(ctx context.Context, dir *fileaccess.Directory, dirPath string) ([]LocalAgentInstance, map[string]time.Time, error) {
	entries, err := dir.ReadDir(-1)
	if err != nil || len(entries) > 1024 {
		return nil, nil, errors.New("local agent instance state is unavailable")
	}
	starts, stops := []LocalAgentInstance{}, map[string]time.Time{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "snapshot-") || name == "transition.lock" {
			continue
		}
		if strings.HasPrefix(name, "stop-") && strings.HasSuffix(name, ".json") {
			raw, err := readPrivateBytes(ctx, dir, name, 4<<10)
			var stop localAgentStop
			fileEvent := strings.TrimSuffix(strings.TrimPrefix(name, "stop-"), ".json")
			if err != nil || strictLocalAgentJSON(raw, &stop) != nil || stop.SchemaVersion != localAgentSchema || stop.EventID != fileEvent || !validHexValue(stop.EventID, 24) || stop.Stopped.IsZero() {
				return nil, nil, errors.New("local agent stop record is invalid")
			}
			stops[stop.EventID] = stop.Stopped
			continue
		}
		if !strings.HasPrefix(name, "start-") || !strings.HasSuffix(name, ".json") {
			return nil, nil, errors.New("local agent instance state contains an unexpected entry")
		}
		raw, err := readPrivateBytes(ctx, dir, name, 32<<10)
		var record LocalAgentInstance
		fileEvent := strings.TrimSuffix(strings.TrimPrefix(name, "start-"), ".json")
		if err != nil || strictLocalAgentJSON(raw, &record) != nil || record.EventID != fileEvent || !validLocalAgentRecord(record, dirPath) {
			return nil, nil, errors.New("local agent record is invalid")
		}
		pair, err := LoadLocalAgentPair(ctx, record)
		if err != nil || pair.Agent.Identity.ID != record.Name {
			return nil, nil, errors.New("local agent record does not match its cached snapshot")
		}
		starts = append(starts, record)
	}
	seenSequence, seenEvent := map[uint64]bool{}, map[string]bool{}
	for _, start := range starts {
		if seenSequence[start.Sequence] || seenEvent[start.EventID] {
			return nil, nil, errors.New("local agent event identity is ambiguous")
		}
		seenSequence[start.Sequence], seenEvent[start.EventID] = true, true
	}
	active := 0
	for event := range stops {
		if !seenEvent[event] {
			return nil, nil, errors.New("local agent stop has no matching start")
		}
	}
	for event, stopped := range stops {
		for _, start := range starts {
			if start.EventID == event && stopped.Before(start.Started) {
				return nil, nil, errors.New("local agent stop predates its start")
			}
		}
	}
	for _, start := range starts {
		if _, stopped := stops[start.EventID]; !stopped {
			active++
		}
	}
	if active > 1 {
		return nil, nil, errors.New("local agent has multiple active registrations")
	}
	return starts, stops, nil
}

func validLocalAgentRecord(record LocalAgentInstance, dir string) bool {
	digest := strings.TrimPrefix(record.SnapshotRevision, "sha256:")
	validTests := record.TestsState == "inline" && record.CachedTests == "" && record.TestsPath != "" && !record.TestsExplicit && record.TestsRevision == record.AgentRevision || record.TestsState == "absent" && record.CachedTests == "" && record.TestsPath != "" && !record.TestsExplicit && record.TestsRevision == "" || record.TestsState == "none" && record.CachedTests == "" && record.TestsPath == "" && !record.TestsExplicit && record.TestsRevision == "" || record.TestsState == "external" && record.CachedTests != "" && record.TestsPath != "" && validDigest(record.TestsRevision)
	return record.SchemaVersion == localAgentSchema && validHexValue(record.EventID, 24) && record.Sequence > 0 && !record.Started.IsZero() && record.Stopped == nil && validLocalAgentID(record.ID) && record.Name != "" && filepath.Base(dir) == record.ID && LocalAgentID(record.Source) == record.ID && validDigest(record.AgentRevision) && validDigest(record.SnapshotRevision) && validTests &&
		record.CachedAgent == filepath.Join(dir, "snapshot-"+digest+".agent.md") && (record.CachedTests == "" || record.CachedTests == filepath.Join(dir, "snapshot-"+digest+".agent.test.md")) &&
		(record.TestsRevision == "" || validDigest(record.TestsRevision)) && (record.Provider == "fake" || record.Provider == "copilot") && agentfile.ValidModel(record.Model) && (record.Provider == "fake") == (record.Model == "fake") && record.State == "active"
}

func ReadLocalAgentInstances(all bool) ([]LocalAgentInstance, error) {
	ctx := context.Background()
	instances, err := localAgentDirectories(ctx, false)
	if errors.Is(err, os.ErrNotExist) {
		return []LocalAgentInstance{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer instances.Close()
	entries, err := instances.ReadDir(-1)
	if err != nil || len(entries) > 1024 {
		return nil, errors.New("local agent inventory is unavailable")
	}
	root, _ := config.StateDir()
	rows := []LocalAgentInstance{}
	for _, entry := range entries {
		if !validLocalAgentID(entry.Name()) || !entry.IsDir() {
			return nil, errors.New("local agent inventory contains an unexpected entry")
		}
		dir, err := fileaccess.OpenDirectoryAt(ctx, instances, entry.Name())
		if err != nil || fileaccess.CheckPrivate(ctx, dir, true) != nil {
			if dir != nil {
				_ = dir.Close()
			}
			return nil, errors.New("local agent instance directory is unsafe")
		}
		path := filepath.Join(root, "local-agents", "instances", entry.Name())
		starts, stops, err := readInstanceEvents(ctx, dir, path)
		_ = dir.Close()
		if err != nil {
			return nil, err
		}
		if len(starts) == 0 {
			continue
		}
		sort.Slice(starts, func(i, j int) bool { return starts[i].Sequence < starts[j].Sequence })
		row := starts[len(starts)-1]
		for _, candidate := range starts {
			if _, stopped := stops[candidate.EventID]; !stopped {
				row = candidate
			}
		}
		if stopped, ok := stops[row.EventID]; ok {
			row.State = "stopped"
			row.Stopped = &stopped
		}
		if all || row.State == "active" {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

func resolveLocalAgent(selector string, all bool) (LocalAgentInstance, error) {
	rows, err := ReadLocalAgentInstances(all)
	if err != nil {
		return LocalAgentInstance{}, err
	}
	var found *LocalAgentInstance
	for i := range rows {
		if rows[i].ID == selector || rows[i].Name == selector || len(selector) >= 8 && strings.HasPrefix(strings.TrimPrefix(rows[i].ID, "md-"), selector) {
			if found != nil {
				return LocalAgentInstance{}, errors.New("local agent selector is ambiguous")
			}
			copy := rows[i]
			found = &copy
		}
	}
	if found == nil {
		return LocalAgentInstance{}, os.ErrNotExist
	}
	return *found, nil
}

func ReadLocalAgentRecord(selector string) (LocalAgentInstance, error) {
	return resolveLocalAgent(selector, true)
}

func LoadLocalAgentPair(ctx context.Context, record LocalAgentInstance) (*agentfile.Pair, error) {
	dir, _, err := localAgentInstanceDirectory(ctx, record.ID, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	agentRaw, err := readPrivateBytes(ctx, dir, filepath.Base(record.CachedAgent), agentfile.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	agent, err := agentfile.Parse(record.Source, agentRaw)
	if err != nil || agent.Revision != record.AgentRevision {
		return nil, errors.New("local agent cached source is invalid")
	}
	var testsRaw []byte
	present := record.TestsState == "external"
	if present {
		testsRaw, err = readPrivateBytes(ctx, dir, filepath.Base(record.CachedTests), agentfile.MaxFileBytes)
		if err != nil {
			return nil, err
		}
	}
	selection := agentfile.TestsSelection{Path: record.TestsPath, Explicit: record.TestsExplicit}
	pair, err := agentfile.ComposePair(record.Source, agent, selection, testsRaw, present)
	if err != nil || pair.SnapshotRevision != record.SnapshotRevision || pair.Tests.State != record.TestsState || pair.Tests.Revision != record.TestsRevision {
		return nil, errors.New("local agent cached snapshot is invalid")
	}
	return pair, nil
}
func PingLocalAgent(_ context.Context, record LocalAgentInstance) error {
	current, err := resolveLocalAgent(record.ID, true)
	if err != nil || current.EventID != record.EventID || current.State != "active" {
		return errors.New("local agent is not active")
	}
	return nil
}

func WithLocalAgentRun(ctx context.Context, record LocalAgentInstance, run func(*agentfile.Pair) error) error {
	dir, _, err := localAgentInstanceDirectory(ctx, record.ID, false)
	if err != nil {
		return err
	}
	defer dir.Close()
	return withLocalAgentTransition(ctx, dir, func() error {
		current, err := resolveLocalAgent(record.ID, true)
		if err != nil || current.EventID != record.EventID || current.State != "active" {
			return errors.New("local agent is not active")
		}
		pair, err := LoadLocalAgentPair(ctx, current)
		if err != nil {
			return err
		}
		return run(pair)
	})
}

func StopLocalAgent(ctx context.Context, selector string) error {
	record, err := resolveLocalAgent(selector, true)
	if err != nil {
		return err
	}
	if record.State != "active" {
		return errors.New("local agent is already stopped")
	}
	dir, _, err := localAgentInstanceDirectory(ctx, record.ID, false)
	if err != nil {
		return err
	}
	defer dir.Close()
	return withLocalAgentTransition(ctx, dir, func() error {
		current, err := resolveLocalAgent(record.ID, true)
		if err != nil || current.EventID != record.EventID || current.State != "active" {
			return errors.New("local agent changed before stop")
		}
		name := "stop-" + record.EventID + ".json"
		if err := ensureLocalAgentCapacity(dir, name); err != nil {
			return err
		}
		raw, _ := json.Marshal(localAgentStop{SchemaVersion: localAgentSchema, EventID: record.EventID, Stopped: time.Now().UTC()})
		return publishPrivateBytes(ctx, dir, name, raw)
	})
}
