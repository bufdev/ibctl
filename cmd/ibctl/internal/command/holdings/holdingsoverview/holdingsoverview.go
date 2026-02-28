// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package holdingsoverview implements the "holdings overview" command.
package holdingsoverview

import (
	"context"
	"os"

	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlholdings"
	"github.com/bufdev/ibctl/internal/pkg/cli"
	"github.com/spf13/pflag"
)

const (
	// configFlagName is the flag name for the configuration file path.
	configFlagName = "config"
	// formatFlagName is the flag name for the output format.
	formatFlagName = "format"
)

// NewCommand returns a new holdings overview command.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	flags := newFlags()
	return &appcmd.Command{
		Use:   name,
		Short: "Display holdings with prices, positions, and classifications",
		Args:  appcmd.NoArgs,
		Run: builder.NewRunFunc(
			func(ctx context.Context, container appext.Container) error {
				return run(ctx, container, flags)
			},
		),
		BindFlags: flags.Bind,
	}
}

type flags struct {
	// Config is the path to the configuration file.
	Config string
	// Format is the output format (table, csv, json).
	Format string
}

func newFlags() *flags {
	return &flags{}
}

// Bind registers the flag definitions with the given flag set.
func (f *flags) Bind(flagSet *pflag.FlagSet) {
	flagSet.StringVar(
		&f.Config,
		configFlagName,
		"ibctl.yaml",
		"The configuration file path",
	)
	flagSet.StringVar(
		&f.Format,
		formatFlagName,
		"table",
		"Output format (table, csv, json)",
	)
}

func run(_ context.Context, _ appext.Container, flags *flags) error {
	if flags.Config == "" {
		return appcmd.NewInvalidArgumentErrorf("--%s is required", configFlagName)
	}
	format, err := cli.ParseFormat(flags.Format)
	if err != nil {
		return appcmd.NewInvalidArgumentError(err.Error())
	}
	config, err := ibctlconfig.ReadConfig(flags.Config)
	if err != nil {
		return err
	}
	// Get the holdings overview data.
	holdingsOverview, err := ibctlholdings.GetHoldingsOverview(config)
	if err != nil {
		return err
	}
	// Write output in the requested format.
	writer := os.Stdout
	switch format {
	case cli.FormatTable:
		headers := ibctlholdings.HoldingsOverviewHeaders()
		rows := make([][]string, 0, len(holdingsOverview))
		for _, h := range holdingsOverview {
			rows = append(rows, ibctlholdings.HoldingOverviewToRow(h))
		}
		return cli.WriteTable(writer, headers, rows)
	case cli.FormatCSV:
		headers := ibctlholdings.HoldingsOverviewHeaders()
		records := make([][]string, 0, len(holdingsOverview)+1)
		records = append(records, headers)
		for _, h := range holdingsOverview {
			records = append(records, ibctlholdings.HoldingOverviewToRow(h))
		}
		return cli.WriteCSVRecords(writer, records)
	case cli.FormatJSON:
		return cli.WriteJSON(writer, holdingsOverview...)
	default:
		return appcmd.NewInvalidArgumentErrorf("unsupported format: %s", format)
	}
}
