// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package categorylist implements the "holding category list" command.
package categorylist

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

// formatFlagName is the flag name for the output format.
const formatFlagName = "format"

// downloadFlagName is the flag name for downloading fresh data before displaying.
const downloadFlagName = "download"

// NewCommand returns a new category list command.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	flags := newFlags()
	return &appcmd.Command{
		Use:   name,
		Short: "List holdings aggregated by category",
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
}

func newFlags() *flags {
	return &flags{}
}

// Bind registers the flag definitions with the given flag set.
func (f *flags) Bind(flagSet *pflag.FlagSet) {
	flagSet.StringVar(&f.Dir, ibctlcmd.DirFlagName, ".", "The ibctl directory containing ibctl.yaml")
	flagSet.StringVar(&f.Format, formatFlagName, "table", "Output format (table, csv, json)")
	flagSet.BoolVar(&f.Download, downloadFlagName, false, "Download fresh data before displaying")
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
	// Compute holdings via FIFO from all trade data.
	result, err := ibctlholdings.GetHoldingsOverview(mergedData.Trades, mergedData.Positions, mergedData.CashPositions, config, fxStore)
	if err != nil {
		return err
	}
	// Aggregate holdings by category.
	categories := ibctlholdings.GetCategoryList(result.Holdings)
	// Write output in the requested format.
	writer := os.Stdout
	switch format {
	case cliio.FormatTable:
		headers := ibctlholdings.CategoryListHeaders()
		rows := make([][]string, 0, len(categories))
		for _, c := range categories {
			rows = append(rows, ibctlholdings.CategoryOverviewToTableRow(c))
		}
		return cliio.WriteTable(writer, headers, rows)
	case cliio.FormatCSV:
		headers := ibctlholdings.CategoryListHeaders()
		records := make([][]string, 0, len(categories)+1)
		records = append(records, headers)
		for _, c := range categories {
			records = append(records, ibctlholdings.CategoryOverviewToRow(c))
		}
		return cliio.WriteCSVRecords(writer, records)
	case cliio.FormatJSON:
		return cliio.WriteJSON(writer, categories...)
	default:
		return appcmd.NewInvalidArgumentErrorf("unsupported format: %s", format)
	}
}
