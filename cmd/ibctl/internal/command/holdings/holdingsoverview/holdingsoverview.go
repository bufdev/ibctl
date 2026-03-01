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
	"github.com/bufdev/ibctl/internal/ibctl/ibctlfxrates"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlholdings"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlmerge"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlpath"
	"github.com/bufdev/ibctl/internal/ibctl/ibctltaxlot"
	"github.com/bufdev/ibctl/internal/pkg/cliio"
	"github.com/bufdev/ibctl/internal/pkg/mathpb"
	"github.com/spf13/pflag"
)

// formatFlagName is the flag name for the output format.
const formatFlagName = "format"

// cachedFlagName is the flag name for skipping download and using cached data only.
const cachedFlagName = "cached"

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
	// Dir is the base directory containing ibctl.yaml and data subdirectories.
	Dir string
	// Format is the output format (table, csv, json).
	Format string
	// Cached skips downloading and uses only cached data.
	Cached bool
}

func newFlags() *flags {
	return &flags{}
}

// Bind registers the flag definitions with the given flag set.
func (f *flags) Bind(flagSet *pflag.FlagSet) {
	flagSet.StringVar(&f.Dir, ibctlcmd.DirFlagName, ".", "The ibctl directory containing ibctl.yaml")
	flagSet.StringVar(&f.Format, formatFlagName, "table", "Output format (table, csv, json)")
	flagSet.BoolVar(&f.Cached, cachedFlagName, false, "Skip downloading and use only cached data")
}

func run(ctx context.Context, container appext.Container, flags *flags) error {
	format, err := cliio.ParseFormat(flags.Format)
	if err != nil {
		return appcmd.NewInvalidArgumentError(err.Error())
	}
	// Read and validate the configuration file from the base directory.
	config, err := ibctlconfig.ReadConfig(flags.Dir)
	if err != nil {
		return err
	}
	// Download fresh data unless --cached is set.
	if !flags.Cached {
		downloader, err := ibctlcmd.NewDownloader(container, flags.Dir)
		if err != nil {
			return err
		}
		if err := downloader.Download(ctx); err != nil {
			return err
		}
	}
	// Merge seed lots + Activity Statement CSVs + Flex Query cached data across all accounts.
	// Trades come from data/ (persistent), snapshots from cache/ (blow-away safe).
	mergedData, err := ibctlmerge.Merge(
		ibctlpath.DataAccountsDirPath(config.DirPath),
		ibctlpath.CacheAccountsDirPath(config.DirPath),
		ibctlpath.ActivityStatementsDirPath(config.DirPath),
		ibctlpath.SeedDirPath(config.DirPath),
		config.AccountAliases,
	)
	if err != nil {
		return err
	}
	// Load FX rates for USD price conversion. Returns an empty store if no data available.
	fxStore := ibctlfxrates.NewStore(ibctlpath.CacheFXDirPath(config.DirPath))
	// Compute holdings via FIFO from all trade data, verified against IBKR positions.
	result, err := ibctlholdings.GetHoldingsOverview(mergedData.Trades, mergedData.Positions, mergedData.CashPositions, config, fxStore)
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
	for _, d := range result.PositionDiscrepancies {
		logPositionDiscrepancy(container, d)
	}
	// Write output in the requested format.
	writer := os.Stdout
	switch format {
	case cliio.FormatTable:
		headers := ibctlholdings.HoldingsOverviewHeaders()
		// Split holdings into securities and cash for separate display sections.
		var securityRows, cashRows [][]string
		for _, h := range result.Holdings {
			row := ibctlholdings.HoldingOverviewToTableRow(h)
			if h.Category == "CASH" {
				cashRows = append(cashRows, row)
			} else {
				securityRows = append(securityRows, row)
			}
		}
		// Build the row sections: securities, then cash, then totals.
		var sections [][]string
		sections = append(sections, securityRows...)
		if len(cashRows) > 0 {
			// Blank separator row between securities and cash.
			sections = append(sections, make([]string, len(headers)))
			sections = append(sections, cashRows...)
		}
		// Build the totals row aligned to the same columns as the data.
		totals := ibctlholdings.ComputeTotals(result.Holdings)
		totalsRow := make([]string, len(headers))
		totalsRow[0] = "TOTAL"
		totalsRow[6] = totals.MarketValueUSD
		totalsRow[7] = totals.UnrealizedPnLUSD
		totalsRow[8] = totals.STCGUSD
		totalsRow[9] = totals.LTCGUSD
		return cliio.WriteTableWithTotals(writer, headers, sections, totalsRow)
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
