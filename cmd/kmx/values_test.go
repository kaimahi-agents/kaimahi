package main

import "testing"

func TestBudgetValuesPreserveNilAndZero(t *testing.T) {
	cents, tokens, err := parseBudgetValues("-", "-")
	if err != nil || cents != nil || tokens != nil {
		t.Fatalf("clear values: %v %v %v", cents, tokens, err)
	}
	cents, tokens, err = parseBudgetValues("0", "1")
	if err != nil || cents == nil || *cents != 0 || tokens == nil || *tokens != 1 {
		t.Fatalf("bounded values: %v %v %v", cents, tokens, err)
	}
}

func TestBudgetValuesRejectInvalidCaps(t *testing.T) {
	for _, values := range [][2]string{{"-1", "-"}, {"-", "-1"}, {"1.5", "0"}, {"0", "9223372036854775808"}} {
		if _, _, err := parseBudgetValues(values[0], values[1]); err == nil {
			t.Fatalf("invalid caps accepted: %v", values)
		}
	}
}
