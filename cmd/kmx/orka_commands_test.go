package main

import (
	"strings"
	"testing"
)

func TestOrkaLocalChartFlagsRequireEachOther(t *testing.T) {
	for _, tc := range []struct {
		name, chart, digest string
		valid               bool
	}{
		{name: "default", valid: true},
		{name: "chart only", chart: "candidate.tgz"},
		{name: "digest only", digest: strings.Repeat("a", 64)},
		{name: "explicit pair", chart: "candidate.tgz", digest: strings.Repeat("a", 64), valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newOrkaInstallCommand(&commandState{deps: productionDependencies()})
			if tc.chart != "" {
				if err := cmd.Flags().Set("chart", tc.chart); err != nil {
					t.Fatal(err)
				}
			}
			if tc.digest != "" {
				if err := cmd.Flags().Set("chart-sha256", tc.digest); err != nil {
					t.Fatal(err)
				}
			}
			if err := cmd.ValidateFlagGroups(); (err == nil) != tc.valid {
				t.Fatalf("flag pair valid=%v, got %v", tc.valid, err)
			}
		})
	}
}
