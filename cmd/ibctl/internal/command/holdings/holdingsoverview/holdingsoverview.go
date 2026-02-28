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
	"github.com/bufdev/ibctl/cmd/ibctl/internal/ibctlcmd"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlholdings"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlmerge"
	"github.com/bufdev/ibctl/internal/ibctl/ibctltaxlot"
	"github.com/bufdev/ibctl/internal/pkg/cliio"
	"github.com/bufdev/ibctl/internal/pkg/mathpb"
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
	flagSet.StringVar(&f.Config, ibctlcmd.ConfigFlagName, ibctlconfig.DefaultConfigFileName, "The configuration file path")
	flagSet.StringVar(&f.Format, formatFlagName, "table", "Output format (table, csv, json)")
}

func run(ctx context.Context, container appext.Container, flags *flags) error {
	format, err := cliio.ParseFormat(flags.Format)
	if err != nil {
		return appcmd.NewInvalidArgumentError(err.Error())
	}
	// Read and validate the configuration file.
	config, err := ibctlconfig.ReadConfig(flags.Config)
	if err != nil {
		return err
	}
	// Ensure Flex Query data has been downloaded (implicitly downloads if missing).
	downloader, err := ibctlcmd.NewDownloader(container, flags.Config)
	if err != nil {
		return err
	}
	if err := downloader.EnsureDownloaded(ctx); err != nil {
		return err
	}
	// Merge Activity Statement CSVs with Flex Query cached data across all accounts.
	mergedData, err := ibctlmerge.Merge(config.DataDirV1Path, config.ActivityStatementsDirPath, config.AccountAliases)
	if err != nil {
		return err
	}
	// Compute the combined holdings overview from merged data.
	result, err := ibctlholdings.GetHoldingsOverview(
		mergedData.Trades,
		mergedData.Positions,
		mergedData.Transfers,
		mergedData.TradeTransfers,
		config,
	)
	if err != nil {
		return err
	}
	// Log any data inconsistencies detected during computation.
	logger := container.Logger()
	for _, unmatched := range result.UnmatchedSells {
		logger.Warn("unmatched sell (buy likely before data window)",
			"account", unmatched.AccountAlias,
			"symbol", unmatched.Symbol,
			"unmatched_quantity", mathpb.ToString(unmatched.UnmatchedQuantity),
		)
	}
	for _, discrepancy := range result.PositionDiscrepancies {
		logPositionDiscrepancy(container, discrepancy)
	}
	// Write output in the requested format.
	writer := os.Stdout
	switch format {
	case cliio.FormatTable:
		headers := ibctlholdings.HoldingsOverviewHeaders()
		rows := make([][]string, 0, len(result.Holdings))
		for _, h := range result.Holdings {
			rows = append(rows, ibctlholdings.HoldingOverviewToRow(h))
		}
		return cliio.WriteTable(writer, headers, rows)
	case cliio.FormatCSV:
		headers := ibctlholdings.HoldingsOverviewHeaders()
		records := make([][]string, 0, len(result.Holdings)+1)
		records = append(records, headers)
		for _, h := range result.Holdings {
			records = append(records, ibctlholdings.HoldingOverviewToRow(h))
		}
		return cliio.WriteCSVRecords(writer, records)
	case cliio.FormatJSON:
		return cliio.WriteJSON(writer, result.Holdings...)
	default:
		return appcmd.NewInvalidArgumentErrorf("unsupported format: %s", format)
	}
}

// logPositionDiscrepancy logs a structured position discrepancy as a warning.
func logPositionDiscrepancy(container appext.Container, d ibctltaxlot.PositionDiscrepancy) {
	logger := container.Logger()
	switch d.Type {
	case ibctltaxlot.DiscrepancyTypeQuantity:
		logger.Warn("position quantity mismatch",
			"account", d.AccountAlias,
			"symbol", d.Symbol,
			"computed", d.ComputedValue,
			"reported", d.ReportedValue,
		)
	case ibctltaxlot.DiscrepancyTypeCostBasis:
		logger.Warn("position cost basis mismatch",
			"account", d.AccountAlias,
			"symbol", d.Symbol,
			"computed", d.ComputedValue,
			"reported", d.ReportedValue,
		)
	case ibctltaxlot.DiscrepancyTypeComputedOnly:
		logger.Warn("position computed but not reported by IBKR",
			"account", d.AccountAlias,
			"symbol", d.Symbol,
			"computed_quantity", d.ComputedValue,
		)
	case ibctltaxlot.DiscrepancyTypeReportedOnly:
		logger.Warn("position reported by IBKR but not in computed data",
			"account", d.AccountAlias,
			"symbol", d.Symbol,
			"reported_quantity", d.ReportedValue,
		)
	}
}
