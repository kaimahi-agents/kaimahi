package mcp

import (
	"context"
	"os"
	"strings"
	"unicode/utf8"
)

const squadBriefPath = ".squad/kmx-brief.md"

type SquadBrief struct {
	Text     string `json:"text"`
	Revision string `json:"revision"`
}

func validateSquadBrief(_ string, raw []byte) error {
	if len(raw) > 16384 || !utf8.Valid(raw) || strings.TrimSpace(string(raw)) == "" || strings.ContainsRune(string(raw), 0) {
		return refusal("invalid_squad_brief")
	}
	return nil
}

func (c *SquadCreator) brief(ctx context.Context) (SquadBrief, error) {
	if err := cancelled(ctx); err != nil {
		return SquadBrief{}, err
	}
	raw, err := c.readLocal(squadBriefPath)
	if os.IsNotExist(err) {
		return SquadBrief{Revision: "none"}, nil
	}
	if err != nil {
		return SquadBrief{}, refusal("invalid_squad_brief")
	}
	if err := validateSquadBrief(squadBriefPath, raw); err != nil {
		return SquadBrief{}, err
	}
	return SquadBrief{Text: string(raw), Revision: revision(raw)}, nil
}

func (c *SquadCreator) saveBrief(ctx context.Context, expected, text string) (File, error) {
	if err := c.checkRoot(); err != nil {
		return File{}, err
	}
	f := &Files{root: c.root, privatePath: squadBriefPath, validate: validateSquadBrief}
	if expected == "none" {
		return f.Create(ctx, squadBriefPath, []byte(text))
	}
	return f.Edit(ctx, squadBriefPath, expected, []byte(text))
}

func (c *SquadCreator) EditBrief(ctx context.Context, expected, text string) (File, error) {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return File{}, cancelled(ctx)
	}
	return c.saveBrief(ctx, expected, text)
}
