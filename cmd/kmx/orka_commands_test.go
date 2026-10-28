package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestOrkaInstallRejectsInvalidChartSelectorsBeforeConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"--chart=", "--chart-sha256="},
		{"--chart=candidate.tgz", "--chart-sha256="},
		{"--chart= ", "--chart-sha256=" + strings.Repeat("a", 64)},
		{"--chart=candidate.tgz", "--chart-sha256=" + strings.Repeat("A", 64)},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			if err := execute(append([]string{"cluster", "orka", "install"}, args...), deps); err == nil || *loads != 0 || !strings.Contains(err.Error(), "--chart") {
				t.Fatalf("invalid chart selection reached configuration: err=%v loads=%d", err, *loads)
			}
		})
	}
}

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
