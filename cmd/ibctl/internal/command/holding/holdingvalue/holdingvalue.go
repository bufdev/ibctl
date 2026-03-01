// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package holdingvalue implements the "holding value" command.
package holdingvalue

import (
	"context"
	"fmt"
	"math"
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
	"github.com/bufdev/ibctl/internal/pkg/mathpb"
	"github.com/spf13/pflag"
)

// downloadFlagName is the flag name for downloading fresh data before displaying.
const downloadFlagName = "download"

// NewCommand returns a new holding value command.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	flags := newFlags()
	return &appcmd.Command{
		Use:   name,
		Short: "Display portfolio value with estimated tax impact",
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
	// Download fetches fresh data before displaying.
	Download bool
}

func newFlags() *flags {
	return &flags{}
}

// Bind registers the flag definitions with the given flag set.
func (f *flags) Bind(flagSet *pflag.FlagSet) {
	flagSet.StringVar(&f.Dir, ibctlcmd.DirFlagName, ".", "The ibctl directory containing ibctl.yaml")
	flagSet.BoolVar(&f.Download, downloadFlagName, false, "Download fresh data before displaying")
}

func run(ctx context.Context, container appext.Container, flags *flags) error {
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
	// Sum up portfolio value, STCG, and LTCG from all holdings.
	var totalValueMicros, totalSTCGMicros, totalLTCGMicros int64
	for _, h := range result.Holdings {
		totalValueMicros += mathpb.ParseMicros(h.MarketValueUSD)
		totalSTCGMicros += mathpb.ParseMicros(h.STCGUSD)
		totalLTCGMicros += mathpb.ParseMicros(h.LTCGUSD)
	}
	// Compute tax amounts. Gains are taxed; losses reduce taxes (can be negative).
	stcgTaxMicros := int64(math.Round(float64(totalSTCGMicros) * config.TaxRateSTCG))
	ltcgTaxMicros := int64(math.Round(float64(totalLTCGMicros) * config.TaxRateLTCG))
	totalTaxMicros := stcgTaxMicros + ltcgTaxMicros
	// After-tax value = portfolio value - total taxes.
	afterTaxMicros := totalValueMicros - totalTaxMicros
	// Print the summary.
	writer := os.Stdout
	fmt.Fprintf(writer, "Portfolio Value:  %s\n", cliio.FormatUSDMicros(totalValueMicros))
	fmt.Fprintf(writer, "\n")
	fmt.Fprintf(writer, "STCG:            %s\n", cliio.FormatUSDMicros(totalSTCGMicros))
	fmt.Fprintf(writer, "STCG Tax (%.1f%%):  %s\n", config.TaxRateSTCG*100, cliio.FormatUSDMicros(stcgTaxMicros))
	fmt.Fprintf(writer, "LTCG:            %s\n", cliio.FormatUSDMicros(totalLTCGMicros))
	fmt.Fprintf(writer, "LTCG Tax (%.1f%%):  %s\n", config.TaxRateLTCG*100, cliio.FormatUSDMicros(ltcgTaxMicros))
	fmt.Fprintf(writer, "Total Tax:       %s\n", cliio.FormatUSDMicros(totalTaxMicros))
	fmt.Fprintf(writer, "\n")
	fmt.Fprintf(writer, "After-Tax Value: %s\n", cliio.FormatUSDMicros(afterTaxMicros))
	return nil
}
