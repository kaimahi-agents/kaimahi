package app

import (
	"bufio"
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

const localAgentSchema = 2

type LocalAgentInstance struct {
	SchemaVersion    int        `json:"schemaVersion"`
	EventID          string     `json:"eventId"`
	Sequence         uint64     `json:"sequence"`
	ID               string     `json:"id"`
	InstanceID       string     `json:"instanceId"`
	Name             string     `json:"name"`
	AuthoredName     string     `json:"authoredName"`
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
	Primary          bool       `json:"primary,omitempty"`
	Ready            bool       `json:"ready,omitempty"`
	Sessions         int        `json:"sessions,omitempty"`
	LastActivity     *time.Time `json:"lastActivity,omitempty"`
	LastError        string     `json:"lastError,omitempty"`
}

type LocalAgentSession struct {
	ID           string    `json:"id"`
	Label        string    `json:"label,omitempty"`
	Messages     int       `json:"messages"`
	Created      time.Time `json:"created"`
	LastActivity time.Time `json:"lastActivity"`
	Active       bool      `json:"active"`
	LastError    string    `json:"lastError,omitempty"`
}

type LocalAgentObservation struct {
	Ready        bool                `json:"ready"`
	Started      time.Time           `json:"started"`
	Sessions     []LocalAgentSession `json:"sessions,omitempty"`
	LastActivity *time.Time          `json:"lastActivity,omitempty"`
	LastError    string              `json:"lastError,omitempty"`
}

type localAgentStop struct {
	SchemaVersion int       `json:"schemaVersion"`
	EventID       string    `json:"eventId"`
	Stopped       time.Time `json:"stopped"`
}

type localAgentPromotion struct {
	SchemaVersion int       `json:"schemaVersion"`
	EventID       string    `json:"eventId"`
	InstanceID    string    `json:"instanceId"`
	Promoted      time.Time `json:"promoted"`
}

type localAgentWarning struct {
	SchemaVersion int       `json:"schemaVersion"`
	EventID       string    `json:"eventId"`
	Warning       string    `json:"warning"`
	RuntimeName   string    `json:"runtimeName,omitempty"`
	Observed      time.Time `json:"observed"`
}

type LocalAgentActivationMode string

const (
	LocalAgentActivationNormal  LocalAgentActivationMode = "normal"
	LocalAgentActivationUpgrade LocalAgentActivationMode = "upgrade"
	LocalAgentActivationNew     LocalAgentActivationMode = "new"
)

type LocalAgentActivationOptions struct {
	Mode LocalAgentActivationMode
	Name string
}

// LocalAgentDriftError reports an active generation whose immutable snapshot or
// inference route differs from the requested generation.
type LocalAgentDriftError struct {
	Active            LocalAgentInstance
	RequestedSnapshot string
}

func (e *LocalAgentDriftError) Error() string {
	return "local agent definition or route changed; choose upgrade or start a new instance"
}

func LocalAgentID(source string) string {
	sum := sha256.Sum256([]byte(source))
	return fmt.Sprintf("md-%x", sum[:16])
}

func validLocalAgentID(id string) bool {
	return validHexValue(strings.TrimPrefix(id, "md-"), 32) && strings.HasPrefix(id, "md-")
}
func validLocalAgentName(name string) bool {
	if name == "" || len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, r := range name {
		if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
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
	count := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "stop-") {
			count++
		}
	}
	for _, name := range names {
		if !present[name] && !strings.HasPrefix(name, "stop-") {
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

func ActivateLocalAgent(ctx context.Context, executable string, pair *agentfile.Pair, provider, endpoint string, model string) (LocalAgentInstance, bool, error) {
	return ActivateLocalAgentWithOptions(ctx, executable, pair, provider, endpoint, model, LocalAgentActivationOptions{Mode: LocalAgentActivationNormal})
}

func ActivateLocalAgentWithOptions(ctx context.Context, executable string, pair *agentfile.Pair, provider, _ string, model string, options LocalAgentActivationOptions) (LocalAgentInstance, bool, error) {
	if pair == nil || pair.Agent == nil {
		return LocalAgentInstance{}, false, errors.New("local agent requires a checked agent file")
	}
	if options.Mode == "" {
		options.Mode = LocalAgentActivationNormal
	}
	if options.Mode != LocalAgentActivationNormal && options.Mode != LocalAgentActivationUpgrade && options.Mode != LocalAgentActivationNew {
		return LocalAgentInstance{}, false, errors.New("invalid local agent activation mode")
	}
	if options.Mode != LocalAgentActivationNew && options.Name != "" {
		return LocalAgentInstance{}, false, errors.New("a runtime name is valid only for a new parallel instance")
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
		rows, err := ReadLocalAgentInstances(false)
		if err != nil {
			return err
		}
		active := make([]LocalAgentInstance, 0)
		stoppedForRestart := ""
		for _, current := range rows {
			if current.ID == id {
				active = append(active, current)
			}
		}
		if options.Mode == LocalAgentActivationNormal {
			for _, current := range active {
				if current.SnapshotRevision == pair.SnapshotRevision && current.Provider == provider && current.Model == model {
					if current.State == "running" {
						result = current
						return nil
					}
					if current.State == "crashed" {
						if err := writeLocalAgentStop(ctx, dir, current.EventID, current.Started); err != nil {
							return err
						}
						stoppedForRestart = current.EventID
						active = nil
						break
					}
				}
			}
			if len(active) > 0 {
				return &LocalAgentDriftError{Active: active[0], RequestedSnapshot: pair.SnapshotRevision}
			}
		}
		event, err := randomEventID()
		if err != nil {
			return err
		}
		digest := strings.TrimPrefix(pair.SnapshotRevision, "sha256:")
		names := []string{"snapshot-" + digest + ".agent.md", "start-" + event + ".json", "active-" + event + ".lock"}
		if options.Mode != LocalAgentActivationNew {
			names = append(names, "primary-"+event+".json")
		}
		if options.Mode == LocalAgentActivationUpgrade {
			names = append(names, "warning-"+event+".json")
		}
		if pair.Tests.State == "external" {
			names = append(names, "snapshot-"+digest+".agent.test.md")
		}
		historyDir, _, err := localAgentInstanceDirectory(ctx, id, false)
		if err != nil {
			return err
		}
		starts, _, _, err := readInstanceEvents(ctx, historyDir, path)
		closeErr := historyDir.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := ensureLocalAgentCapacity(dir, names...); err != nil {
			return err
		}
		agent, tests, err := cachePair(ctx, dir, path, pair)
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
		instanceID := "li-" + event
		name := pair.Agent.Identity.ID
		if options.Mode == LocalAgentActivationNew {
			name = options.Name
			if name == "" {
				name = pair.Agent.Identity.ID + "-" + event[:8]
			}
		}
		if len(name) > 63 {
			name = strings.TrimRight(name[:54], "-") + "-" + event[:8]
		}
		if !validLocalAgentName(name) {
			return errors.New("local agent runtime name must use lowercase letters, digits and internal hyphens")
		}
		for _, row := range rows {
			replaced := false
			if options.Mode == LocalAgentActivationUpgrade {
				for _, old := range active {
					if old.EventID == row.EventID {
						replaced = true
						break
					}
				}
			}
			if row.Name == name && row.EventID != stoppedForRestart && !replaced {
				return errors.New("local agent runtime name is already active")
			}
		}
		result = LocalAgentInstance{SchemaVersion: localAgentSchema, EventID: event, InstanceID: instanceID, Sequence: sequence, ID: id, Name: name, AuthoredName: pair.Agent.Identity.ID, Source: pair.AgentPath,
			AgentRevision: pair.Agent.Revision, SnapshotRevision: pair.SnapshotRevision, TestsState: pair.Tests.State, TestsRevision: pair.Tests.Revision,
			TestsPath: pair.Tests.Selection.Path, TestsExplicit: pair.Tests.Selection.Explicit, CachedAgent: agent, CachedTests: tests,
			Provider: provider, Model: model, Started: time.Now().UTC(), State: "active"}
		raw, _ := json.Marshal(result)
		if err := publishPrivateBytes(ctx, dir, "start-"+event+".json", raw); err != nil {
			return err
		}
		if err := startLocalAgentWorker(ctx, executable, result); err != nil {
			_ = writeLocalAgentStop(context.Background(), dir, event, result.Started)
			return err
		}
		if options.Mode != LocalAgentActivationNew {
			if err := writeLocalAgentPromotion(ctx, dir, result); err != nil {
				_, _ = probeLocalAgent(context.Background(), result, "shutdown", localAgentIPCRequest{})
				_ = writeLocalAgentStop(context.Background(), dir, event, result.Started)
				return err
			}
			result.Primary = true
		}
		result.State, result.Ready = "running", true
		created = true
		if options.Mode == LocalAgentActivationUpgrade {
			cleanupErrors := []error{}
			for _, old := range active {
				if old.State == "running" {
					if _, err := probeLocalAgent(ctx, old, "shutdown", localAgentIPCRequest{}); err != nil {
						cleanupErrors = append(cleanupErrors, errors.New("old worker could not be stopped"))
						continue
					}
					if err := waitLocalAgentStopped(ctx, old); err != nil {
						cleanupErrors = append(cleanupErrors, err)
						continue
					}
				} else if held, err := localAgentWorkerLeaseHeld(ctx, old); err != nil || held {
					if err != nil {
						cleanupErrors = append(cleanupErrors, err)
					} else {
						cleanupErrors = append(cleanupErrors, errors.New("old worker is unavailable"))
					}
					continue
				}
				if err := writeLocalAgentStop(ctx, dir, old.EventID, old.Started); err != nil {
					cleanupErrors = append(cleanupErrors, err)
				}
			}
			if len(cleanupErrors) > 0 {
				result.LastError = "upgrade active; old generation cleanup incomplete"
				if result.Name == pair.Agent.Identity.ID {
					base := pair.Agent.Identity.ID
					if len(base) > 54 {
						base = strings.TrimRight(base[:54], "-")
					}
					result.Name = base + "-" + event[:8]
				}
				raw, _ := json.Marshal(localAgentWarning{SchemaVersion: localAgentSchema, EventID: result.EventID, Warning: result.LastError, RuntimeName: result.Name, Observed: time.Now().UTC()})
				_ = publishPrivateBytes(context.Background(), dir, "warning-"+result.EventID+".json", raw)
			}
		}
		return nil
	})
	return result, created, err
}

func readInstanceEvents(ctx context.Context, dir *fileaccess.Directory, dirPath string) ([]LocalAgentInstance, map[string]time.Time, map[string]time.Time, error) {
	entries, err := dir.ReadDir(-1)
	if err != nil || len(entries) > 2048 {
		return nil, nil, nil, errors.New("local agent instance state is unavailable")
	}
	starts, stops, promotions := []LocalAgentInstance{}, map[string]time.Time{}, map[string]time.Time{}
	warnings := map[string]localAgentWarning{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "snapshot-") || name == "transition.lock" || validLocalAgentLockName(name) || validLocalAgentSocketName(name) {
			continue
		}
		if strings.HasPrefix(name, "primary-") && strings.HasSuffix(name, ".json") {
			raw, err := readPrivateBytes(ctx, dir, name, 4<<10)
			var promotion localAgentPromotion
			fileEvent := strings.TrimSuffix(strings.TrimPrefix(name, "primary-"), ".json")
			if err != nil || strictLocalAgentJSON(raw, &promotion) != nil || promotion.SchemaVersion != localAgentSchema || promotion.EventID != fileEvent || !validHexValue(fileEvent, 24) || promotion.InstanceID != "li-"+fileEvent || promotion.Promoted.IsZero() {
				return nil, nil, nil, errors.New("local agent promotion record is invalid")
			}
			promotions[fileEvent] = promotion.Promoted
			continue
		}
		if strings.HasPrefix(name, "stop-") && strings.HasSuffix(name, ".json") {
			raw, err := readPrivateBytes(ctx, dir, name, 4<<10)
			var stop localAgentStop
			fileEvent := strings.TrimSuffix(strings.TrimPrefix(name, "stop-"), ".json")
			if err != nil || strictLocalAgentJSON(raw, &stop) != nil || stop.SchemaVersion != localAgentSchema || stop.EventID != fileEvent || !validHexValue(stop.EventID, 24) || stop.Stopped.IsZero() {
				return nil, nil, nil, errors.New("local agent stop record is invalid")
			}
			stops[stop.EventID] = stop.Stopped
			continue
		}
		if strings.HasPrefix(name, "warning-") && strings.HasSuffix(name, ".json") {
			raw, err := readPrivateBytes(ctx, dir, name, 4<<10)
			var warning localAgentWarning
			fileEvent := strings.TrimSuffix(strings.TrimPrefix(name, "warning-"), ".json")
			if err != nil || strictLocalAgentJSON(raw, &warning) != nil || warning.SchemaVersion != localAgentSchema || warning.EventID != fileEvent || warning.Warning == "" || warning.Observed.IsZero() {
				return nil, nil, nil, errors.New("local agent warning record is invalid")
			}
			if warning.RuntimeName != "" && !validLocalAgentName(warning.RuntimeName) {
				return nil, nil, nil, errors.New("local agent warning runtime name is invalid")
			}
			warnings[fileEvent] = warning
			continue
		}
		if !strings.HasPrefix(name, "start-") || !strings.HasSuffix(name, ".json") {
			return nil, nil, nil, errors.New("local agent instance state contains an unexpected entry")
		}
		raw, err := readPrivateBytes(ctx, dir, name, 32<<10)
		var version struct {
			SchemaVersion int `json:"schemaVersion"`
		}
		if err == nil && json.Unmarshal(raw, &version) == nil && version.SchemaVersion == 1 {
			return nil, nil, nil, errors.New("draft local-agent state is obsolete; stop old draft workers, remove KMX_HOME/local-agents, and run again")
		}
		var record LocalAgentInstance
		fileEvent := strings.TrimSuffix(strings.TrimPrefix(name, "start-"), ".json")
		if err != nil || strictLocalAgentJSON(raw, &record) != nil || record.EventID != fileEvent || !validLocalAgentRecord(record, dirPath) {
			return nil, nil, nil, errors.New("local agent record is invalid")
		}
		pair, err := LoadLocalAgentPair(ctx, record)
		if err != nil || pair.Agent.Identity.ID != record.AuthoredName {
			return nil, nil, nil, errors.New("local agent record does not match its cached snapshot")
		}
		starts = append(starts, record)
	}
	for i := range starts {
		if warning, ok := warnings[starts[i].EventID]; ok {
			starts[i].LastError = warning.Warning
			if warning.RuntimeName != "" {
				starts[i].Name = warning.RuntimeName
			}
		}
	}
	seenSequence, seenEvent := map[uint64]bool{}, map[string]bool{}
	for _, start := range starts {
		if seenSequence[start.Sequence] || seenEvent[start.EventID] {
			return nil, nil, nil, errors.New("local agent event identity is ambiguous")
		}
		seenSequence[start.Sequence], seenEvent[start.EventID] = true, true
	}
	active := 0
	for event := range stops {
		if !seenEvent[event] {
			return nil, nil, nil, errors.New("local agent stop has no matching start")
		}
	}
	for event, stopped := range stops {
		for _, start := range starts {
			if start.EventID == event && stopped.Before(start.Started) {
				return nil, nil, nil, errors.New("local agent stop predates its start")
			}
		}
	}
	for _, start := range starts {
		if _, stopped := stops[start.EventID]; !stopped {
			active++
		}
	}
	for event := range promotions {
		if !seenEvent[event] {
			return nil, nil, nil, errors.New("local agent promotion has no matching start")
		}
	}
	_ = active
	return starts, stops, promotions, nil
}

func validLocalAgentRecord(record LocalAgentInstance, dir string) bool {
	digest := strings.TrimPrefix(record.SnapshotRevision, "sha256:")
	validTests := record.TestsState == "inline" && record.CachedTests == "" && record.TestsPath != "" && !record.TestsExplicit && record.TestsRevision == record.AgentRevision || record.TestsState == "absent" && record.CachedTests == "" && record.TestsPath != "" && !record.TestsExplicit && record.TestsRevision == "" || record.TestsState == "none" && record.CachedTests == "" && record.TestsPath == "" && !record.TestsExplicit && record.TestsRevision == "" || record.TestsState == "external" && record.CachedTests != "" && record.TestsPath != "" && validDigest(record.TestsRevision)
	return record.SchemaVersion == localAgentSchema && validHexValue(record.EventID, 24) && record.InstanceID == "li-"+record.EventID && record.Sequence > 0 && !record.Started.IsZero() && record.Stopped == nil && validLocalAgentID(record.ID) && validLocalAgentName(record.Name) && validLocalAgentName(record.AuthoredName) && filepath.Base(dir) == record.ID && LocalAgentID(record.Source) == record.ID && validDigest(record.AgentRevision) && validDigest(record.SnapshotRevision) && validTests &&
		record.CachedAgent == filepath.Join(dir, "snapshot-"+digest+".agent.md") && (record.CachedTests == "" || record.CachedTests == filepath.Join(dir, "snapshot-"+digest+".agent.test.md")) &&
		(record.TestsRevision == "" || validDigest(record.TestsRevision)) && (record.Provider == "fake" || record.Provider == "copilot") && agentfile.ValidModel(record.Model) && (record.Provider == "fake") == (record.Model == "fake") && record.State == "active"
}

func ReadLocalAgentInstances(all bool) ([]LocalAgentInstance, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
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
		starts, stops, promotions, err := readInstanceEvents(ctx, dir, path)
		_ = dir.Close()
		if err != nil {
			return nil, err
		}
		if len(starts) == 0 {
			continue
		}
		sort.Slice(starts, func(i, j int) bool { return starts[i].Sequence < starts[j].Sequence })
		for _, start := range starts {
			row := start
			if stopped, ok := stops[row.EventID]; ok {
				row.State = "stopped"
				row.Stopped = &stopped
			}
			var newest time.Time
			for event, promoted := range promotions {
				if promoted.After(newest) || promoted.Equal(newest) && event > row.EventID {
					newest = promoted
				}
			}
			_, row.Primary = promotions[row.EventID]
			if row.Primary && !promotions[row.EventID].Equal(newest) {
				row.Primary = false
			}
			if row.State == "active" {
				probeCtx, probeCancel := context.WithTimeout(ctx, time.Second)
				observation, probeErr := probeLocalAgent(probeCtx, row, "observe", localAgentIPCRequest{})
				probeCancel()
				if probeErr == nil {
					row.State, row.Ready = "running", observation.Observation.Ready
					row.Sessions = len(observation.Observation.Sessions)
					row.LastActivity = observation.Observation.LastActivity
					if observation.Observation.LastError != "" {
						row.LastError = observation.Observation.LastError
					}
				} else if held, lockErr := localAgentWorkerLeaseHeld(ctx, row); lockErr != nil {
					return nil, lockErr
				} else if held {
					row.State, row.LastError = "unavailable", "worker IPC unavailable"
				} else {
					row.State = "crashed"
				}
			}
			if all || row.State == "running" || row.State == "unavailable" || row.State == "crashed" {
				rows = append(rows, row)
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name == rows[j].Name {
			return rows[i].Sequence < rows[j].Sequence
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

func resolveLocalAgent(selector string, all bool) (LocalAgentInstance, error) {
	rows, err := ReadLocalAgentInstances(all)
	if err != nil {
		return LocalAgentInstance{}, err
	}
	matches := make([]LocalAgentInstance, 0, 1)
	for i := range rows {
		match := rows[i].InstanceID == selector || rows[i].Name == selector || len(selector) >= 8 && strings.HasPrefix(strings.TrimPrefix(rows[i].InstanceID, "li-"), selector)
		if rows[i].ID == selector || len(selector) >= 8 && strings.HasPrefix(strings.TrimPrefix(rows[i].ID, "md-"), selector) {
			match = true
		}
		if match {
			matches = append(matches, rows[i])
		}
	}
	if len(matches) == 0 {
		return LocalAgentInstance{}, os.ErrNotExist
	}
	active := matches[:0]
	for _, match := range matches {
		if match.State != "stopped" {
			active = append(active, match)
		}
	}
	if len(active) > 0 {
		matches = active
	}
	if len(matches) != 1 {
		return LocalAgentInstance{}, errors.New("local agent selector is ambiguous; use a runtime name or instance ID")
	}
	return matches[0], nil
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
	current, err := resolveLocalAgent(record.InstanceID, true)
	if err != nil || current.EventID != record.EventID || current.State != "running" {
		return errors.New("local agent is not active")
	}
	_, err = probeLocalAgent(context.Background(), current, "ping", localAgentIPCRequest{})
	return err
}

func WithLocalAgentRun(ctx context.Context, record LocalAgentInstance, run func(*agentfile.Pair) error) error {
	dir, _, err := localAgentInstanceDirectory(ctx, record.ID, false)
	if err != nil {
		return err
	}
	defer dir.Close()
	return withLocalAgentTransition(ctx, dir, func() error {
		current, err := resolveLocalAgent(record.InstanceID, true)
		if err != nil || current.EventID != record.EventID || current.State != "running" {
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
	if record.State == "stopped" {
		return errors.New("local agent is already stopped")
	}
	dir, _, err := localAgentInstanceDirectory(ctx, record.ID, false)
	if err != nil {
		return err
	}
	defer dir.Close()
	return withLocalAgentTransition(ctx, dir, func() error {
		current, err := resolveLocalAgent(record.InstanceID, true)
		if err != nil || current.EventID != record.EventID || current.State == "stopped" {
			return errors.New("local agent changed before stop")
		}
		if current.State == "running" {
			if _, err := probeLocalAgent(ctx, current, "shutdown", localAgentIPCRequest{}); err != nil {
				return errors.New("local agent shutdown failed")
			}
			if err := waitLocalAgentStopped(ctx, current); err != nil {
				return err
			}
		} else {
			held, err := localAgentWorkerLeaseHeld(ctx, current)
			if err != nil {
				return err
			}
			if held {
				return errors.New("local agent process is alive but unavailable")
			}
			if err := removeLocalAgentSocket(current); err != nil {
				return err
			}
		}
		return writeLocalAgentStop(ctx, dir, record.EventID, current.Started)
	})
}

func writeLocalAgentStop(ctx context.Context, dir *fileaccess.Directory, event string, started time.Time) error {
	name := "stop-" + event + ".json"
	if err := ensureLocalAgentCapacity(dir, name); err != nil {
		return err
	}
	stopped := time.Now().UTC()
	if stopped.Before(started) {
		stopped = started
	}
	raw, _ := json.Marshal(localAgentStop{SchemaVersion: localAgentSchema, EventID: event, Stopped: stopped})
	return publishPrivateBytes(ctx, dir, name, raw)
}

func writeLocalAgentPromotion(ctx context.Context, dir *fileaccess.Directory, record LocalAgentInstance) error {
	name := "primary-" + record.EventID + ".json"
	if err := ensureLocalAgentCapacity(dir, name); err != nil {
		return err
	}
	raw, _ := json.Marshal(localAgentPromotion{SchemaVersion: localAgentSchema, EventID: record.EventID, InstanceID: record.InstanceID, Promoted: time.Now().UTC()})
	return publishPrivateBytes(ctx, dir, name, raw)
}

func ObserveLocalAgent(ctx context.Context, selector string) (LocalAgentObservation, error) {
	record, err := resolveLocalAgent(selector, false)
	if err != nil || record.State != "running" {
		return LocalAgentObservation{}, errors.New("local agent is not running")
	}
	response, err := probeLocalAgent(ctx, record, "observe", localAgentIPCRequest{})
	return response.Observation, err
}

func OpenLocalAgentSession(ctx context.Context, selector, label string) (LocalAgentSession, error) {
	return SelectLocalAgentSession(ctx, selector, label, true)
}

func SelectLocalAgentSession(ctx context.Context, selector, label string, fresh bool) (LocalAgentSession, error) {
	record, err := resolveLocalAgent(selector, false)
	if err != nil || record.State != "running" {
		return LocalAgentSession{}, errors.New("local agent is not running")
	}
	response, err := probeLocalAgent(ctx, record, "session-open", localAgentIPCRequest{Label: label, Fresh: fresh})
	return response.Session, err
}

func ReadLocalAgentSessions(ctx context.Context, selector string) ([]LocalAgentSession, error) {
	observation, err := ObserveLocalAgent(ctx, selector)
	return observation.Sessions, err
}

func CloseLocalAgentSession(ctx context.Context, selector, sessionID string) error {
	record, err := resolveLocalAgent(selector, false)
	if err != nil || record.State != "running" {
		return errors.New("local agent is not running")
	}
	_, err = probeLocalAgent(ctx, record, "session-close", localAgentIPCRequest{SessionID: sessionID})
	return err
}

func InvokeLocalAgent(ctx context.Context, selector, sessionID, input string) (string, error) {
	record, err := resolveLocalAgent(selector, false)
	if err != nil || record.State != "running" {
		return "", errors.New("local agent is not running")
	}
	response, err := invokeLocalAgentIPC(ctx, record, sessionID, input)
	return response.Answer, err
}

func ChatLocalAgent(ctx context.Context, selector, sessionID string, in io.Reader, out io.Writer) error {
	if in == nil || out == nil {
		return errors.New("local chat requires input and output")
	}
	scanner := bufio.NewScanner(io.LimitReader(in, 1<<20))
	for scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "/quit" {
			return nil
		}
		if input == "" {
			continue
		}
		answer, err := InvokeLocalAgent(ctx, selector, sessionID, input)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out, answer); err != nil {
			return err
		}
	}
	return scanner.Err()
}
