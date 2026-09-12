package main

import (
	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
)

func parseBudgetValues(cents, tokens string) (*int64, *int64, error) {
	c, err := admin.ParseCap("cents cap", cents)
	if err != nil {
		return nil, nil, err
	}
	t, err := admin.ParseCap("token cap", tokens)
	if err != nil {
		return nil, nil, err
	}
	return c, t, nil
}
