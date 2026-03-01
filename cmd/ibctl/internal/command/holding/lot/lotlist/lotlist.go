// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package lotlist implements the "holding lot list" command.
package lotlist

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
	"github.com/bufdev/ibctl/internal/pkg/cliio"
	"github.com/spf13/pflag"
)

const (
	// formatFlagName is the flag name for the output format.
	formatFlagName = "format"
	// downloadFlagName is the flag name for downloading fresh data before displaying.
	downloadFlagName = "download"
	// symbolFlagName is the flag name for filtering by symbol.
	symbolFlagName = "symbol"
)

// NewCommand returns a new lot list command.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	flags := newFlags()
	return &appcmd.Command{
		Use:   name,
		Short: "List individual tax lots, optionally filtered by symbol",
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
	// Download fetches fresh data before displaying.
	Download bool
	// Symbol filters lots to a specific symbol. Empty means all symbols.
	Symbol string
}

func newFlags() *flags {
	return &flags{}
}

// Bind registers the flag definitions with the given flag set.
func (f *flags) Bind(flagSet *pflag.FlagSet) {
	flagSet.StringVar(&f.Dir, ibctlcmd.DirFlagName, ".", "The ibctl directory containing ibctl.yaml")
	flagSet.StringVar(&f.Format, formatFlagName, "table", "Output format (table, csv, json)")
	flagSet.BoolVar(&f.Download, downloadFlagName, false, "Download fresh data before displaying")
	flagSet.StringVar(&f.Symbol, symbolFlagName, "", "Filter by symbol (omit for all symbols)")
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
	// Download fresh data if --download is set.
	if flags.Download {
		downloader, err := ibctlcmd.NewDownloader(container, flags.Dir)
		if err != nil {
			return err
		}
		if err := downloader.Download(ctx); err != nil {
			return err
		}
	}
	// Merge trade data from all sources.
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
	// Load FX rates for USD conversion.
	fxStore := ibctlfxrates.NewStore(ibctlpath.CacheFXDirPath(config.DirPath))
	// Get the lot list, optionally filtered by symbol.
	result, err := ibctlholdings.GetLotList(flags.Symbol, mergedData.Trades, mergedData.Positions, config, fxStore)
	if err != nil {
		return err
	}
	// Write output in the requested format.
	writer := os.Stdout
	switch format {
	case cliio.FormatTable:
		headers := ibctlholdings.LotListHeaders()
		rows := make([][]string, 0, len(result.Lots))
		for _, l := range result.Lots {
			rows = append(rows, ibctlholdings.LotOverviewToTableRow(l))
		}
		// Build totals row.
		totals := ibctlholdings.ComputeLotTotals(result.Lots)
		totalsRow := make([]string, len(headers))
		totalsRow[0] = "TOTAL"
		totalsRow[9] = totals.PnLUSD
		totalsRow[10] = totals.STCGUSD
		totalsRow[11] = totals.LTCGUSD
		totalsRow[12] = totals.ValueUSD
		return cliio.WriteTableWithTotals(writer, headers, rows, totalsRow)
	case cliio.FormatCSV:
		headers := ibctlholdings.LotListHeaders()
		records := make([][]string, 0, len(result.Lots)+1)
		records = append(records, headers)
		for _, l := range result.Lots {
			records = append(records, ibctlholdings.LotOverviewToRow(l))
		}
		return cliio.WriteCSVRecords(writer, records)
	case cliio.FormatJSON:
		return cliio.WriteJSON(writer, result.Lots...)
	default:
		return appcmd.NewInvalidArgumentErrorf("unsupported format: %s", format)
	}
}
