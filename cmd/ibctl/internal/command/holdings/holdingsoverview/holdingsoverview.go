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
	"github.com/bufdev/ibctl/internal/pkg/cliio"
	"github.com/spf13/pflag"
)

// formatFlagName is the flag name for the output format.
const formatFlagName = "format"

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
	// Format is the output format (table, csv, json).
	Format string
}

func newFlags() *flags {
	return &flags{}
}

// Bind registers the flag definitions with the given flag set.
func (f *flags) Bind(flagSet *pflag.FlagSet) {
	flagSet.StringVar(
		&f.Format,
		formatFlagName,
		"table",
		"Output format (table, csv, json)",
	)
}

func run(_ context.Context, container appext.Container, flags *flags) error {
	format, err := cliio.ParseFormat(flags.Format)
	if err != nil {
		return appcmd.NewInvalidArgumentError(err.Error())
	}
	// Read and validate the configuration file.
	config, err := ibctlconfig.ReadConfig(container.ConfigDirPath())
	if err != nil {
		return err
	}
	// Compute the versioned data directory path.
	dataDirV1Path := ibctlconfig.DataDirV1Path(container.DataDirPath())
	// Get the holdings overview data.
	holdingsOverview, err := ibctlholdings.GetHoldingsOverview(dataDirV1Path, config)
	if err != nil {
		return err
	}
	// Write output in the requested format.
	writer := os.Stdout
	switch format {
	case cliio.FormatTable:
		headers := ibctlholdings.HoldingsOverviewHeaders()
		rows := make([][]string, 0, len(holdingsOverview))
		for _, h := range holdingsOverview {
			rows = append(rows, ibctlholdings.HoldingOverviewToRow(h))
		}
		return cliio.WriteTable(writer, headers, rows)
	case cliio.FormatCSV:
		headers := ibctlholdings.HoldingsOverviewHeaders()
		records := make([][]string, 0, len(holdingsOverview)+1)
		records = append(records, headers)
		for _, h := range holdingsOverview {
			records = append(records, ibctlholdings.HoldingOverviewToRow(h))
		}
		return cliio.WriteCSVRecords(writer, records)
	case cliio.FormatJSON:
		return cliio.WriteJSON(writer, holdingsOverview...)
	default:
		return appcmd.NewInvalidArgumentErrorf("unsupported format: %s", format)
	}
}
