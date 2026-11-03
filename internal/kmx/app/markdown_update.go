package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type MarkdownUpdatePlan struct {
	Instance   AgentInstance
	Definition *MarkdownAgent
	Model      string
	TestStatus string
	Noop       bool
}

func (s *MarkdownSession) PlanAgentUpdate(ctx context.Context, a *MarkdownAgent) (MarkdownUpdatePlan, error) {
	var p MarkdownUpdatePlan
	if err := attachApprovedTools(a); err != nil {
		return p, err
	}
	if err := s.checkSelection(a); err != nil {
		return p, err
	}
	r, err := FindAgentInstance(a.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return p, errors.New("agent has no instance; use run without --update first")
		}
		return p, err
	}
	if r.State != "running" {
		return p, errors.New("agent is stopped; use run without --update to start the current file")
	}
	if r.Manager != localAgentManagerVersion {
		return p, errors.New("agent uses an older manager; stop and rerun once to enable update")
	}
	p = MarkdownUpdatePlan{Instance: r, Definition: a, Model: s.effective(a).Model, Noop: r.Version == a.Revision && r.Model == s.effective(a).Model && r.BindingDigest == a.BindingDigest, TestStatus: "untested"}
	if p.Noop {
		return p, nil
	}
	if _, err = CacheMarkdownVersion(a); err != nil {
		return p, err
	}
	rows := []AgentInstance{r}
	observeAgentActivity(ctx, rows)
	p.Instance = rows[0]
	history, err := LoadMarkdownHistory(a)
	if err != nil {
		p.TestStatus = "history unavailable"
	} else if len(history) > 0 {
		p.TestStatus = MarkdownStatus(s.effective(a), &history[len(history)-1]) + " (local preview)"
	}
	return p, nil
}
func ConfirmAgentUpdate(ctx context.Context, out io.Writer, in io.Reader, p MarkdownUpdatePlan, yes bool) error {
	if p.Noop {
		return nil
	}
	count := "all (count unavailable)"
	if p.Instance.SessionCount != nil {
		count = fmt.Sprint(*p.Instance.SessionCount)
	}
	started := "unknown"
	if !p.Instance.Started.IsZero() {
		started = p.Instance.Started.UTC().Format(time.RFC3339)
	}
	if _, err := fmt.Fprintf(out, "Update %s (%s)\nUptime: %s (started %s)\nVersion: %s → %s\nModel: %s → %s\nSessions: %s open conversations will close\nTests: %s\n", p.Instance.Name, ShortAgentID(p.Instance.ID), agentAge(p.Instance.Started, time.Now()), started, mdShort(p.Instance.Version), mdShort(p.Definition.Revision), mdModelLabel(p.Instance.Model), mdModelLabel(p.Model), count, p.TestStatus); err != nil {
		return err
	}
	if p.Instance.BindingDigest != p.Definition.BindingDigest {
		if _, err := fmt.Fprintf(out, "Tool binding: %s → %s\n", mdShort(p.Instance.BindingDigest), mdShort(p.Definition.BindingDigest)); err != nil {
			return err
		}
	}
	if p.Definition.Grant != nil {
		detail := "github.com/" + p.Definition.Grant.Repository + " · read-only"
		if p.Definition.Grant.Adapter == cliToolAdapter {
			detail = "local executable bindings"
		}
		if _, err := fmt.Fprintf(out, "Approved tools: %s · %s\n", strings.Join(p.Definition.Grant.Tools, ", "), detail); err != nil {
			return err
		}
	}
	if yes {
		return ctx.Err()
	}
	if in == nil {
		return errors.New("update requires confirmation; pass --yes to accept closing the agent's sessions")
	}
	if _, err := fmt.Fprint(out, "Apply update? [Y/n]: "); err != nil {
		return err
	}
	scanner := bufio.NewScanner(mdImportLineReader{in})
	if !scanner.Scan() {
		return errors.New("update cancelled; running agent unchanged")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	if answer != "" && answer != "y" && answer != "yes" {
		return errors.New("update declined; running agent unchanged")
	}
	return nil
}
func (s *MarkdownSession) ApplyAgentUpdate(ctx context.Context, p MarkdownUpdatePlan) (AgentInstance, error) {
	if p.Noop {
		return p.Instance, nil
	}
	if err := p.Definition.CheckCurrent(); err != nil {
		return AgentInstance{}, err
	}
	r := p.Instance
	reply, err := agentSocketCall(ctx, r.Socket, agentWorkerRequest{AgentID: r.ID, Version: r.Version, Generation: r.Generation, Operation: "update", NewVersion: p.Definition.Revision, Model: p.Model, Source: p.Definition.TestsPath, BindingDigest: p.Definition.BindingDigest})
	if err != nil {
		return AgentInstance{}, err
	}
	if reply.Instance == nil {
		return AgentInstance{}, errors.New("manager returned no updated instance")
	}
	return *reply.Instance, nil
}
