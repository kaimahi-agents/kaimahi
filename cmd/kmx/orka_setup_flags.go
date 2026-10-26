package main

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func addOrkaSetupFlags(cmd *cobra.Command, opt *app.OrkaSetupOptions) {
	cmd.Flags().StringVar(&opt.Chart, "orka-chart", "", "exact local Orka chart used for runtime setup")
	cmd.Flags().StringVar(&opt.ChartSHA256, "orka-chart-sha256", "", "SHA256 of the exact local Orka chart")
	opt.Enforcement = new(bool)
	cmd.Flags().BoolVar(opt.Enforcement, "enforcement", false, "explicitly enable or disable privileged provider enrollment in the verified bundled chart; omission preserves it")
	cmd.MarkFlagsRequiredTogether("orka-chart", "orka-chart-sha256")
}

func selectedOrkaSetup(cmd *cobra.Command, opt app.OrkaSetupOptions) app.OrkaSetupOptions {
	if !cmd.Flags().Changed("enforcement") {
		opt.Enforcement = nil
	}
	return opt
}

func validateOrkaSetupFlags(cmd *cobra.Command, opt app.OrkaSetupOptions) error {
	if cmd.Flags().Changed("orka-chart") || cmd.Flags().Changed("orka-chart-sha256") {
		_, err := hex.DecodeString(opt.ChartSHA256)
		if strings.TrimSpace(opt.Chart) == "" || len(opt.ChartSHA256) != 64 ||
			opt.ChartSHA256 != strings.ToLower(opt.ChartSHA256) || err != nil {
			return fmt.Errorf("--orka-chart and --orka-chart-sha256 require a local file and exact lowercase SHA256")
		}
	}
	if cmd.Flags().Changed("enforcement") && opt.Chart == "" {
		return fmt.Errorf("--enforcement requires --orka-chart and --orka-chart-sha256 with the bundled provider installer")
	}
	return nil
}
