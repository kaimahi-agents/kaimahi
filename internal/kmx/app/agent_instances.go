package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// AgentInstance describes an execution, never a merely discovered source file.
// The process-held lease is authoritative for liveness; PID is informational.
type AgentInstance struct {
	BindingDigest   string             `json:"bindingDigest,omitempty"`
	Tools           []string           `json:"tools,omitempty"`
	SchemaVersion   int                `json:"schemaVersion"`
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	Type            string             `json:"type"`
	Environment     string             `json:"environment"`
	Source          string             `json:"source"`
	Version         string             `json:"version"`
	PreviousVersion string             `json:"previousVersion,omitempty"`
	Generation      int                `json:"generation"`
	State           string             `json:"state"`
	Activity        string             `json:"activity,omitempty"`
	Session         string             `json:"session,omitempty"`
	Sessions        []AgentSessionInfo `json:"sessions,omitempty"`
	SessionCount    *int               `json:"sessionCount,omitempty"`
	Reason          string             `json:"reason,omitempty"`
	PID             int                `json:"pid"`
	Provider        string             `json:"provider"`
	Model           string             `json:"model"`
	Started         time.Time          `json:"started"`
	Finished        *time.Time         `json:"finished,omitempty"`
	Socket          string             `json:"socket,omitempty"`
	Manager         string             `json:"manager,omitempty"`
}
type agentLease struct {
	file   *os.File
	path   string
	record AgentInstance
}

var agentVersionDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func agentInstanceID(source string) string {
	return "md-" + strings.TrimPrefix(mdHash([]byte(source)), "sha256:")
}

// ShortAgentID is a display handle, never a version digest or a storage key.
func ShortAgentID(id string) string {
	raw := strings.TrimPrefix(id, "md-")
	return raw[:min(12, len(raw))]
}
func agentDisplayID(id string, rows []AgentInstance) string {
	raw := strings.TrimPrefix(id, "md-")
	for n := min(12, len(raw)); n <= len(raw); n++ {
		unique := true
		for _, r := range rows {
			if r.ID != id && strings.HasPrefix(strings.TrimPrefix(r.ID, "md-"), raw[:n]) {
				unique = false
				break
			}
		}
		if unique {
			return raw[:n]
		}
	}
	return id
}
func resolveAgentInstance(rows []AgentInstance, selector string) (AgentInstance, error) {
	if selector == "" {
		return AgentInstance{}, os.ErrNotExist
	}
	// Exact IDs always win, including the full hex form without the type prefix.
	for _, r := range rows {
		if selector == r.ID || selector == strings.TrimPrefix(r.ID, "md-") {
			return r, nil
		}
	}
	// Literal paths cannot be mistaken for names or ID prefixes.
	if filepath.IsAbs(selector) || strings.ContainsAny(selector, "/\\") || strings.HasSuffix(selector, ".md") {
		absolute, _ := filepath.Abs(selector)
		if canonical, e := filepath.EvalSymlinks(absolute); e == nil {
			absolute = canonical
		}
		for _, r := range rows {
			if absolute == r.Source {
				return r, nil
			}
		}
		return AgentInstance{}, os.ErrNotExist
	}
	hex := strings.TrimPrefix(selector, "md-")
	isHex := len(hex) > 0
	for _, c := range hex {
		if !strings.ContainsRune("0123456789abcdef", c) {
			isHex = false
			break
		}
	}
	selectMatch := func(matches []AgentInstance) (AgentInstance, error) {
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) == 0 {
			return AgentInstance{}, os.ErrNotExist
		}
		sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
		var b strings.Builder
		fmt.Fprintf(&b, "agent selector %q is ambiguous; use a longer ID or source path:\n", safeTerminal(selector))
		for _, r := range matches {
			fmt.Fprintf(&b, "  %s  %s  %s  %s\n", agentDisplayID(r.ID, rows), safeTerminal(r.Name), r.State, safeTerminal(r.Source))
		}
		return AgentInstance{}, errors.New(strings.TrimSpace(b.String()))
	}
	if isHex && len(hex) >= 6 {
		matches := []AgentInstance{}
		for _, r := range rows {
			if strings.HasPrefix(strings.TrimPrefix(r.ID, "md-"), hex) {
				matches = append(matches, r)
			}
		}
		if len(matches) > 0 {
			return selectMatch(matches)
		}
	}
	matches := []AgentInstance{}
	for _, r := range rows {
		if r.Name == selector {
			matches = append(matches, r)
		}
	}
	if len(matches) > 0 {
		return selectMatch(matches)
	}
	if isHex && len(hex) < 6 {
		for _, r := range rows {
			if strings.HasPrefix(strings.TrimPrefix(r.ID, "md-"), hex) {
				return AgentInstance{}, errors.New("agent ID prefixes require at least 6 hexadecimal characters")
			}
		}
	}
	return AgentInstance{}, os.ErrNotExist
}
func agentStateRoot() (string, error) {
	root, err := config.StateDir()
	return filepath.Join(root, "agents"), err
}

// AgentCompletionCandidates reads local records/leases only. Duplicate names
// are omitted; displayed IDs stay unique across both running and stopped rows.
func AgentCompletionCandidates(prefix string, runningOnly bool) ([]string, error) {
	rows, err := ReadAgentInstances("")
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.Name]++
	}
	values := []string{}
	for _, r := range rows {
		if runningOnly && r.State != "running" {
			continue
		}
		id := agentDisplayID(r.ID, rows)
		if strings.HasPrefix(prefix, "md-") {
			id = r.ID
		} else if len(prefix) > len(id) {
			id = strings.TrimPrefix(r.ID, "md-")
		}
		detail := safeTerminal(r.Name) + " · " + r.Type + " · " + r.State
		if strings.HasPrefix(id, prefix) {
			values = append(values, id+"\t"+detail)
		}
		if counts[r.Name] == 1 && strings.HasPrefix(r.Name, prefix) {
			resolved, e := resolveAgentInstance(rows, r.Name)
			if e == nil && resolved.ID == r.ID {
				values = append(values, r.Name+"\t"+agentDisplayID(r.ID, rows)+" · "+r.Type+" · "+r.State)
			}
		}
	}
	sort.Strings(values)
	return values, nil
}

// CacheMarkdownVersion stores exact source bytes once. Existing entries are
// verified rather than rewritten, so all invocations agree on a version's bytes.
func CacheMarkdownVersion(a *MarkdownAgent) (*MarkdownAgent, error) {
	if !agentVersionDigest.MatchString(a.Revision) || mdHash(a.Source) != a.Revision {
		return nil, errors.New("agent snapshot digest mismatch")
	}
	root, err := agentStateRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "versions", strings.TrimPrefix(a.Revision, "sha256:"))
	if err = mdPrivateDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "agent.md")
	if old, e := mdRead(path, markdownAgentLimit); e == nil {
		if !bytes.Equal(old, a.Source) {
			return nil, errors.New("cached agent version is corrupt")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	} else {
		temp, e := os.CreateTemp(dir, ".version-*")
		if e != nil {
			return nil, e
		}
		defer os.Remove(temp.Name())
		if _, e = temp.Write(a.Source); e != nil {
			temp.Close()
			return nil, e
		}
		if e = temp.Sync(); e != nil {
			temp.Close()
			return nil, e
		}
		if e = temp.Close(); e != nil {
			return nil, e
		}
		if e = os.Link(temp.Name(), path); e != nil && !errors.Is(e, os.ErrExist) {
			return nil, e
		}
		old, e := mdRead(path, markdownAgentLimit)
		if e != nil || !bytes.Equal(old, a.Source) {
			return nil, errors.New("cached version verification failed")
		}
	}
	// Parse the actual cache bytes for execution, not a mutable source pathname.
	raw, err := mdRead(path, markdownAgentLimit)
	if err != nil {
		return nil, err
	}
	frozen, err := parseMarkdownAgent(a.Path, raw)
	if err != nil {
		return nil, err
	}
	frozen.TestsPath, frozen.TestsRevision, frozen.PairRevision = a.TestsPath, a.TestsRevision, a.PairRevision
	frozen.Grant, frozen.BindingDigest = a.Grant, a.BindingDigest
	frozen.CLITools = append([]CLIToolSpec(nil), a.CLITools...)
	frozen.ToolsRevision = a.ToolsRevision
	return frozen, nil
}

func beginAgentInstance(a *MarkdownAgent, model string) (*agentLease, *MarkdownAgent, error) {
	source, err := filepath.Abs(a.Path)
	if err != nil {
		return nil, nil, err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return nil, nil, err
	}
	root, err := agentStateRoot()
	if err != nil {
		return nil, nil, err
	}
	id := agentInstanceID(source)
	dir := filepath.Join(root, "instances", id)
	if err = mdPrivateDir(dir); err != nil {
		return nil, nil, err
	}
	f, err := openAgentLease(filepath.Join(dir, "active.lock"), true)
	if err != nil {
		return nil, nil, err
	}
	if err = mdLock(f); err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("agent %s is already running; stop its foreground session before running an updated version", a.Name)
	}
	failed := true
	defer func() {
		if failed {
			f.Close()
		}
	}()
	recordPath := filepath.Join(dir, "instance.json")
	var old AgentInstance
	e := mdStateRead(recordPath, &old)
	if errors.Is(e, os.ErrNotExist) {
		e = mdStateRead(filepath.Join(dir, "removed.json"), &old)
	}
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, nil, e
	}
	if old.ID != "" && (old.ID != id || old.SchemaVersion != 1 || old.Source != source) {
		return nil, nil, errors.New("invalid agent instance record")
	}
	if err = a.CheckCurrent(); err != nil {
		return nil, nil, err
	}
	frozen, err := CacheMarkdownVersion(a)
	if err != nil {
		return nil, nil, err
	}
	frozen.Model = model
	r := AgentInstance{SchemaVersion: 1, ID: id, Name: a.Name, Type: "md", Source: source, Version: a.Revision, PreviousVersion: old.Version, Generation: old.Generation + 1, State: "running", PID: os.Getpid(), Provider: "copilot", Model: model, Started: time.Now().UTC()}
	r.BindingDigest = a.BindingDigest
	if a.Grant != nil {
		r.Tools = append([]string{}, a.Grant.Tools...)
	}
	if old.Version == r.Version {
		r.PreviousVersion = old.PreviousVersion
	}
	lease := &agentLease{file: f, path: recordPath, record: r}
	if err = lease.save(); err != nil {
		return nil, nil, err
	}
	failed = false
	return lease, frozen, nil
}

// RemoveAgentInstance removes an instance from inventory, never its definition,
// cached versions or test history. Keep lock inodes and a private tombstone so a
// concurrent/recreated instance cannot acquire stale generation authority.
func RemoveAgentInstance(ctx context.Context, selector string, force bool) (AgentInstance, error) {
	r, err := FindAgentInstance(selector)
	if err != nil {
		return r, err
	}
	if err = ctx.Err(); err != nil {
		return r, err
	}
	if r.State == "running" {
		if !force {
			return r, fmt.Errorf("agent %s (%s) is running; stop it first or use rm --force %s", r.Name, ShortAgentID(r.ID), ShortAgentID(r.ID))
		}
		if _, err = agentWorkerCall(ctx, r, "stop", ""); err != nil {
			return r, err
		}
	}
	root, err := agentStateRoot()
	if err != nil {
		return r, err
	}
	dir := filepath.Join(root, "instances", r.ID)
	lease, err := openAgentLease(filepath.Join(dir, "active.lock"), false)
	if err != nil {
		return r, err
	}
	defer lease.Close()
	deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		err = mdLock(lease)
		if err == nil {
			break
		}
		if !force || !mdLockBusy(err) {
			return r, errors.New("agent instance busy; nothing removed")
		}
		select {
		case <-deadline.Done():
			return r, errors.New("stop requested but agent is still busy; nothing removed")
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer mdUnlock(lease)
	path := filepath.Join(dir, "instance.json")
	var current AgentInstance
	if err = mdStateRead(path, &current); err != nil {
		return r, err
	}
	if current.ID != r.ID || current.Source != r.Source || current.Generation != r.Generation || current.Version != r.Version || !current.Started.Equal(r.Started) {
		return r, errors.New("agent changed during removal; nothing removed")
	}
	if err = ctx.Err(); err != nil {
		return r, err
	}
	// Atomic rename hides the record from inventory. The retained record is only
	// consulted to allocate a later generation, not to list or resume the agent.
	if err = os.Rename(path, filepath.Join(dir, "removed.json")); err != nil {
		return r, err
	}
	d, err := os.Open(dir)
	if err != nil {
		return r, err
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return r, fmt.Errorf("agent removed but persistence uncertain: %w", err)
	}
	r.State = "removed"
	return r, nil
}
func (l *agentLease) save() error {
	return mdStateUpdate(l.path, func() ([]byte, error) { return json.Marshal(l.record) })
}
func (l *agentLease) finish(cause error) error {
	if l == nil || l.file == nil {
		return nil
	}
	now := time.Now().UTC()
	l.record.State = "stopped"
	l.record.Finished = &now
	l.record.Reason = "session ended"
	if cause != nil {
		l.record.Reason = "execution failed or cancelled"
	}
	err := l.save()
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(err, closeErr)
}

func ReadAgentInstances(directory string) ([]AgentInstance, error) {
	root, err := agentStateRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "instances")
	f, err := os.Open(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []AgentInstance{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(1025)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 1024 {
		return nil, errors.New("agent inventory exceeds 1024 instances")
	}
	filter := ""
	if directory != "" {
		filter, err = filepath.Abs(directory)
		if err != nil {
			return nil, err
		}
		if canonical, e := filepath.EvalSymlinks(filter); e == nil {
			filter = canonical
		}
	}
	rows := []AgentInstance{}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "md-") {
			return nil, errors.New("invalid agent instance directory")
		}
		path := filepath.Join(dir, entry.Name())
		var r AgentInstance
		if err = mdStateRead(filepath.Join(path, "instance.json"), &r); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		if r.SchemaVersion != 1 || r.ID != entry.Name() || r.ID != agentInstanceID(r.Source) || !agentVersionDigest.MatchString(r.Version) || r.Type != "md" || (r.State != "running" && r.State != "stopped") {
			return nil, errors.New("invalid agent instance record")
		}
		if filter != "" {
			rel, e := filepath.Rel(filter, r.Source)
			if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
		}
		lock, e := openAgentLease(filepath.Join(path, "active.lock"), false)
		if e != nil {
			return nil, e
		}
		e = mdLock(lock)
		if e == nil {
			if r.State == "running" {
				r.State = "stopped"
				r.Reason = "owner exited without recording completion"
				r.Finished = nil
			}
			_ = mdUnlock(lock)
		} else if !mdLockBusy(e) {
			lock.Close()
			return nil, e
		}
		lock.Close()
		r.Environment = "local"
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Started.After(rows[j].Started) })
	return rows, nil
}
func AgentInstanceStatus(out io.Writer, directory, output string) error {
	return ListAgentInstances(out, directory, output, true)
}

func ListAgentInstances(out io.Writer, directory, output string, all bool) error {
	if output != "table" && output != "json" {
		return errors.New("agent inventory output must be table or json")
	}
	rows, err := ReadAgentInstances(directory)
	if err != nil {
		return err
	}
	allRows := rows
	if !all {
		rows = []AgentInstance{}
		for _, r := range allRows {
			if r.State == "running" {
				rows = append(rows, r)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	observeAgentActivity(ctx, rows)
	if output == "json" {
		return json.NewEncoder(out).Encode(struct {
			Environment string          `json:"environment"`
			Agents      []AgentInstance `json:"agents"`
		}{"local", rows})
	}
	if output != "table" {
		return errors.New("status output must be table or json")
	}
	if len(rows) == 0 {
		message := "No running agents. Use kmx list --all for stopped instances, or kmx run FILE.md to start one."
		if all {
			message = "No agent instances. Start one with: kmx run FILE.md"
		}
		_, err = fmt.Fprintln(out, message)
		return err
	}
	cells := [][]string{}
	for _, r := range rows {
		model := r.Model
		if model == "" {
			model = "default"
		}
		activity, session := r.Activity, "unknown"
		if activity == "" {
			activity = "—"
		}
		if r.SessionCount != nil {
			session = fmt.Sprint(*r.SessionCount)
		}
		cells = append(cells, []string{agentDisplayID(r.ID, allRows), r.Name, r.Type, r.Environment, r.State, activity, session, mdShort(r.Version), r.Provider + "/" + model, agentAge(r.Started, time.Now())})
	}
	_, err = fmt.Fprintln(out, cliui.New(out).TableWithRoles([]string{"ID", "NAME", "TYPE", "ENVIRONMENT", "STATE", "ACTIVITY", "SESSIONS", "VERSION", "INFERENCE", "AGE"}, cells, []cliui.ColumnRole{cliui.ColumnText, cliui.ColumnText, cliui.ColumnText, cliui.ColumnText, cliui.ColumnState, cliui.ColumnState, cliui.ColumnNumber}))
	return err
}

// Age is elapsed wall time since the latest start, not uptime or a version ID.
func agentAge(start, now time.Time) string {
	if start.IsZero() {
		return "unknown"
	}
	elapsed := now.Sub(start)
	if elapsed < 0 {
		return "clock skew"
	}
	if elapsed < time.Minute {
		return fmt.Sprintf("%ds", int(elapsed.Seconds()))
	}
	if elapsed < time.Hour {
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	}
	if elapsed < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(elapsed.Hours()), int(elapsed.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(elapsed.Hours())/24, int(elapsed.Hours())%24)
}

// InspectAgentInstance is source-independent: edits/deletions cannot change the
// identity and cached instructions of the selected running/stopped instance.
func InspectAgentInstance(ctx context.Context, out io.Writer, selector, output string) error {
	if output != "table" && output != "json" {
		return errors.New("inspect output must be table or json")
	}
	r, err := FindAgentInstance(selector)
	if err != nil {
		return err
	}
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows := []AgentInstance{r}
	observeAgentActivity(probe, rows)
	r = rows[0]
	a, cacheErr := loadAgentVersion(r)
	if output == "json" {
		report := struct {
			AgentInstance
			Instructions string `json:"instructions,omitempty"`
			CacheError   string `json:"cacheError,omitempty"`
		}{AgentInstance: r}
		if cacheErr != nil {
			report.CacheError = "cached definition unavailable or corrupt"
		} else {
			report.Instructions = a.Instructions
		}
		return json.NewEncoder(out).Encode(report)
	}
	model := r.Model
	if model == "" {
		model = "default"
	}
	activity, session := r.Activity, "unknown"
	if activity == "" {
		activity = "—"
	}
	if r.SessionCount != nil {
		session = fmt.Sprint(*r.SessionCount)
	}
	fields := []cliui.Field{{Label: "ID", Value: r.ID}, {Label: "Name", Value: safeTerminal(r.Name)}, {Label: "Type", Value: r.Type}, {Label: "Environment", Value: r.Environment}, {Label: "State", Value: r.State}, {Label: "Activity", Value: activity}, {Label: "Open sessions", Value: session}, {Label: "Version", Value: r.Version}, {Label: "Inference", Value: r.Provider + "/" + model}, {Label: "Generation", Value: fmt.Sprint(r.Generation)}, {Label: "Source", Value: safeTerminal(r.Source)}, {Label: "Started", Value: r.Started.Format(time.RFC3339)}}
	if r.Finished != nil {
		fields = append(fields, cliui.Field{Label: "Stopped", Value: r.Finished.Format(time.RFC3339)})
	}
	fields = append(fields, cliui.Field{Label: "Age (since start)", Value: agentAge(r.Started, time.Now())})
	if r.BindingDigest != "" {
		fields = append(fields, cliui.Field{Label: "Tool binding", Value: r.BindingDigest}, cliui.Field{Label: "Approved tools", Value: strings.Join(r.Tools, ", ")})
	}
	if r.Reason != "" {
		fields = append(fields, cliui.Field{Label: "Reason", Value: safeTerminal(r.Reason)})
	}
	ui := cliui.New(out)
	text := ui.Fields(fields)
	if cacheErr != nil {
		text += "\n\nCached definition unavailable or corrupt."
	} else {
		text += "\n\n" + ui.Heading("Cached instructions") + "\n" + safeTerminal(a.Instructions)
	}
	if len(r.Sessions) > 0 {
		cells := [][]string{}
		for _, s := range r.Sessions {
			activity := s.Activity
			if activity == "" {
				activity = "—"
			}
			cells = append(cells, []string{s.ID, s.Label, s.State, activity, mdShort(s.Version)})
		}
		text += "\n\n" + ui.Heading("Sessions") + "\n" + ui.Table([]string{"SESSION", "LABEL", "STATE", "ACTIVITY", "VERSION"}, cells)
	}
	_, err = fmt.Fprintln(out, text)
	return err
}

// Activity is a bounded observation, never inferred from a held lease or a PID.
// Older managers and unreachable providers retain lifecycle but report unknown.
func observeAgentActivity(ctx context.Context, rows []AgentInstance) {
	for i := range rows {
		r := &rows[i]
		r.Activity, r.Session = "", ""
		r.SessionCount = nil
		if r.State != "running" {
			zero := 0
			r.SessionCount = &zero
			for j := range r.Sessions {
				r.Sessions[j].State = "closed"
				r.Sessions[j].Activity = ""
			}
			continue
		}
		r.Sessions = nil
		r.Activity = "unknown"
		if r.Manager != localAgentManagerVersion || r.Socket == "" || ctx.Err() != nil {
			continue
		}
		reply, err := agentWorkerCall(ctx, *r, "observe", "")
		if err != nil || !reply.Ready || (reply.Activity != "idle" && reply.Activity != "responding" && reply.Activity != "error") {
			continue
		}
		r.Activity, r.Session = reply.Activity, reply.Session
		if reply.SessionsKnown {
			r.Sessions = reply.Sessions
			count := len(reply.Sessions)
			r.SessionCount = &count
		}
	}
}

func agentInstanceInventory(directory string) ([]agentTUIAgent, error) {
	instances, err := ReadAgentInstances(directory)
	if err != nil {
		return nil, err
	}
	rows := []agentTUIAgent{}
	for _, r := range instances {
		row := agentTUIAgent{Name: r.Name, Runtime: r.Type, Ready: r.State, Version: mdShort(r.Version), DefinitionPath: r.Source, TestsPath: MarkdownTestsPath(r.Source), Provider: r.Provider, Model: mdModelLabel(r.Model), PromptSource: "cached version " + r.Version, TestStatus: r.State + " · " + mdShort(r.Version)}
		root, e := agentStateRoot()
		row.InstanceID = r.ID
		for _, tool := range r.Tools {
			row.Tools = append(row.Tools, agentTUITool{Name: tool, Kind: "approved local read", Detail: "Frozen binding " + r.BindingDigest})
		}
		if e != nil {
			return nil, e
		}
		b, e := mdRead(filepath.Join(root, "versions", strings.TrimPrefix(r.Version, "sha256:"), "agent.md"), markdownAgentLimit)
		if e == nil && mdHash(b) == r.Version {
			a, e := parseMarkdownAgent(r.Source, b)
			if e == nil {
				row.SystemPrompt = a.Instructions
			}
		} else {
			row.PromptError = "cached definition unavailable"
		}
		rows = append(rows, row)
	}
	return rows, nil
}
