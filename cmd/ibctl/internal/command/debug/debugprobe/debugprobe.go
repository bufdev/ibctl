// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package debugprobe implements the "debug probe" command for testing API date ranges.
package debugprobe

import (
	"context"
	"errors"
	"fmt"
	"time"

	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/pkg/ibkrflexquery"
	"github.com/bufdev/ibctl/internal/standard/xtime"
	"github.com/spf13/pflag"
)

const (
	// fromFlagName is the flag name for the start date.
	fromFlagName = "from"
	// toFlagName is the flag name for the end date.
	toFlagName = "to"
	// ibkrTokenEnvVar is the environment variable name for the IBKR token.
	ibkrTokenEnvVar = "IBKR_TOKEN"
)

// NewCommand returns a new debug probe command for testing API date ranges.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	flags := newFlags()
	return &appcmd.Command{
		Use:   name,
		Short: "Probe the IBKR Flex Query API with a specific date range",
		Long: `Probe the IBKR Flex Query API with a specific date range.

Makes a single API call with the given --from and --to dates (YYYYMMDD format)
and prints the number of trades, positions, and cash transactions returned.
Does not write to the data cache. Useful for testing whether the API supports
historical date ranges beyond 365 days from today.`,
		Args: appcmd.NoArgs,
		Run: builder.NewRunFunc(
			func(ctx context.Context, container appext.Container) error {
				return run(ctx, container, flags)
			},
		),
		BindFlags: flags.Bind,
	}
}

type flags struct {
	// From is the start date (YYYYMMDD).
	From string
	// To is the end date (YYYYMMDD).
	To string
}

func newFlags() *flags {
	return &flags{}
}

// Bind registers the flag definitions with the given flag set.
func (f *flags) Bind(flagSet *pflag.FlagSet) {
	flagSet.StringVar(
		&f.From,
		fromFlagName,
		"",
		"Start date (YYYYMMDD, required)",
	)
	flagSet.StringVar(
		&f.To,
		toFlagName,
		"",
		"End date (YYYYMMDD, required)",
	)
}

func run(ctx context.Context, container appext.Container, flags *flags) error {
	if flags.From == "" || flags.To == "" {
		return appcmd.NewInvalidArgumentError("--from and --to are both required")
	}
	// Parse the date flags (YYYYMMDD format).
	fromDate, err := parseYYYYMMDD(flags.From)
	if err != nil {
		return appcmd.NewInvalidArgumentErrorf("invalid --from date %q, expected YYYYMMDD format: %v", flags.From, err)
	}
	toDate, err := parseYYYYMMDD(flags.To)
	if err != nil {
		return appcmd.NewInvalidArgumentErrorf("invalid --to date %q, expected YYYYMMDD format: %v", flags.To, err)
	}
	// Read config for the query ID.
	config, err := ibctlconfig.ReadConfig(container.ConfigDirPath())
	if err != nil {
		return err
	}
	// Read the IBKR token from the environment.
	ibkrToken := container.Env(ibkrTokenEnvVar)
	if ibkrToken == "" {
		return errors.New("IBKR_TOKEN environment variable is required, set it to your IBKR Flex Web Service token (see \"ibctl --help\" for details)")
	}
	// Make a single API call with the specified date range.
	logger := container.Logger()
	client := ibkrflexquery.NewClient(logger)
	logger.Info("probing API", "from", fromDate.String(), "to", toDate.String(), "query_id", config.IBKRQueryID)
	statement, err := client.Download(ctx, ibkrToken, config.IBKRQueryID, fromDate, toDate)
	if err != nil {
		return fmt.Errorf("probe failed: %w", err)
	}
	// Print results to stdout.
	_, err = fmt.Fprintf(
		container.Stdout(),
		"trades: %d\npositions: %d\ncash_transactions: %d\n",
		len(statement.Trades),
		len(statement.OpenPositions),
		len(statement.CashTransactions),
	)
	return err
}

// parseYYYYMMDD parses a date string in YYYYMMDD format into an xtime.Date.
func parseYYYYMMDD(s string) (xtime.Date, error) {
	t, err := time.Parse("20060102", s)
	if err != nil {
		return xtime.Date{}, err
	}
	return xtime.TimeToDate(t), nil
}
