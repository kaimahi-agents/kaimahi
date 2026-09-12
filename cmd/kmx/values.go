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

func parseApprovalValues(ttl, uses, amount string) (*int64, *int64, *int64, error) {
	t, err := admin.ParseTTL(ttl)
	if err != nil {
		return nil, nil, nil, err
	}
	u, err := admin.ParseCap("uses", uses)
	if err != nil {
		return nil, nil, nil, err
	}
	m, err := admin.ParseCap("amount", amount)
	if err != nil {
		return nil, nil, nil, err
	}
	return t, u, m, nil
}
